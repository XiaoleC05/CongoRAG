// Package pdfparse 调 Python PDF 解析 sidecar（_dev/pdf-service），
// 把学术 PDF 变成带"页码 + 归一化坐标 + 章节路径"的结构化分块。
//
// 【为什么是 HTTP 而不是 Go 内嵌 PDF 库】双栏阅读顺序、扫描件 OCR、公式/
// 表格结构、541 页长文档分片这些能力都在 Python 生态里（PyMuPDF/RapidOCR），
// 用 Go 重写既慢又差。sidecar 只做"解析"这一件事，不提供公网端点，由
// worker 在进程内调用；接口契约见 _dev/CONTRACT_pdf-service.md。
//
// 【这个包不 import knowledge】knowledge 声明 PDFParser 端口，本包的
// ParsePDF 方法签名与它逐字相同，装配根直接把自己传进去——依赖方向是
// 装配根 → 两边，而不是 knowledge ↔ pdfparse 互相认识。
package pdfparse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// maxBlockRunes 是单个分块进 embedding 前的上限（按字符数）。
//
// sidecar 输出的 block 通常已经是一段，但公式块/表格块可能整页一整块；
// 超过上限就按 rune 硬切（保留同一页的 bbox/section），避免 embedding
// 请求因为单条过长被上游拒绝。这个值刻意与 knowledge 的 1200 字符切分
// 区分开：那是"纯文本文档"的策略，这里 serviser 已经按学术结构切好了，
// 只需要一个兜底上限。
const maxBlockRunes = 4000

// Client 是 sidecar 的 HTTP 客户端。BaseURL 形如 http://127.0.0.1:8000。
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 造一个客户端。timeout 是单次 /parse 的上限——541 页大文档
// 可能要几分钟，所以默认给 3 分钟（调用方传）。
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// response 是 sidecar /parse 的 JSON 形状（字段见接口契约）。
type response struct {
	DocID         string  `json:"doc_id"`
	Filename      string  `json:"filename"`
	PageCount     int     `json:"page_count"`
	ParserVersion string  `json:"parser_version"`
	Blocks        []block `json:"blocks"`
	Stats         struct {
		PagesParsed int `json:"pages_parsed"`
		OCRPages    int `json:"ocr_pages"`
	} `json:"stats"`
}

type block struct {
	ID          string    `json:"id"`
	Page        int       `json:"page"`
	BBox        []float64 `json:"bbox"`
	Type        string    `json:"type"`
	Text        string    `json:"text"`
	SectionPath []string  `json:"section_path"`
	Level       *int      `json:"level"`
}

// ParsePDF 实现 knowledge.PDFParser 端口：上传 PDF 原始字节，返回
// (页数, 解析器版本, 分块列表, error)。
//
// 【错误分类】sidecar 不可达/非 200 → 返回 error，由 knowledge 侧走
// failProcessing（重试到终态）；返回 200 但 blocks 全空 → 视为"解析成功
// 但没有可取文本"的合法结果（扫描件 OCR 失败就是这种），让文档能标 ready
// 并保留原始文件，而不是卡在 processing。
func (c *Client) ParsePDF(ctx context.Context, filename string, content []byte) (int, string, []domain.Chunk, error) {
	if c == nil || c.baseURL == "" {
		return 0, "", nil, errors.New("pdf parser is not configured: CONGORAG_PDF_SERVICE_URL is empty")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return 0, "", nil, fmt.Errorf("build multipart for %s: %w", filename, err)
	}
	if _, err := part.Write(content); err != nil {
		return 0, "", nil, fmt.Errorf("write pdf body for %s: %w", filename, err)
	}
	if err := mw.Close(); err != nil {
		return 0, "", nil, fmt.Errorf("close multipart for %s: %w", filename, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse", &body)
	if err != nil {
		return 0, "", nil, fmt.Errorf("new parse request for %s: %w", filename, err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("call pdf service for %s: %w", filename, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, "", nil, fmt.Errorf("read pdf service response for %s: %w", filename, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", nil, fmt.Errorf("pdf service returned %d for %s: %s",
			resp.StatusCode, filename, snippet(raw))
	}

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, "", nil, fmt.Errorf("decode pdf service response for %s: %w", filename, err)
	}
	if r.PageCount <= 0 {
		return 0, "", nil, fmt.Errorf("pdf service returned page_count=%d for %s", r.PageCount, filename)
	}

	parser := r.ParserVersion
	if parser == "" {
		parser = "pdf-service"
	}
	return r.PageCount, parser, chunksFromBlocks(r.Blocks), nil
}

// chunksFromBlocks 把 sidecar 的 block 列表映射成 domain.Chunk。
//
// 【为什么保留 page 而不是拼成一篇长文再切】页码是"溯源"的锚点：引用跳转、
// 阅读器高亮、评测的页码命中率都依赖它。按 block 1:1 保留，等于把
// sidecar 的学术结构（标题/公式/表格/参考文献）原样带进检索层。
func chunksFromBlocks(blocks []block) []domain.Chunk {
	out := make([]domain.Chunk, 0, len(blocks))
	for _, b := range blocks {
		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}
		if b.Page <= 0 {
			continue // 没有页码的块无法溯源，宁可不入索引
		}
		typ := b.Type
		if typ == "" {
			typ = "paragraph"
		}
		section := append([]string(nil), b.SectionPath...)
		bbox := append([]float64(nil), b.BBox...)

		for _, piece := range splitRunes(text, maxBlockRunes) {
			out = append(out, domain.Chunk{
				Content:     piece,
				PageStart:   b.Page,
				PageEnd:     b.Page,
				BBox:        bbox,
				SectionPath: section,
				BlockType:   typ,
			})
		}
	}
	return out
}

// splitRunes 按 rune 边界把超长文本切成若干片；不产生空片。
func splitRunes(s string, limit int) []string {
	if utf8.RuneCountInString(s) <= limit {
		return []string{s}
	}
	runes := []rune(s)
	out := make([]string, 0, len(runes)/limit+1)
	for len(runes) > 0 {
		n := limit
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// snippet 截断错误响应体，避免把整页 HTML 灌进日志/错误链。
func snippet(b []byte) string {
	const max = 500
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
