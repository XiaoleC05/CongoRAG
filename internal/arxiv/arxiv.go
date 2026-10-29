// Package arxiv 是 arXiv API 的最小客户端（参赛版 M4 的文献检索工具）。
//
// 【为什么自己写而不是引三方库】需求只有一件事：按关键词查论文、把
// 结果解析成结构化数据。arXiv 的 Atom 响应很稳定，encoding/xml 足够；
// 引一个库反而多一条供应链和一套版本约束。
//
// 【为什么要有独立的 Client 接口】Agent 的工具层只依赖一个
// "能按查询返回论文"的接口，测试用它注入假实现；真实网络错误（超时、
// 429、DNS）也统一翻译成 platform.ErrUpstream，HTTP 边界按同一套错误
// 分类给用户看。
package arxiv

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// DefaultBaseURL 是 arXiv API 的正式地址。
const DefaultBaseURL = "https://export.arxiv.org/api/query"

// maxResultsLimit 是单次检索的返回条数上限。
//
// 【为什么是 25】Agent 的工具结果要进模型上下文，10 条足够；但 M4 的
// "最新论文推荐"要在本地做相关性排序，候选池大一点排序才有意义。
// 上限仍由调用方按场景控制（工具传 10、推荐传 15）。
const maxResultsLimit = 25

// Paper 是一篇 arXiv 论文的展示字段（也是工具返回的 JSON 形状）。
type Paper struct {
	ID        string   `json:"id"`        // arXiv id（如 1706.03762v7）
	Title     string   `json:"title"`     // 标题（空白已折叠）
	Authors   []string `json:"authors"`   // 作者名
	Abstract  string   `json:"abstract"`  // 摘要（空白已折叠）
	Published string   `json:"published"` // ISO8601 发布时间
	URL       string   `json:"url"`       // abs 页面链接
	PDFURL    string   `json:"pdfUrl"`    // PDF 链接（ingest 用）
	Comment   string   `json:"comment"`   // arXiv comment（常含代码/GitHub 链接）
}

// Client 访问 arXiv API。
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient 构造一个带超时的客户端。
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    DefaultBaseURL,
	}
}

// Search 按 query 检索论文（query 会作为 search_query 的值直接拼进去）。
//
// 【caller 负责做什么】query 里的字段前缀（all: / ti: / au:）由调用方
// 决定——工具层把模型给的 query 原样传进来，这里只做 URL 编码。
func (c *Client) Search(ctx context.Context, query string, maxResults int) ([]Paper, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty arxiv query: %w", platform.ErrInvalid)
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > maxResultsLimit {
		maxResults = maxResultsLimit
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse arxiv base url: %w", err)
	}
	q := u.Query()
	q.Set("search_query", query)
	q.Set("start", "0")
	q.Set("max_results", fmt.Sprintf("%d", maxResults))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build arxiv request: %w", err)
	}
	req.Header.Set("User-Agent", "CongoRAG/0.1 (competition demo)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call arxiv api: %w: %v", platform.ErrUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv api returned status %d: %w", resp.StatusCode, platform.ErrUpstream)
	}

	var feed atomFeed
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("decode arxiv atom response: %w: %v", platform.ErrUpstream, err)
	}

	out := make([]Paper, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		p := Paper{
			ID:        shortenID(e.ID),
			Title:     collapseSpace(e.Title),
			Abstract:  collapseSpace(e.Summary),
			Published: strings.TrimSpace(e.Published),
			URL:       e.ID,
		}
		for _, a := range e.Authors {
			if name := strings.TrimSpace(a.Name); name != "" {
				p.Authors = append(p.Authors, name)
			}
		}
		for _, l := range e.Links {
			if l.Rel == "alternate" && l.Type == "text/html" {
				p.URL = l.HREF
			}
			if l.Type == "application/pdf" {
				p.PDFURL = strings.Replace(l.HREF, "http://", "https://", 1)
			}
		}
		p.Comment = collapseSpace(e.Comment)
		if p.ID == "" || p.Title == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// DownloadPDF 下载一篇 arXiv 论文的 PDF 原始字节（M4 的"下钻入库"用）。
//
// 【只允许 arXiv 域名】这是服务端发起的出站请求，URL 来自前端/模型时
// 必须做 host 白名单，否则就是一个 SSRF 入口。
// 【大小上限】论文 PDF 一般几 MB，60MB 上限挡住异常大文件。
func (c *Client) DownloadPDF(ctx context.Context, pdfURL string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(pdfURL))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid pdf url: %w", platform.ErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	if host != "arxiv.org" && host != "www.arxiv.org" && host != "export.arxiv.org" {
		return nil, fmt.Errorf("refuse to download non-arxiv pdf host %q: %w", host, platform.ErrInvalid)
	}
	u.Scheme = "https"
	u.Host = host
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build pdf request: %w", err)
	}
	req.Header.Set("User-Agent", "CongoRAG/0.1 (competition demo)")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download arxiv pdf: %w: %v", platform.ErrUpstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv pdf returned status %d: %w", resp.StatusCode, platform.ErrUpstream)
	}
	const maxPDFBytes = 60 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPDFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read arxiv pdf: %w: %v", platform.ErrUpstream, err)
	}
	if len(data) > maxPDFBytes {
		return nil, fmt.Errorf("arxiv pdf exceeds %d bytes: %w", maxPDFBytes, platform.ErrInvalid)
	}
	return data, nil
}

// atomFeed 只声明用得到的字段；arXiv 的其它元素被忽略。
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Summary   string     `xml:"summary"`
	Published string     `xml:"published"`
	Comment   string     `xml:"http://arxiv.org/schemas/atom comment"`
	Authors   []atomName `xml:"author"`
	Links     []atomLink `xml:"link"`
}

type atomName struct {
	Name string `xml:"name"`
}

type atomLink struct {
	HREF string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

// shortenID 把 http://arxiv.org/abs/1706.03762v7 变成 1706.03762v7。
func shortenID(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.LastIndex(s, "/abs/"); i >= 0 {
		return s[i+len("/abs/"):]
	}
	return s
}

// collapseSpace 把换行/连续空白折成单个空格（Atom 里的标题和摘要
// 经常带缩进换行，直接展示会破坏排版）。
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
