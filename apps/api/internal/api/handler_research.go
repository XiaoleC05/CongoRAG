// 科研助手特色能力的端点：检索调试、翻译、CSV 可视化、写作 Copilot、
// 多篇对比、引用综述。
//
// 【为什么这些算一个域】它们都以"知识库 + 模型"为输入、以"可展示的科研
// 产物"为输出（对照表、草稿、图表代码、综述），并且都不改变持久化状态。
// 其中检索类（搜索调试、对比、写作、综述）要先取证据再交给模型；翻译与
// CSV 可视化是纯转换，不检索。把它们放在一起是因为"改一处 prompt 或结果
// 结构"时它们共享同一套上下文，而不是因为它们步骤相同。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/prompt"
	"github.com/XiaoleC05/CongoRAG/internal/visualize"
)

// TranslateText 翻译一段论文文本（科研助手 M3 阅读器翻译）。
func (s *Server) TranslateText(c *gin.Context) {
	var req TranslateRequest
	if !s.bindJSON(c, &req) {
		return
	}
	target := ""
	if req.TargetLang != nil {
		target = *req.TargetLang
	}
	out, err := s.deps.LLM.Translate(c.Request.Context(), req.Text, target)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, TranslateResponse{Translation: out})
}

// VisualizeCSV 把 CSV 转成图表描述、Mermaid/TikZ 代码与 Figure Caption（M6）。
func (s *Server) VisualizeCSV(c *gin.Context) {
	var req VisualizeRequest
	if !s.bindJSON(c, &req) {
		return
	}
	chartType := "auto"
	if req.ChartType != nil {
		chartType = string(*req.ChartType)
	}
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	res, err := visualize.Build(req.Csv, chartType, title)
	if err != nil {
		s.fail(c, err)
		return
	}
	// 图注先给确定性兜底，再用模型润色；模型不可用不影响图表/代码产出。
	if polished, cerr := s.figureCaption(c.Request.Context(), res.Chart, res.Caption); cerr == nil && strings.TrimSpace(polished) != "" {
		res.Caption = strings.TrimSpace(polished)
	}
	series := make([]VisualizeSeries, 0, len(res.Chart.Series))
	for _, ss := range res.Chart.Series {
		series = append(series, VisualizeSeries{Name: ss.Name, Values: ss.Values})
	}
	c.JSON(http.StatusOK, VisualizeResult{
		Table: VisualizeTable{Headers: res.Table.Headers, Rows: res.Table.Rows},
		Chart: VisualizeChart{
			Type:       VisualizeChartType(res.Chart.Type),
			Title:      res.Chart.Title,
			YLabel:     res.Chart.YLabel,
			Categories: res.Chart.Categories,
			Series:     series,
		},
		Mermaid:    res.Mermaid,
		Tikz:       res.TikZ,
		Matplotlib: res.Matplotlib,
		Caption:    res.Caption,
	})
}

// figureCaption 让模型把"图表数据摘要"润色成一句学术图注。
func (s *Server) figureCaption(ctx context.Context, chart visualize.Chart, fallback string) (string, error) {
	data, err := json.Marshal(chart)
	if err != nil {
		return "", err
	}
	system := prompt.MustRender("caption.system", nil)
	user := "图表数据（JSON）：\n" + string(data) + "\n\n确定性草稿（可改写但不要偏离数据）：\n" + fallback
	return s.deps.LLM.Generate(ctx, system, user)
}

// GetCitationSurvey 基于引用图谱生成领域综述与 Future Work（M5 加分项）。
func (s *Server) GetCitationSurvey(c *gin.Context, id openapi_types.UUID) {
	var req CitationSurveyRequest
	if !s.bindJSON(c, &req) {
		return
	}
	ctx := c.Request.Context()
	graph, err := s.deps.Knowledge.CitationGraph(ctx, id)
	if err != nil {
		s.fail(c, err)
		return
	}
	if len(graph.Nodes) == 0 {
		s.fail(c, fmt.Errorf("知识库里没有 ready 文档，无法生成综述: %w", platform.ErrInvalid))
		return
	}
	focus := ""
	if req.Focus != nil {
		focus = strings.TrimSpace(*req.Focus)
	}

	filename := make(map[uuid.UUID]string, len(graph.Nodes))
	var docs strings.Builder
	for i, node := range graph.Nodes {
		filename[node.DocumentID] = node.Filename
		if i >= 10 {
			continue
		}
		chunks, cerr := s.deps.Knowledge.ChunksPreview(ctx, node.DocumentID, 2)
		if cerr != nil {
			continue
		}
		label := node.Filename
		if strings.TrimSpace(node.Title) != "" {
			label = node.Title + "（" + node.Filename + "）"
		}
		if year := arxivYearFromFilename(node.Filename); year != "" {
			label += "（" + year + " 年）"
		}
		docs.WriteString("### " + label + "\n")
		docs.WriteString(excerptChunks(chunks, 700))
		docs.WriteString("\n\n")
	}
	var rels strings.Builder
	for _, e := range graph.Edges {
		rels.WriteString("- " + filename[e.From] + " 引用了 " + filename[e.To] + "（" + strconv.Itoa(e.Weight) + " 条参考文献）\n")
	}
	system := prompt.MustRender("survey.system", nil)
	user := prompt.MustRender("survey.user", map[string]string{
		"focus":     focus,
		"docs":      docs.String(),
		"relations": rels.String(),
	})
	markdown, err := s.deps.LLM.Generate(ctx, system, user)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, CitationSurvey{Markdown: markdown})
}

// arxivYearFromFilename 从 arXiv ID 文件名（YYMM.NNNNN.pdf）推断发表年份，
// 供引用图谱综述的时间线提示使用；不是 arXiv 文件就返回空。
func arxivYearFromFilename(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.SplitN(base, ".", 2)
	if len(parts) != 2 || len(parts[0]) != 4 {
		return ""
	}
	yy, err := strconv.Atoi(parts[0][:2])
	if err != nil {
		return ""
	}
	if yy < 50 {
		return strconv.Itoa(2000 + yy)
	}
	return strconv.Itoa(1900 + yy)
}

// SearchKnowledgeBase 是检索调试视图（Hit Testing，issue #77）的后端。
//
// 【为什么按知识库限定范围】与 ReindexKnowledgeBase 同一条既有语义：
// 路径里的 id 就是范围。这里先查一次知识库，一是让不存在返回 404 而不是
// 一个空结果，二是避免对着一堆别的库的向量做一次没有意义的检索。
func (s *Server) SearchKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	var req KnowledgeSearchRequest
	if !s.bindJSON(c, &req) {
		return
	}

	if _, err := s.deps.Knowledge.Get(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}

	topK := knowledgeSearchDefaultTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	// 【越界返回 400，不静默夹取】夹取会让调试视图显示"只命中这么多"，
	// 而实际是被服务端截断了——在这个视图上那个区别是致命的。
	// 契约里的 minimum/maximum 只是给客户端看的声明，强制点在这里。
	if topK < 1 || topK > knowledgeSearchMaxTopK {
		s.fail(c, fmt.Errorf("topK must be between 1 and %d, got %d: %w",
			knowledgeSearchMaxTopK, topK, platform.ErrInvalid))
		return
	}

	chunks, err := s.deps.Retrieval.Search(c.Request.Context(), domain.SearchRequest{
		KnowledgeBaseID: id, Text: req.Query, TopK: topK,
	})
	if err != nil {
		s.fail(c, err)
		return
	}

	c.JSON(http.StatusOK, KnowledgeSearchResult{Hits: toAPIKnowledgeHits(chunks)})
}

// GenerateWritingDraft 基于知识库检索结果生成写作草稿（科研助手 M6）。
//
// 【为什么是独立端点而不是复用聊天】输出形态不同：聊天是流式一问一答，
// 写作要的是"一份可以继续编辑的 Markdown 草稿 + 引用来源"；前者用户
// 通常不保存，后者会被粘进论文里，所以必须把 citations 一起返回，
// 让前端把每条依据的页码/原文都摆出来。
func (s *Server) GenerateWritingDraft(c *gin.Context, id openapi_types.UUID) {
	var req WritingRequest
	if !s.bindJSON(c, &req) {
		return
	}
	if _, err := s.deps.Knowledge.Get(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}

	topK := writingDefaultTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	if topK < writingMinTopK || topK > writingMaxTopK {
		s.fail(c, fmt.Errorf("topK must be between %d and %d, got %d: %w",
			writingMinTopK, writingMaxTopK, topK, platform.ErrInvalid))
		return
	}

	chunks, err := s.deps.Retrieval.Search(c.Request.Context(), domain.SearchRequest{
		KnowledgeBaseID: id, Text: req.Topic, TopK: topK,
	})
	if err != nil {
		s.fail(c, err)
		return
	}

	// 没有证据时不去调模型：返回固定说明 + 空引用，和聊天拒答同一个原则。
	if len(chunks) == 0 {
		c.JSON(http.StatusOK, WritingResponse{
			Draft:     "没有在当前知识库里检索到与主题相关的内容，因此没有生成草稿。可以换一个主题，或先上传相关论文。",
			Citations: []KnowledgeSearchHit{},
		})
		return
	}

	system, user := buildWritingPrompt(string(req.Kind), req.Topic, chunks)
	draft, err := s.deps.LLM.Generate(c.Request.Context(), system, user)
	if err != nil {
		s.fail(c, err)
		return
	}
	graphviz := writingGraphviz(string(req.Kind))
	c.JSON(http.StatusOK, WritingResponse{
		Draft:     draft,
		Citations: toAPIKnowledgeHits(chunks),
		Graphviz:  &graphviz,
	})
}

// ComparePapers 生成多篇论文的对比分析（科研助手 A6）。
//
// 【和写作为什么分成两个端点】对比要求"按维度组织成表格 + 每篇论文的
// 证据并排"，提示词与输出校验都不同；合成一个端点会让 kind 枚举变成
// 一个隐式的模式开关，调用方和文档都会难讲清楚。
func (s *Server) ComparePapers(c *gin.Context, id openapi_types.UUID) {
	var req CompareRequest
	if !s.bindJSON(c, &req) {
		return
	}
	if _, err := s.deps.Knowledge.Get(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}

	topK := compareDefaultTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	if topK < compareMinTopK || topK > compareMaxTopK {
		s.fail(c, fmt.Errorf("topK must be between %d and %d, got %d: %w",
			compareMinTopK, compareMaxTopK, topK, platform.ErrInvalid))
		return
	}

	chunks, err := s.deps.Retrieval.Search(c.Request.Context(), domain.SearchRequest{
		KnowledgeBaseID: id, Text: req.Question, TopK: topK,
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	if len(chunks) == 0 {
		c.JSON(http.StatusOK, CompareResponse{
			Answer:    "没有在当前知识库里检索到与问题相关的内容，无法进行对比。可以换一个问法，或先上传要对比的论文。",
			Citations: []KnowledgeSearchHit{},
		})
		return
	}

	system, user := buildComparePrompt(req.Question, chunks)
	answer, err := s.deps.LLM.Generate(c.Request.Context(), system, user)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, CompareResponse{Answer: answer, Citations: toAPIKnowledgeHits(chunks)})
}

// buildComparePrompt 拼"多篇对比"的提示词。
//
// 【编号与 citations 顺序一致】citations 就是 chunks 顺序；提示词里的
// [n] 由模型按资料编号生成，前端据此把 [n] 渲染成可点来源。
func buildComparePrompt(question string, chunks []domain.Chunk) (system, user string) {
	// 证据块仍由这里按 citations 顺序拼：[n] 的编号必须与返回给前端的
	// citations 数组一致，否则点 [2] 会跳到另一篇论文。提示词文本本身
	// 在 internal/prompt 注册表（compare.system / compare.user）。
	var evidence strings.Builder
	for i, ch := range chunks {
		fmt.Fprintf(&evidence, "\n[%d] ", i+1)
		if ch.PageStart > 0 {
			fmt.Fprintf(&evidence, "（%s 第 %d 页）", ch.Filename, ch.PageStart)
		}
		evidence.WriteString(ch.Content)
	}
	system = prompt.MustRender("compare.system", nil)
	user = prompt.MustRender("compare.user", map[string]string{
		"question": question,
		"evidence": evidence.String(),
	})
	return system, user
}

// 多篇对比的 topK 边界（与契约里的 minimum/maximum 一致）。
const (
	compareDefaultTopK = 8
	compareMinTopK     = 2
	compareMaxTopK     = 12
)

// buildWritingPrompt 把 kind/topic/检索片段拼成一次写作请求。
//
// 【片段编号必须和 citations 顺序一致】返回的 citations 就是 chunks 的
// 顺序；草稿里的 [n] 由模型按提示词里的 [n] 生成，前端据此把 [n] 变成
// 可点击的来源卡片。
func buildWritingPrompt(kind, topic string, chunks []domain.Chunk) (system, user string) {
	// 体例任务文本也是提示词资产（writing.task.*）；未知体例退回「相关工作」，
	// 与 v6.2.0 的 default 分支行为一致。
	task, err := prompt.WritingTaskText(kind)
	if err != nil {
		task = prompt.MustRender("writing.task.related_work", nil)
	}

	var evidence strings.Builder
	for i, ch := range chunks {
		fmt.Fprintf(&evidence, "\n[%d] %s", i+1, ch.Content)
		if ch.PageStart > 0 {
			fmt.Fprintf(&evidence, "（%s 第 %d 页）", ch.Filename, ch.PageStart)
		}
	}
	system = prompt.MustRender("writing.system", nil)
	user = prompt.MustRender("writing.user", map[string]string{
		"topic":    topic,
		"task":     task,
		"evidence": evidence.String(),
	})
	return system, user
}

// writingGraphviz 生成论文框架的 Graphviz DOT 脚本（M6 的拓扑图输出之一）。
// 不依赖模型：结构来自体例本身，保证"没有模型也有可渲染的图"。
func writingGraphviz(kind string) string {
	sections := []string{"Abstract", "Introduction", "Related Work", "Method", "Experiments", "Conclusion", "References"}
	if kind == "related_work" {
		sections = []string{"Topic", "Related Work", "Gap", "References"}
	} else if kind == "summary" {
		sections = []string{"Topic", "Key Methods", "Results", "Takeaway"}
	} else if kind == "outline" {
		sections = []string{"Title", "Section 1", "Section 2", "Section 3", "References"}
	}
	var b strings.Builder
	b.WriteString("digraph paper_framework {\n  rankdir=LR;\n  node [shape=box, style=rounded, fontname=\"Microsoft YaHei\"];\n")
	for i, sec := range sections {
		fmt.Fprintf(&b, "  s%d [label=%q];\n", i, sec)
		if i > 0 {
			fmt.Fprintf(&b, "  s%d -> s%d;\n", i-1, i)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// 写作 Copilot 的 topK 边界（与契约里的 minimum/maximum 一致）。
const (
	writingDefaultTopK = 6
	writingMinTopK     = 1
	writingMaxTopK     = 10
)

// 检索调试的 topK 边界。契约里的 min/max 与它们必须一致——
// 那一处是给客户端看的声明，这一处才是强制点。
const (
	knowledgeSearchDefaultTopK = 5
	knowledgeSearchMaxTopK     = 50
)
