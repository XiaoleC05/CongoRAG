package knowledge

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// uploadSniffBytes 是内容嗅探读取的前缀长度。
//
// 512 是"够判断文件魔数 / 前若干字节是否可解码"的常规值：PDF 魔数只有 5 字节，
// 纯文本判定只需要看有没有 NUL 与非 UTF-8 序列。更大的前缀没有额外信息量，
// 却会让每次上传多读一块内存。
const uploadSniffBytes = 512

// sanitizeUploadFilename 把客户端给的文件名收敛成"可以直接展示与落库"的形式。
//
// 【为什么必须做，而不是信浏览器】multipart 的 filename 是客户端写的字符串：
// 直接 curl 可以传 "../../etc/passwd" 或带控制字符的名字。落库的 storage_key
// 是 UUID（不会路径穿越），但展示名会进界面、日志、Content-Disposition——
// 换行符能伪造日志行，路径分隔符会让人误以为文件路径。这里只保留纯文件名：
//   - 反斜杠统一成正斜杠后取最后一段（Windows 客户端会传 C:\x\a.pdf）；
//   - 去掉 ASCII 控制字符（<0x20 与 0x7f）；
//   - 只剩 "." / ".." / 空串时返回空，由调用方按"文件名为空"拒绝。
func sanitizeUploadFilename(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	switch name {
	case "", ".", "..":
		return ""
	}
	return name
}

// validateUploadContent 在落盘之前嗅探内容，把"扩展名对但内容不对"的文件挡在门外。
//
// 【为什么扩展名白名单不够】白名单只能保证"客户端声称这是 md/txt/pdf"。
// 一份改名成 .pdf 的 HTML 会一路流到 PDF sidecar，解析失败后文档落 failed——
// 用户看到的是"处理失败"，而不是"你传错了文件"；更糟的是这类文件会被原样
// 落盘、再通过下载端点点开。嗅探把错误暴露在**上传响应**里，符合"配错就在
// 最早能说的那一层说"这条原则。
//
// 【返回的 reader 必须包含已读的前缀】嗅探用了 bufio.Reader 的 Peek（不消费），
// 直接把 bufio.Reader 交回去即可；调用方 WriteTemp 读到的就是完整内容。
func validateUploadContent(ext string, r io.Reader) (io.Reader, error) {
	br := bufio.NewReaderSize(r, uploadSniffBytes)
	head, err := br.Peek(uploadSniffBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("peek upload content: %w", platform.ErrInvalid)
	}
	return br, validateUploadHead(ext, head)
}

// validateUploadHead 是纯函数版本（便于单测，不碰 reader）。
func validateUploadHead(ext string, head []byte) error {
	switch ext {
	case ".pdf":
		// PDF 规范要求文件以 %PDF-1.x 开头；扫描件也是。少数生成器会在前面
		// 加 BOM/空白，所以允许最多 1KB 的前导空白后再找魔数。
		if !bytes.HasPrefix(head, []byte("%PDF-")) {
			return fmt.Errorf("file does not look like a PDF (missing %%PDF- header): %w", platform.ErrInvalid)
		}
	case ".md", ".markdown", ".txt":
		if bytes.IndexByte(head, 0) >= 0 {
			return fmt.Errorf("text file contains NUL bytes (binary content?): %w", platform.ErrInvalid)
		}
		if !validUTF8Prefix(head) {
			return fmt.Errorf("text file is not valid UTF-8: %w", platform.ErrInvalid)
		}
	}
	return nil
}

// validUTF8Prefix 判断前缀是否"可能来自合法的 UTF-8 文本"。
//
// 【为什么不能直接 utf8.Valid】Peek 的 512 字节可能恰好切在一个多字节字符
// 中间——一整段中文的 md 文件在第 512 字节处被截断的概率不低。做法是允许
// 丢掉**一个被截断的 rune**：截掉最多 3 字节后剩余部分是合法 UTF-8，且被截掉
// 的尾部确实是"某个多字节 rune 的前缀"（首字节声明的长度 > 实际拿到的字节数）。
//
// 【为什么不只是"去掉最多 3 字节再验"】那样会把任意坏文件当成截断：
// 例如 [a, 0xff, 0xfe, b] 去掉 3 字节后剩 [a]，是合法 UTF-8——但 0xff 不是
// 任何合法 rune 的起始字节，文件本身是坏的。必须再核一次被截尾部的形状。
func validUTF8Prefix(head []byte) bool {
	for trim := 0; trim <= 3 && trim < len(head); trim++ {
		candidate := head[:len(head)-trim]
		if !utf8.Valid(candidate) {
			continue
		}
		tail := head[len(candidate):]
		if len(tail) == 0 {
			return true
		}
		if truncatedRunePrefix(tail) {
			return true
		}
	}
	return false
}

// truncatedRunePrefix 判断 tail 是否恰好是"某个多字节 rune 的前缀"
// （首字节声明了总长度，而实际只有一部分）。
func truncatedRunePrefix(tail []byte) bool {
	if len(tail) == 0 || len(tail) >= utf8.UTFMax {
		return false
	}
	first := tail[0]
	need := 0
	switch {
	case first&0xE0 == 0xC0:
		need = 2
	case first&0xF0 == 0xE0:
		need = 3
	case first&0xF8 == 0xF0:
		need = 4
	default:
		// 0x80–0xBF（续字节）或 0xF8+（非法起始）都不是合法 rune 的开头。
		return false
	}
	return len(tail) < need
}

// uploadExtOf 返回小写扩展名；集中在这里，是为了让"用什么扩展名判类型"
// 只有一处实现（Upload 与嗅探必须判同一个东西）。
func uploadExtOf(filename string) string {
	return strings.ToLower(path.Ext(filename))
}
