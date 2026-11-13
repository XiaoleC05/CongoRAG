package knowledge

import (
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

func TestSanitizeUploadFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"paper.pdf", "paper.pdf"},
		{"  paper.pdf  ", "paper.pdf"},
		{"C:\\Users\\me\\paper.pdf", "paper.pdf"},
		{"../../etc/passwd", "passwd"},
		{"dir/sub/paper.md", "paper.md"},
		{"weird\x00name\x1b.txt", "weirdname.txt"},
		{"..", ""},
		{".", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, sanitizeUploadFilename(c.in), "input=%q", c.in)
	}
}

func TestValidateUploadHead_PDF(t *testing.T) {
	require.NoError(t, validateUploadHead(".pdf", []byte("%PDF-1.7\n...")))
	err := validateUploadHead(".pdf", []byte("<html><body>not a pdf</body></html>"))
	require.ErrorIs(t, err, platform.ErrInvalid)
	err = validateUploadHead(".pdf", []byte(""))
	require.ErrorIs(t, err, platform.ErrInvalid)
}

func TestValidateUploadHead_Text(t *testing.T) {
	require.NoError(t, validateUploadHead(".md", []byte("# 标题\n\n正文，含中文。")))
	require.NoError(t, validateUploadHead(".txt", []byte("plain text")))

	err := validateUploadHead(".txt", []byte("binary\x00content"))
	require.ErrorIs(t, err, platform.ErrInvalid)

	// 非法 UTF-8：0xff 不是合法起始字节。
	err = validateUploadHead(".md", []byte{'a', 0xff, 0xfe, 'b'})
	require.ErrorIs(t, err, platform.ErrInvalid)
}

// TestValidUTF8Prefix_HandlesCutRune 守住那个真实误报：512 字节的嗅探窗口
// 恰好切在一个 3 字节汉字中间时，文件本身是合法 UTF-8，不能被判成坏文件。
func TestValidUTF8Prefix_HandlesCutRune(t *testing.T) {
	// "中文测试" 每字 3 字节；512 % 36 = 8，正好停在第三个汉字的第 2 个字节。
	full := []byte(strings.Repeat("中文测试", 200))
	head := full[:512]
	require.False(t, utf8.Valid(head), "fixture 必须真的切在多字节字符中间")
	require.True(t, validUTF8Prefix(head), "截断的多字节尾部不应被判成非法 UTF-8")

	require.True(t, validUTF8Prefix([]byte("normal ascii")))
	require.False(t, validUTF8Prefix([]byte{'a', 0xff, 0xfe, 0xfd}), "不是合法 rune 起始字节的尾巴必须拒绝")
	require.False(t, validUTF8Prefix([]byte{0xff}))
	require.True(t, validUTF8Prefix([]byte{'a', 0xC3}), "紧邻合法前文的截断 2 字节 rune 前缀应被容忍")
}

func TestValidateUploadContent_ReturnsFullReader(t *testing.T) {
	// 嗅探不能吃掉前缀：返回的 reader 读出来必须等于原文。
	src := "%PDF-1.7\n" + strings.Repeat("x", 2000)
	r, err := validateUploadContent(".pdf", strings.NewReader(src))
	require.NoError(t, err)
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, src, string(got))

	_, err = validateUploadContent(".pdf", strings.NewReader("<html>"))
	require.ErrorIs(t, err, platform.ErrInvalid)
}
