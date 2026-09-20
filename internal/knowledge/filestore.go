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
	"os"
	"path/filepath"
)

var _ FileStore = (*LocalFileStore)(nil)

// tmpSubdir 是临时文件的落脚目录，正式文件和它是同级但不同目录，
// 这样 List（对账用）只需要扫 rootDir 顶层，不用过滤掉临时文件。
const tmpSubdir = "tmp"

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

func (fs *LocalFileStore) WriteTemp(ctx context.Context, r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(fs.tmpDir(), 0o755); err != nil {
		return "", 0, fmt.Errorf("create tmp dir: %w", err)
	}

	f, err := os.CreateTemp(fs.tmpDir(), "upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	// 无论下面成功还是失败都要关闭文件描述符；写完之后调用方靠返回的路径
	// 去 Commit，这里不需要再持有文件句柄。
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		// 写失败时清理掉这个半成品临时文件，不留垃圾。
		_ = os.Remove(f.Name())
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
