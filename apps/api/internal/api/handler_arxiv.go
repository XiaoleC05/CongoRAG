// arXiv 相关端点：按库内主题推荐最新论文、赞/踩反馈、一键下载 PDF 入库。
//
// 外部网络调用（arXiv API / PDF 下载）都发生在 internal/arxiv 里，这里只
// 负责参数校验、把抓取结果转成契约类型，以及把"入库"接到既有上传流水线。
// 这里不持久化任何推荐结果：Recommendation 是即时计算的，下载或入库失败
// 直接返回 Problem，前端可以就地重试。
package api

import (
	"bytes"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/recommend"
)

// GetArxivRecommendations 根据知识库主题推荐 arXiv 最新论文（M4）。
func (s *Server) GetArxivRecommendations(c *gin.Context, id openapi_types.UUID) {
	var req ArxivRecommendationRequest
	if !s.bindJSON(c, &req) {
		return
	}
	limit := 8
	if req.Limit != nil && *req.Limit >= 1 && *req.Limit <= 20 {
		limit = *req.Limit
	}
	ctx := c.Request.Context()
	docs, _, err := s.deps.Knowledge.ListDocuments(ctx, id, "", 50)
	if err != nil {
		s.fail(c, err)
		return
	}
	titles := make([]string, 0, len(docs))
	for _, d := range docs {
		title := strings.TrimSuffix(d.Filename, filepath.Ext(d.Filename))
		if outline, oerr := s.deps.Knowledge.Outline(ctx, d.ID); oerr == nil {
			for _, e := range outline {
				if t := strings.TrimSpace(e.Title); t != "" {
					title = t
					break
				}
			}
		}
		titles = append(titles, title)
	}
	if len(titles) == 0 {
		c.JSON(http.StatusOK, ArxivRecommendationList{Items: []ArxivRecommendation{}})
		return
	}
	papers, err := s.deps.Arxiv.Search(ctx, recommend.QueryForTitles(titles), 15)
	if err != nil {
		s.fail(c, err)
		return
	}
	ids := make([]string, 0, len(papers))
	for _, p := range papers {
		ids = append(ids, p.ID)
	}
	votes, _ := s.deps.ArxivVotes.Votes(ctx, s.deps.Querier, id, ids)
	results := recommend.Build(titles, papers, votes, limit)
	items := make([]ArxivRecommendation, 0, len(results))
	for _, r := range results {
		items = append(items, ArxivRecommendation{
			ArxivId:   r.Paper.ID,
			Title:     r.Paper.Title,
			Authors:   r.Paper.Authors,
			Abstract:  r.Paper.Abstract,
			Published: r.Paper.Published,
			Url:       r.Paper.URL,
			PdfUrl:    r.Paper.PDFURL,
			GithubUrl: r.GithubURL,
			Reason:    r.Reason,
			Score:     r.Score,
			Vote:      ArxivRecommendationVote(votes[r.Paper.ID]),
		})
	}
	c.JSON(http.StatusOK, ArxivRecommendationList{Items: items})
}

// SubmitArxivFeedback 记录用户对一条推荐的赞/踩（M4）。
func (s *Server) SubmitArxivFeedback(c *gin.Context, id openapi_types.UUID) {
	var req ArxivFeedbackRequest
	if !s.bindJSON(c, &req) {
		return
	}
	if err := s.deps.ArxivVotes.Vote(c.Request.Context(), s.deps.Querier, id, req.ArxivId, string(req.Vote)); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// arxivFileName 把论文标题变成安全的入库文件名。
var arxivUnsafeNamePattern = regexp.MustCompile(`[^\p{L}\p{Nd}._-]+`)

// IngestArxivPaper 下载 arXiv PDF 并交给既有上传/解析流水线（M4 的"下钻入库"）。
func (s *Server) IngestArxivPaper(c *gin.Context, id openapi_types.UUID) {
	var req ArxivIngestRequest
	if !s.bindJSON(c, &req) {
		return
	}
	data, err := s.deps.Arxiv.DownloadPDF(c.Request.Context(), req.PdfUrl)
	if err != nil {
		s.fail(c, err)
		return
	}
	name := ""
	if req.Title != nil {
		name = strings.TrimSpace(*req.Title)
	}
	if name == "" && req.ArxivId != nil {
		name = *req.ArxivId
	}
	if name == "" {
		name = "arxiv-paper"
	}
	name = strings.TrimSpace(arxivUnsafeNamePattern.ReplaceAllString(name, "_"))
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	doc, err := s.deps.Knowledge.Upload(c.Request.Context(), id, name, bytes.NewReader(data))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toAPIDocument(doc))
}
