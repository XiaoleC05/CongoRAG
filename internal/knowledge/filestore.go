// filestore.go 是 FileStore 的本地文件系统实现。
//
// 写路径的三步（写临时文件 → fsync → rename）在这里落地成具体的 os 调用；
// usecase.go 的 Upload 方法只调用这个文件导出的方法名，不知道下面是
// os.CreateTemp 还是别的实现——将来要换成 S3 之类的对象存储，只需要换
// 这一个文件，port.go 的 FileStore 接口和 Upload 方法都不用动。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var _ FileStore = (*LocalFileStore)(nil)

// tmpSubdir 是临时文件的落脚目录，正式文件和它是同级但不同目录，
// 这样 List（对账用）只需要扫 rootDir 顶层，不用过滤掉临时文件。
const tmpSubdir = "tmp"

// tmpFilePrefix 是临时文件名的前缀，os.CreateTemp 会在这后面接一串随机字符。
//
// 【为什么要单独留一个常量给清扫用】清扫只该删自己造的文件——把 tmp/ 里
// 一切文件都删掉的话，运维往那儿放的东西也会一起没。前缀是这里唯一
// 能识别"这是上传途中的半成品"的依据（见 SweepTemp）。
const tmpFilePrefix = "upload-"

// LocalFileStore 把 rootDir 当成所有文档的存储根目录
// （默认 platform.Config.DocumentsDir，即 ./data/documents）。
type LocalFileStore struct {
	rootDir string
}

func NewLocalFileStore(rootDir string) *LocalFileStore {
	return &LocalFileStore{rootDir: rootDir}
}

func (fs *LocalFileStore) tmpDir() string {
	return filepath.Join(fs.rootDir, tmpSubdir)
}

// tempFile 是 closeAndRemoveTempFile 需要的最小文件能力。
//
// 【为什么要这个接缝】下面那个顺序（先关再删）只在 Windows 上才看得出对错：
// POSIX 允许 unlink 一个仍被打开的文件，所以旧顺序在 Linux/macOS 上也能删成功，
// 于是「修复」与「改回 bug」在 CI 跑的那台机器上表现完全一样——那是一段没有
// 门禁的代码。把顺序抽成一个具名步骤之后，就能用假句柄直接断言调用次序，
// 不再依赖运行平台。
//
// 接口故意只有两个方法：真实实现是 *os.File，测试实现是个几行的假结构体。
// 不给 LocalFileStore 加字段、不改 port.go 的 FileStore 接口，是为了不为了
// 一条断言把整个 os 调用面抽象出来。
type tempFile interface {
	Close() error
	Name() string
}

// closeAndRemoveTempFile 关掉一个写了一半的临时文件，再删除它。
//
// 【顺序不能换，而且换错了在 Linux 上看不出来】Windows 上 os.CreateTemp
// 打开文件时只带 FILE_SHARE_READ|FILE_SHARE_WRITE（没有 FILE_SHARE_DELETE），
// 句柄还开着的时候 os.Remove 会以共享冲突失败——先删后关等于什么都没删，
// 半成品会永久留在 tmp/ 里。Linux/macOS 允许 unlink 已打开的文件，所以那里
// 两种顺序都能删成功。这条顺序由一个与平台无关的测试钉住：
// TestCloseAndRemoveTempFile_ClosesBeforeRemoving。
//
// 【关闭失败也要继续尝试删除】关闭失败只说明句柄没干净地释放，那个半成品
// 留在 tmp/ 里没有任何好处；调用方会把 closeErr 记进日志，而 SweepTemp 是
// 最后一道兜底。所以这里不做「关了失败就跳过删除」的短路。
func closeAndRemoveTempFile(f tempFile, remove func(path string) error) (closeErr, removeErr error) {
	closeErr = f.Close()
	removeErr = remove(f.Name())
	return closeErr, removeErr
}

func (fs *LocalFileStore) WriteTemp(ctx context.Context, r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(fs.tmpDir(), 0o755); err != nil {
		return "", 0, fmt.Errorf("create tmp dir: %w", err)
	}

	f, err := os.CreateTemp(fs.tmpDir(), tmpFilePrefix+"*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	// 无论下面成功还是失败都要关闭文件描述符；写完之后调用方靠返回的路径
	// 去 Commit，这里不需要再持有文件句柄。
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		// 清理顺序的理由写在 closeAndRemoveTempFile 上（那一段注释原来在
		// 这里，抽出去是为了它在测试里也能被读到）。这次显式 Close 之后，
		// 上面那个 defer 的 Close 只是个幂等兜底。
		closeErr, removeErr := closeAndRemoveTempFile(f, os.Remove)

		// 删不掉也不能让手上这个"写失败"变成静默：文件会留在
		// tmp/ 里，最后由挂载对账的 SweepTemp 按 mtime 兜底清掉。
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			slog.Default().Error("failed to remove half-written temp file",
				"path", f.Name(), "remove_error", removeErr, "close_error", closeErr)
		}
		return "", 0, fmt.Errorf("write temp file: %w", err)
	}

	return f.Name(), n, nil
}

func (fs *LocalFileStore) Commit(ctx context.Context, tmpPath, storageKey string) error {
	// fsync：把数据真正落到磁盘，不是还停在操作系统的页缓存里。
	// 顺序很关键——必须在 rename 之前，否则进程在 fsync 之前崩溃的话，
	// rename 后的正式文件可能只是一个空文件或残缺数据。
	f, err := os.OpenFile(tmpPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("reopen temp file for fsync: %w", err)
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return fmt.Errorf("fsync temp file: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temp file after fsync: %w", closeErr)
	}

	if err := os.MkdirAll(fs.rootDir, 0o755); err != nil {
		return fmt.Errorf("create root dir: %w", err)
	}

	finalPath := filepath.Join(fs.rootDir, storageKey)
	// rename 在同一个文件系统内是原子操作：外部观察者只会看到"文件不存在"
	// 或"文件存在且完整"两种状态之一，不会看到写了一半的中间状态。
	// tmpDir 和 rootDir 在同一个根目录下，保证同一文件系统。
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", storageKey, err)
	}
	return nil
}

func (fs *LocalFileStore) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(fs.rootDir, storageKey))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", storageKey, err)
	}
	return f, nil
}

func (fs *LocalFileStore) RemoveTemp(ctx context.Context, tmpPath string) error {
	err := os.Remove(tmpPath)
	// Commit 成功之后，tmpPath 已经被 rename 走了——这里删不到东西是
	// 意料之中的情况，不是错误。Upload 方法用 defer 无条件调用 RemoveTemp，
	// 这条 no-op 判断是让那个 defer 在成功路径上也能安全调用。
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (fs *LocalFileStore) Remove(ctx context.Context, storageKey string) error {
	err := os.Remove(filepath.Join(fs.rootDir, storageKey))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (fs *LocalFileStore) SweepTemp(ctx context.Context, olderThan time.Time) error {
	entries, err := os.ReadDir(fs.tmpDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// tmp/ 还没被创建过（一次上传都没发生过），没有东西可扫。
			return nil
		}
		return fmt.Errorf("read tmp dir %s: %w", fs.tmpDir(), err)
	}

	for _, e := range entries {
		// 只认自己造的那类文件：前缀不匹配的一律不碰（见 tmpFilePrefix 的注释）。
		if e.IsDir() || !strings.HasPrefix(e.Name(), tmpFilePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// 单个文件 stat 不出来（正在被删、权限不够）不该让整轮清扫失败。
			slog.Default().Warn("failed to stat temp file during sweep",
				"name", e.Name(), "error", err)
			continue
		}
		if !info.ModTime().Before(olderThan) {
			continue
		}
		if err := os.Remove(filepath.Join(fs.tmpDir(), e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Default().Warn("failed to remove stale temp file",
				"name", e.Name(), "error", err)
		}
	}
	return nil
}

func (fs *LocalFileStore) List(ctx context.Context) ([]FileInfo, error) {
	entries, err := os.ReadDir(fs.rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 根目录还没被创建过（一次上传都没发生过），这不是错误，
			// 只是"目前没有任何文件"。
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", fs.rootDir, err)
	}

	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		// tmp/ 子目录不是正式文件，跳过；子目录本身也跳过——
		// 正式文件全部直接放在 rootDir 顶层，不会有别的子目录。
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", e.Name(), err)
		}
		out = append(out, FileInfo{StorageKey: e.Name(), ModTime: info.ModTime()})
	}
	return out, nil
}
