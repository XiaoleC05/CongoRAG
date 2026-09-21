package knowledge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 这一组测试用真实文件系统（t.TempDir），不是内存假实现——
// WriteTemp 失败时的清理、tmp/ 的清扫都是 os 层面的行为，
// 假实现里没有句柄、没有删除失败这回事，测不到。

// failAfterFirstRead 第一次 Read 正常给数据，第二次返回错误——
// 复刻"写到一半写端出错"（磁盘写满 ENOSPC 是最典型的一种）。
type failAfterFirstRead struct {
	first bool
}

func (r *failAfterFirstRead) Read(p []byte) (int, error) {
	if r.first {
		return 0, errors.New("fake write-side failure")
	}
	r.first = true
	return copy(p, "写了一半的内容"), nil
}

// 【这条是#22的回归测试】WriteTemp 写失败时要把半成品删掉。原来的顺序是
// 先 os.Remove 再由 defer 关句柄，而 Windows 上文件在打开状态下删不掉
// （没有 FILE_SHARE_DELETE），删除错误又被 `_ =` 吞掉——半成品会永久留在
// tmp/ 里，且对 FileStore.List 不可见（它只扫 rootDir 顶层）。
//
// 【这条测试只有在 Windows 上才会在修复前变红】POSIX 允许 unlink 一个仍被
// 打开的文件，Linux/macOS 上旧代码也能删掉它。但"先关句柄再删"在两个平台上
// 都是对的，只有错的那半边是平台相关的。
//
// 【所以它不是那条顺序的门禁】CI 跑在 ubuntu-latest 上，这条用例在那里
// 改前改后都绿。真正钉住顺序的是下面与平台无关的
// TestCloseAndRemoveTempFile_ClosesBeforeRemoving；这条继续留着，作为
// "接缝没有和真实文件系统脱节"的锚。
func TestWriteTemp_WriteFails_LeavesNoTempFile(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())

	_, _, err := fs.WriteTemp(context.Background(), &failAfterFirstRead{})

	require.Error(t, err)

	entries, readErr := os.ReadDir(fs.tmpDir())
	require.NoError(t, readErr)
	assert.Empty(t, entries, "写失败的半成品必须被删掉，不能留在 tmp/ 里")
}

// fakeTempFile 记录自己被关了几次，好让断言能说出「删的时候关了没有」。
//
// Close 是幂等的（真实代码在修复路径上会显式关一次、defer 再关一次），
// 所以这里只累加计数，不做"已经关过就报错"那种断言——那会让假实现自己的
// 语义干扰被测的顺序。
type fakeTempFile struct {
	name       string
	closeErr   error
	closeCalls int
}

func (f *fakeTempFile) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakeTempFile) Name() string { return f.name }

// 【这条是与平台无关的回归测试，钉住「先关句柄再删」这个顺序】
//
// 为什么必须有它：上面那条 TestWriteTemp_WriteFails_LeavesNoTempFile 只在
// Windows 上才会在修复前变红，而 CI 跑的是 ubuntu-latest——那段顺序在 CI 上
// 从来没有门禁，改回旧写法不会被任何东西发现，只有开发者的本机会红。
//
// 做法是把顺序本身抽成 closeAndRemoveTempFile，再用假句柄 + 假删除函数
// 直接观察「删除发生时句柄关了没有」。断言写成 Equal(1, ...) 而不是
// True(closed)：顺序被换回去时，失败信息会直接说出「删的时候还没关」。
func TestCloseAndRemoveTempFile_ClosesBeforeRemoving(t *testing.T) {
	f := &fakeTempFile{name: "tmp/upload-abc123"}

	removeCalls := 0
	closeCallsAtRemove := -1
	removedName := ""

	closeErr, removeErr := closeAndRemoveTempFile(f, func(path string) error {
		removeCalls++
		closeCallsAtRemove = f.closeCalls
		removedName = path
		return nil
	})

	require.NoError(t, closeErr)
	require.NoError(t, removeErr)
	assert.Equal(t, 1, removeCalls, "删除必须被尝试一次")
	assert.Equal(t, f.name, removedName, "删的必须是这个句柄指向的文件")
	assert.Equal(t, 1, closeCallsAtRemove,
		"删除发生时句柄必须已经关掉——Windows 上句柄未关时的 os.Remove 会以共享冲突失败")
}

// 关闭失败不等于不删：半成品留在 tmp/ 里没有任何好处，调用方负责把它记进
// 日志，SweepTemp 是最后一道兜底。这条语义现有代码就是对的，只是从来没被
// 任何断言钉住过——将来有人为了"让错误更干净"加一句 early return 就会静默
// 改变行为。
func TestCloseAndRemoveTempFile_RemoveStillAttemptedWhenCloseFails(t *testing.T) {
	f := &fakeTempFile{name: "tmp/upload-abc123", closeErr: errors.New("close failed")}

	removeCalls := 0
	closeErr, removeErr := closeAndRemoveTempFile(f, func(string) error {
		removeCalls++
		return nil
	})

	require.Error(t, closeErr, "关闭失败必须如实报出来，不能吞掉")
	require.NoError(t, removeErr)
	assert.Equal(t, 1, removeCalls, "关闭失败也要继续尝试删除")
}

func TestWriteTemp_Success_ReturnsPathAndSizeAndKeepsFile(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())

	tmpPath, n, err := fs.WriteTemp(context.Background(), strings.NewReader("内容"))

	require.NoError(t, err)
	assert.Equal(t, int64(len("内容")), n, "字节数必须是服务端自己数出来的实际写入量")
	assert.FileExists(t, tmpPath)
}

// 【这条是#22的另一半】tmp/ 里的陈旧半成品要有人扫。只删自己造的文件
// （tmpFilePrefix），且只删够旧的——太新的可能正属于一次进行中的上传。
func TestSweepTemp_RemovesOnlyOldUploadFiles(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())
	require.NoError(t, os.MkdirAll(fs.tmpDir(), 0o755))

	old := time.Now().Add(-time.Hour)
	write := func(name string) string {
		p := filepath.Join(fs.tmpDir(), name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		return p
	}

	stale := write("upload-stale")
	fresh := write("upload-fresh")
	foreign := write("运维放的东西.txt")
	require.NoError(t, os.Chtimes(stale, old, old))
	require.NoError(t, os.Chtimes(foreign, old, old))

	require.NoError(t, fs.SweepTemp(context.Background(), time.Now().Add(-orphanGracePeriod)))

	assert.NoFileExists(t, stale, "够旧的半成品应该被扫掉")
	assert.FileExists(t, fresh, "太新的临时文件可能正属于一次进行中的上传")
	assert.FileExists(t, foreign, "只清自己造的文件，别人放进 tmp/ 的东西不能碰")
}

func TestSweepTemp_NoTmpDir_NoOp(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())

	// 一次上传都没发生过时 tmp/ 根本不存在，这不是错误。
	assert.NoError(t, fs.SweepTemp(context.Background(), time.Now()))
}

// List 的契约是"只列正式文件"：tmp/ 是子目录，必须继续被跳过，
// 否则孤儿对账会把正在写的一半文件当成孤儿。
func TestList_IgnoresTmpSubdir(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())
	require.NoError(t, os.MkdirAll(fs.tmpDir(), 0o755))
	_, _, err := fs.WriteTemp(context.Background(), strings.NewReader("x"))
	require.NoError(t, err)

	files, err := fs.List(context.Background())

	require.NoError(t, err)
	assert.Empty(t, files, "tmp/ 里的临时文件不是正式文件")
}

var _ io.Reader = (*failAfterFirstRead)(nil)
