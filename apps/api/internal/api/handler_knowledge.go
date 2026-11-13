// 知识库与文档相关的端点：CRUD、上传下载、解析产物、版本差异与重索引。
//
// 【为什么上传/下载也在这里】它们的资源是文档，和列表/删除共享同一套
// 归属校验与文件存储句柄；拆到单独文件只会让"文档的生命周期"分散到两处。
//
// 依赖：knowledge.Usecase（业务）、knowledge.FileStore（原始文件）、
// retrieval（重索引时换 embedding 后的重算）。所有错误都走 problem 映射，
// 本文件不直接写 HTTP 状态码字符串。
package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/prompt"
)

func (s *Server) ListKnowledgeBases(c *gin.Context) {
	// c.Request.Context() 是 net/http 给请求挂的 ctx，一路传到 pgx。
	// 客户端断开时整条链上的操作会被打断。
	kbs, err := s.deps.Knowledge.List(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIKBList(kbs))
}

func (s *Server) CreateKnowledgeBase(c *gin.Context) {
	var req CreateKnowledgeBaseRequest

	// 解析失败说明请求体不是合法 JSON，或者结构上对不上契约。
	// 这是参数问题，包成 ErrInvalid 让 fail 返回 400 而不是 500。
	if !s.bindJSON(c, &req) {
		return
	}

	kb, err := s.deps.Knowledge.Create(c.Request.Context(), req.Name)
	if err != nil {
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusCreated, toAPIKB(kb))
}

func (s *Server) GetKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	// id 由包装层从 URL 取出来、按契约里的 format: uuid 校验并转换后传入。
	kb, err := s.deps.Knowledge.Get(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIKB(kb))
}

func (s *Server) RenameKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	var req RenameKnowledgeBaseRequest
	if !s.bindJSON(c, &req) {
		return
	}

	if err := s.deps.Knowledge.Rename(c.Request.Context(), id, req.Name); err != nil {
		s.fail(c, err)
		return
	}

	// 204 不能带响应体，所以用 c.Status 而不是 c.JSON。
	c.Status(http.StatusNoContent)
}

func (s *Server) DeleteKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.Knowledge.Delete(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) ListDocuments(c *gin.Context, id openapi_types.UUID, params ListDocumentsParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	docs, next, err := s.deps.Knowledge.ListDocuments(c.Request.Context(), id, cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, DocumentPage{
		Items:      toAPIDocumentList(docs),
		NextCursor: nullableCursor(next),
	})
}

// UploadDocument 手动解析 multipart 表单——oapi-codegen 对 multipart
// 请求体不像 JSON 那样生成绑定代码，ServerInterface 里这个方法的签名
// 就是普通的 (c *gin.Context, id)，body 需要自己从 c.Request 里取。
//
// 【为什么用 c.Request.FormFile 而不是先 c.MultipartForm()】
// FormFile 只解析请求体到拿到这一个字段为止，不会把整个 multipart body
// 缓存进内存——上传大文件时更省内存。这个方法只需要一个字段（file），
// 不需要 MultipartForm() 那种"拿到全部字段"的能力。
//
// 【大小上限必须在 FormFile 之前设】FormFile 内部会调
// ParseMultipartForm(defaultMaxMemory)，defaultMaxMemory 是 32MB：超出的
// 部分会被完整读进来、溢写到 os.TempDir，之后才轮到这里有机会拒绝。
// MaxBytesReader 把上限提前到"读取阶段"，超限的请求体既不进内存也不落盘。
func (s *Server) UploadDocument(c *gin.Context, kbID openapi_types.UUID) {
	if s.deps.MaxUploadBytes > 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.deps.MaxUploadBytes)
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		// 【超限和别的读取失败要分开报】MaxBytesReader 超限返回的是
		// *http.MaxBytesError，它的文案（"http: request body too large"）是
		// 标准库内部的说法，对客户端没有意义——换成一句带上限值的。
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.fail(c, fmt.Errorf(
				"upload exceeds the %d byte limit: %w", tooLarge.Limit, platform.ErrInvalid))
			return
		}
		s.fail(c, fmt.Errorf("read multipart field %q: %w", "file", platform.ErrInvalid))
		return
	}
	defer file.Close()

	doc, err := s.deps.Knowledge.Upload(c.Request.Context(), kbID, header.Filename, file)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toAPIDocument(doc))
}

func (s *Server) GetDocument(c *gin.Context, id openapi_types.UUID) {
	doc, err := s.deps.Knowledge.GetDocument(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIDocument(doc))
}

// ListDocumentReferences 返回参考文献并标注库内命中（科研助手 M5）。
func (s *Server) ListDocumentReferences(c *gin.Context, id openapi_types.UUID) {
	entries, err := s.deps.Knowledge.References(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]ReferenceEntry, 0, len(entries))
	for _, e := range entries {
		item := ReferenceEntry{
			Index: e.Index,
			Text:  e.Text,
			Page:  e.Page,
		}
		if e.MatchedDocumentID != "" {
			docID := e.MatchedDocumentID
			name := e.MatchedFilename
			item.MatchedDocumentId = &docID
			item.MatchedFilename = &name
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// GetDocumentOutline 返回阅读器目录（科研助手 M3）。
func (s *Server) GetDocumentOutline(c *gin.Context, id openapi_types.UUID) {
	entries, err := s.deps.Knowledge.Outline(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]OutlineEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, OutlineEntry{Title: e.Title, Page: e.Page, Level: e.Level})
	}
	c.JSON(http.StatusOK, out)
}

// DownloadDocumentFile 把原始文件字节流给阅读器（PDF.js）。
//
// 【为什么不缓存】本地单机应用、文件几 MB，每次从磁盘读比维护一层缓存
// 简单；真要缓存留给公网部署那层（反向代理）去做。
func (s *Server) DownloadDocumentFile(c *gin.Context, id openapi_types.UUID) {
	ctx := c.Request.Context()

	doc, err := s.deps.Knowledge.GetDocument(ctx, id)
	if err != nil {
		s.fail(c, err)
		return
	}

	rc, err := s.deps.Files.Open(ctx, doc.StorageKey)
	if err != nil {
		// 元信息还在、文件没了，是"数据库和磁盘不同步"的坏状态。
		// 对客户端来说就是 404：它要的东西不在了。
		if errors.Is(err, os.ErrNotExist) {
			s.fail(c, fmt.Errorf("file of document %s is gone: %w", id, platform.ErrNotFound))
			return
		}
		s.fail(c, fmt.Errorf("open file of document %s: %w", id, err))
		return
	}
	defer func() { _ = rc.Close() }()

	contentType := "application/octet-stream"
	disposition := "attachment"
	if strings.HasSuffix(strings.ToLower(doc.Filename), ".pdf") {
		contentType = "application/pdf"
		// inline：浏览器内置 PDF 查看器直接打开，不弹下载框
		disposition = "inline"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, doc.Filename))
	c.Header("Cache-Control", "private, max-age=300")

	if _, err := io.Copy(c.Writer, rc); err != nil {
		// 响应头已经发出去了，这里只能记日志——客户端会看到截断的响应，
		// 那是网络侧的失败，不是我们能改写的 HTTP 错误。
		s.deps.Logger.Warn("stream document file failed",
			"document_id", id, "error", err)
	}
}

func (s *Server) DeleteDocument(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.Knowledge.DeleteDocument(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ResolveDocumentDuplicate 处理上传时去重的后续动作（科研助手 M1）。
func (s *Server) ResolveDocumentDuplicate(c *gin.Context, id openapi_types.UUID) {
	var req DocumentDuplicateResolution
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("decode duplicate resolution: %w", platform.ErrInvalid))
		return
	}
	if err := s.deps.Knowledge.ResolveDuplicate(c.Request.Context(), id, string(req.Action)); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DiffDocumentVersions 对比同一篇论文两个版本的差异（科研助手 M1 高级表现）。
func (s *Server) DiffDocumentVersions(c *gin.Context, id openapi_types.UUID) {
	var req VersionDiffRequest
	if !s.bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	a, err := s.deps.Knowledge.GetDocument(ctx, id)
	if err != nil {
		s.fail(c, err)
		return
	}
	b, err := s.deps.Knowledge.GetDocument(ctx, req.OtherDocumentId)
	if err != nil {
		s.fail(c, err)
		return
	}
	if a.KnowledgeBaseID != b.KnowledgeBaseID {
		s.fail(c, fmt.Errorf("documents belong to different knowledge bases: %w", platform.ErrInvalid))
		return
	}
	older, newer := compareVersions(a, b)
	olderChunks, err := s.deps.Knowledge.ChunksPreview(ctx, older.ID, 6)
	if err != nil {
		s.fail(c, err)
		return
	}
	newerChunks, err := s.deps.Knowledge.ChunksPreview(ctx, newer.ID, 6)
	if err != nil {
		s.fail(c, err)
		return
	}
	system := prompt.MustRender("versiondiff.system", nil)
	user := fmt.Sprintf("旧版本《%s》开头片段：\n%s\n\n新版本《%s》开头片段：\n%s\n\n请输出 3-5 条修订要点。",
		older.Filename, excerptChunks(olderChunks, 2600), newer.Filename, excerptChunks(newerChunks, 2600))
	summary, err := s.deps.LLM.Generate(ctx, system, user)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, VersionDiff{
		Summary:       summary,
		OlderFilename: older.Filename,
		NewerFilename: newer.Filename,
	})
}

// compareVersions 决定两份文档谁是旧版/新版。
//
// 【优先看文件名里的 vN】官方测试用例就是 1706.03762v1 vs v7：上传顺序
// 可能反过来（先传新版再传旧版做去重），只看创建时间会把方向判错。
// 文件名不是"同一 base + vN"时（普通 md/扫描件），退回比较创建时间。
var versionSuffixPattern = regexp.MustCompile(`(?i)v(\d+)\.pdf$`)

func compareVersions(a, b *knowledge.Document) (older, newer *knowledge.Document) {
	sa := versionSuffixPattern.FindStringSubmatch(a.Filename)
	sb := versionSuffixPattern.FindStringSubmatch(b.Filename)
	if sa != nil && sb != nil {
		baseA := strings.ToLower(strings.TrimSpace(a.Filename[:len(a.Filename)-len(sa[0])]))
		baseB := strings.ToLower(strings.TrimSpace(b.Filename[:len(b.Filename)-len(sb[0])]))
		if baseA == baseB {
			na, _ := strconv.Atoi(sa[1])
			nb, _ := strconv.Atoi(sb[1])
			if na != nb {
				if na < nb {
					return a, b
				}
				return b, a
			}
		}
	}
	if b.CreatedAt.Before(a.CreatedAt) {
		return b, a
	}
	return a, b
}

// ReindexDocument 重新索引一份文档（issue #39）。
//
// 【202 而不是 200】和上传一样，这里只是"排上了队"——真正干活的是 worker，
// 返回时文档是 queued 状态。
//
// 【为什么回读一次】响应里的 status 必须是数据库里真实的值。直接拼一个
// queued 看起来一样，但那是"服务端以为的状态"，而这里是唯一能让调用方
// 确认"状态真的变了"的地方。
func (s *Server) ReindexDocument(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.Knowledge.ReindexDocument(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}

	doc, err := s.deps.Knowledge.GetDocument(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toAPIDocument(doc))
}

// ReindexKnowledgeBase 重新索引一个知识库下的全部文档（issue #39）。
func (s *Server) ReindexKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	enqueued, err := s.deps.Knowledge.ReindexKnowledgeBase(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, ReindexAccepted{Enqueued: enqueued})
}

// GetCitationGraph 返回知识库的局域引用图谱（科研助手 M5）。
func (s *Server) GetCitationGraph(c *gin.Context, id openapi_types.UUID) {
	g, err := s.deps.Knowledge.CitationGraph(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	nodes := make([]CitationNode, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes = append(nodes, CitationNode{
			DocumentId:     n.DocumentID,
			Filename:       n.Filename,
			Title:          n.Title,
			CitationCount:  n.CitationCount,
			ReferenceCount: n.ReferenceCount,
			PageRank:       n.PageRank,
			Cornerstone:    n.Cornerstone,
		})
	}
	edges := make([]CitationEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, CitationEdge{From: e.From, To: e.To, Weight: e.Weight})
	}
	c.JSON(http.StatusOK, CitationGraph{Nodes: nodes, Edges: edges})
}
