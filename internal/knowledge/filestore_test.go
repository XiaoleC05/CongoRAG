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
func TestWriteTemp_WriteFails_LeavesNoTempFile(t *testing.T) {
	fs := NewLocalFileStore(t.TempDir())

	_, _, err := fs.WriteTemp(context.Background(), &failAfterFirstRead{})

	require.Error(t, err)

	entries, readErr := os.ReadDir(fs.tmpDir())
	require.NoError(t, readErr)
	assert.Empty(t, entries, "写失败的半成品必须被删掉，不能留在 tmp/ 里")
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
