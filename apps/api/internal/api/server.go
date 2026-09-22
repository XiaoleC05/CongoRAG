// Package api 是 HTTP 边界。
//
// 这是唯一 import gin 的业务侧包；internal/ 下那几个包不写 handler、
// 不 import gin，也不知道 HTTP 状态码。
//
// 路由不用手写：generated.go 里的 RegisterHandlers 按 contracts/openapi.yaml
// 把每个 URL 挂到 ServerInterface 对应的方法上。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/agent"
	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/retrieval"
)

// Deps 是 Server 需要的全部依赖，由装配根填入。
// 后续里程碑每加一个模块就往这里加一个字段。
type Deps struct {
	Logger       *slog.Logger
	Knowledge    *knowledge.Usecase
	LLM          *llm.Usecase
	Conversation *conversation.Usecase
	Agent        *agent.Usecase

	// Retrieval 是检索调试视图（POST /knowledge-bases/{id}/search）用的。
	// 它和 knowledge 是不同的关注点：knowledge 管知识库与文档的 CRUD，
	// 检索是它下面那一层（retrieval 才是向量的读写方）。
	Retrieval *retrieval.Usecase

	// MaxUploadBytes 是上传接口允许的最大请求体字节数（platform.Config
	// 的对应项）。只有 UploadDocument 用它，<=0 表示不限——生产装配里
	// 永远是一个正数。
	MaxUploadBytes int64

	// DB 是就绪探针要探测的那个依赖，只有 Readyz 用它。
	//
	// 类型是 platform.Pinger 而不是 *pgxpool.Pool：这个包至今没有 import
	// 过任何数据库驱动，Deps 是它唯一能拿到池的口子，这里一写具体类型，
	// 驱动就顺着 Deps 进了 HTTP 边界。
	DB platform.Pinger
}

// 编译期断言：契约里加了端点而这里没实现，编译失败。
var _ ServerInterface = (*Server)(nil)

type Server struct {
	deps Deps
}

func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// toAPIKB 把业务类型转成契约类型。
//
// 两个类型分开是因为服务对象不同：knowledge.KB 随业务变化，
// api.KnowledgeBase 随接口变化。在这里显式转一次，
// "哪些字段暴露给前端"就有了唯一一处可看的地方。
// 直接复用的话，业务里加个内部字段会自动漏到 API 上。
func toAPIKB(kb *knowledge.KB) KnowledgeBase {
	return KnowledgeBase{
		Id:        kb.ID,
		Name:      kb.Name,
		CreatedAt: kb.CreatedAt,
		UpdatedAt: kb.UpdatedAt,
	}
}

// toAPIKBList 转一批。
//
// 用 make(..., 0, n) 而不是 var out []KnowledgeBase：
// nil 切片序列化成 JSON 是 null，空切片是 []。
// 前端拿到 null 去做 .map() 会直接崩。
func toAPIKBList(kbs []*knowledge.KB) []KnowledgeBase {
	out := make([]KnowledgeBase, 0, len(kbs))
	for _, kb := range kbs {
		out = append(out, toAPIKB(kb))
	}
	return out
}

// ────────────────────────────────────────────────────────────────
// ServerInterface 的六个方法
// 调用链一致：解析入参 → 调 usecase → 出错交给 fail → 写响应
// ────────────────────────────────────────────────────────────────

// Healthz 只报告进程存活，不检查数据库。
//
// 数据库连不上时服务本身是活的；是否该给它发流量由 readiness 探针判断。
// 两者混在一起会导致数据库抖动时实例被反复重启。
func (s *Server) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// readinessProbeTimeout 是 /readyz 探测数据库的上限。
//
// 【必须有这个超时】pgxpool.Ping 内部是 Acquire（可能要新建连接）+ 一次
// 往返，两者都只认 ctx；连接串里没写 connect_timeout 时 pgx 不设
// ConnectTimeout，一个被防火墙丢包的数据库会让 Ping 一直挂着。探针要回答
// 的是"现在能不能发流量"，不是"等到有答案"。
const readinessProbeTimeout = 2 * time.Second

// Readyz 是就绪探针：进程活着之外，还要求数据库能用。
//
// 【探测什么、不探测什么】只探这个进程服务每一个请求都离不开的东西，
// 也就是连接池。不探：
//   - schema_migrations 的版本：它不在请求路径上（运行时没有代码读它），
//     一次没跑完或 dirty 的迁移是运维要处理的事，不是"这个实例不能干活"。
//     接进来等于把一个部署期状态变成永远好不了的 503。
//   - River 的队列表：api 侧只是 insert-only，缺表只影响上传这一条路径，
//     而 /readyz 是一刀切的门禁——判它不 ready 会把浏览、聊天一起下线。
//   - 任何出网的调用（模型/embedding 服务）：它们是每请求的，而且已经以
//     upstream_llm_error(502) 的形式失败；用探针每几秒去敲一次第三方，
//     等于把对方的抖动变成"你本地应用挂了"。
//
// 【失败为什么不走 s.fail】fail 的 5xx 分支会把 detail 换成"服务内部错误"
// ——那恰恰是就绪探针最需要说清楚的信息（是数据库，不是别的），换掉之后
// 运维只能去翻日志；而且它按 Error 级别记日志，一个几秒一次的探针会把日志
// 淹掉。这里直接 writeProblem，和 spa.go 的 NoRoute 404 是同一类
// "不是请求失败，而是状态报告"。
// parseListParams 把三个分页端点共用的 limit / cursor 解出来（issue #45）。
//
// 【为什么在 handler 里校验】"参数越界返回 400"是 HTTP 边界的语义。
// usecase 里也会再校验一遍（它可能被别的调用方直接调），那是纵深不是重复。
//
// 返回的 bool 为 false 表示已经写过响应了，调用方直接 return。
func (s *Server) parseListParams(c *gin.Context, limit *Limit, cursor *Cursor) (int, string, bool) {
	n := 0
	if limit != nil {
		n = int(*limit)
	}
	n, err := platform.ClampListLimit(n)
	if err != nil {
		s.fail(c, err)
		return 0, "", false
	}

	cur := ""
	if cursor != nil {
		cur = strings.TrimSpace(string(*cursor))
	}
	return n, cur, true
}

// nullableCursor 把内部用的空串转成契约里的 null / 缺省。
//
// 【空串在内部表示"没有更多"】契约里这个字段是 nullable 的字符串，前端拿
// 到 null 就该把「加载更多」收起来。用空串表达的话前端要多判一种情况，
// 而且空串是一个合法的字符串值，语义上区分不干净。
func nullableCursor(next string) *string {
	if next == "" {
		return nil
	}
	return &next
}

func (s *Server) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessProbeTimeout)
	defer cancel()

	if err := s.deps.DB.Ping(ctx); err != nil {
		s.deps.Logger.Warn("readiness probe failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"error", err,
		)
		typ, title, detail := "unavailable", "服务暂不可用", "数据库连接不可用"
		writeProblem(c, http.StatusServiceUnavailable, Problem{
			Type: typ, Status: http.StatusServiceUnavailable, Title: &title, Detail: &detail,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

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
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
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
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
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

// ────────────────────────────────────────────────────────────────
// 文档
// ────────────────────────────────────────────────────────────────

// toAPIDocument 和 toAPIKB 一样的理由：业务类型和契约类型分开，
// "哪些字段暴露给前端"只有这一处可看。
func toAPIDocument(d *knowledge.Document) Document {
	return Document{
		Id:              d.ID,
		KnowledgeBaseId: d.KnowledgeBaseID,
		Filename:        d.Filename,
		Status:          DocumentStatus(d.Status),
		ByteSize:        d.ByteSize,
		ChunkCount:      d.ChunkCount,
		CreatedAt:       d.CreatedAt,
		UpdatedAt:       d.UpdatedAt,
	}
}

func toAPIDocumentList(docs []*knowledge.Document) []Document {
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		out = append(out, toAPIDocument(d))
	}
	return out
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

func (s *Server) DeleteDocument(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.Knowledge.DeleteDocument(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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

// ────────────────────────────────────────────────────────────────
// BYOK：ListProviders / CreateProvider
// ────────────────────────────────────────────────────────────────

// fromAPICapabilities 把契约里的 Capabilities（字段全是 *bool,因为
// 整个对象和每个字段在契约里都是可选的）转成 llm.Capabilities。
//
// 【default 只是文档,不是运行时行为】oapi-codegen 不会替我们把
// contracts/openapi.yaml 里写的 default: true/false 应用到零值——
// 那只是给读契约的人看的说明。这个函数自己把契约声明的默认值补上，
// 相当于把"文档里的承诺"变成"代码里真的发生的事"。
func fromAPICapabilities(c *Capabilities) llm.Capabilities {
	// chat 与 toolCalling 的契约默认值都是 true，其余是 false。
	//
	// 【toolCalling 是 true 这件事必须有理由】这一位现在真的参与判断
	// （internal/agent 的门控会读它），默认 false 等于默认禁掉所有带工具的
	// Agent。默认 true 同时对应迁移 0007 对已有行的回填——两边是同一件事
	// 的两半，改一处必须改另一处。
	out := llm.Capabilities{Chat: true, ToolCalling: true}
	if c == nil {
		return out
	}
	if c.Chat != nil {
		out.Chat = *c.Chat
	}
	if c.Streaming != nil {
		out.Streaming = *c.Streaming
	}
	if c.ToolCalling != nil {
		out.ToolCalling = *c.ToolCalling
	}
	if c.Reasoning != nil {
		out.Reasoning = *c.Reasoning
	}
	return out
}

func toAPICapabilities(c llm.Capabilities) Capabilities {
	return Capabilities{
		Chat:        &c.Chat,
		Streaming:   &c.Streaming,
		ToolCalling: &c.ToolCalling,
		Reasoning:   &c.Reasoning,
	}
}

func toAPIModelSummary(m *llm.Model) ModelSummary {
	kind := ModelSummaryKind(m.Kind)
	caps := toAPICapabilities(m.Capabilities)
	return ModelSummary{
		Id:              m.ID,
		ModelId:         m.ModelID,
		Kind:            kind,
		Capabilities:    caps,
		ContextWindow:   m.ContextWindow,
		MaxOutputTokens: m.MaxOutputTokens,
		TokenizerType:   m.TokenizerType,
		EmbeddingDim:    m.EmbeddingDim,
		CreatedAt:       m.CreatedAt,
	}
}

// groupModelsByProvider 是 ListProviders 用的——两次 List 调用各自查
// 各自的表（llm.Usecase 没有一个"带模型的 provider"的查询，那会把两张
// 表的关系硬编码进 SQL 层),在 handler 这一层拼起来。
func groupModelsByProvider(models []*llm.Model) map[openapi_types.UUID][]ModelSummary {
	out := make(map[openapi_types.UUID][]ModelSummary)
	for _, m := range models {
		out[m.ProviderID] = append(out[m.ProviderID], toAPIModelSummary(m))
	}
	return out
}

func (s *Server) ListProviders(c *gin.Context) {
	ctx := c.Request.Context()

	providers, err := s.deps.LLM.ListProviders(ctx)
	if err != nil {
		s.fail(c, err)
		return
	}
	models, err := s.deps.LLM.ListModels(ctx)
	if err != nil {
		s.fail(c, err)
		return
	}
	modelsByProvider := groupModelsByProvider(models)

	out := make([]ProviderWithModels, 0, len(providers))
	for _, p := range providers {
		out = append(out, ProviderWithModels{
			Id:        p.ID,
			BaseUrl:   p.BaseURL,
			CreatedAt: p.CreatedAt,
			// 【make(..., 0, ...) 不是 nil】即使这个 provider 还没有模型,
			// JSON 序列化要给 [] 不是 null——原因同 toAPIKBList 的注释。
			Models: append(make([]ModelSummary, 0, len(modelsByProvider[p.ID])), modelsByProvider[p.ID]...),
		})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) CreateProvider(c *gin.Context) {
	var req CreateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return
	}

	result, err := s.deps.LLM.Bootstrap(c.Request.Context(), llm.BootstrapRequest{
		BaseURL: req.BaseUrl,
		APIKey:  req.ApiKey,
		ChatModel: llm.ChatModelInput{
			ModelID:         req.ChatModel.ModelId,
			Capabilities:    fromAPICapabilities(req.ChatModel.Capabilities),
			ContextWindow:   req.ChatModel.ContextWindow,
			MaxOutputTokens: req.ChatModel.MaxOutputTokens,
			TokenizerType:   req.ChatModel.TokenizerType,
		},
		EmbeddingModelID: req.EmbeddingModelId,
		// AllowEmbeddingReset 是用户已经确认"会清空已有向量并重建"的标记。
		// nil 表示没传，按契约默认的 false 处理——那时服务端会返回 409
		// embedding_change_requires_reindex 让前端去弹确认框。
		AllowEmbeddingReset: req.AllowEmbeddingReset != nil && *req.AllowEmbeddingReset,
	})
	if err != nil {
		s.fail(c, err)
		return
	}

	resp := ProviderWithModels{
		Id:        result.Provider.ID,
		BaseUrl:   result.Provider.BaseURL,
		CreatedAt: result.Provider.CreatedAt,
		Models: []ModelSummary{
			toAPIModelSummary(result.ChatModel),
			toAPIModelSummary(result.EmbeddingModel),
		},
	}
	// 【只有真的重建过才带这个字段】同模型同维度地重存一次也会传
	// allowEmbeddingReset=true（前端判不出来这次会不会清），但那种情况一条
	// 向量都没丢，服务端返回 0——它不是"用户传了 true"的回声。
	if result.RequeuedDocuments > 0 {
		n := result.RequeuedDocuments
		resp.RequeuedDocuments = &n
	}

	c.JSON(http.StatusCreated, resp)
}

// ────────────────────────────────────────────────────────────────
// 会话
// ────────────────────────────────────────────────────────────────

func toAPIConversation(c *conversation.Conversation) Conversation {
	return Conversation{
		Id:              c.ID,
		Title:           c.Title,
		KnowledgeBaseId: c.KnowledgeBaseID,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
	}
}

func toAPIMessage(m *conversation.Message) Message {
	return Message{
		Id:             m.ID,
		ConversationId: m.ConversationID,
		Role:           MessageRole(m.Role),
		Content:        m.Content,
		Status:         MessageStatus(m.Status),
		SequenceNo:     m.SequenceNo,
		CreatedAt:      m.CreatedAt,
	}
}

func toAPIMessageList(msgs []*conversation.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toAPIMessage(m))
	}
	return out
}

func (s *Server) CreateConversation(c *gin.Context) {
	var req CreateConversationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 【为什么允许空 body】CreateConversationRequest 的字段都不是
		// required（契约里 title/knowledgeBaseId 都可选）——一个空的
		// JSON 对象 "{}" 应该能通过绑定。ShouldBindJSON 在 body 完全
		// 为空（Content-Length: 0）时才会报错，那种情况才拒绝。
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return
	}

	title := ""
	if req.Title != nil {
		title = *req.Title
	}

	conv, err := s.deps.Conversation.CreateConversation(c.Request.Context(), title, req.KnowledgeBaseId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAPIConversation(conv))
}

func (s *Server) ListConversations(c *gin.Context, params ListConversationsParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	convs, next, err := s.deps.Conversation.ListConversations(c.Request.Context(), cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	// 空切片不是 nil：契约里 items 是数组，nil 会序列化成 null，
	// 前端拿到 null 去 .map() 直接崩（和 toAPIKBList 那条同一个约定）。
	out := make([]Conversation, 0, len(convs))
	for _, cv := range convs {
		out = append(out, toAPIConversation(cv))
	}
	c.JSON(http.StatusOK, ConversationPage{Items: out, NextCursor: nullableCursor(next)})
}

func (s *Server) ListConversationMessages(c *gin.Context, id openapi_types.UUID, params ListConversationMessagesParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	msgs, next, err := s.deps.Conversation.ListMessages(c.Request.Context(), id, cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, MessagePage{
		Items:      toAPIMessageList(msgs),
		NextCursor: nullableCursor(next),
	})
}

// SendMessage 是全项目唯一一个"成功路径不调用 s.fail"的 handler——
// 一旦请求体解析通过，响应就切换成 SSE 模式（newSSESink 会立刻写
// 200 + 响应头），之后 conversation.Usecase.Send 内部的任何错误都通过
// error 事件传给客户端（docs/sse-protocol.md），不再改变 HTTP 状态码。
//
// 【为什么请求体解析失败还能用 s.fail】那一步发生在 newSSESink 之前，
// 响应还没有被提交成 200，这时候返回一个正常的 400 Problem 是安全的。
//
// 【幂等键为什么在这里读，而不是做成中间件】这个键的作用域要包含会话 id
// （endpoint 字符串是 "POST /api/v1/conversations/<id>/messages"），而中间件
// 拿不到路由参数之外的东西；更要紧的是真正的裁决发生在
// WithConversationLock 的事务里（见 conversation.Usecase 的注释），中间件
// 两头都够不着。handler 只负责把头读出来并做长度校验。
func (s *Server) SendMessage(c *gin.Context, id openapi_types.UUID, params SendMessageParams) {
	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return
	}

	idempotencyKey := ""
	if params.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(*params.IdempotencyKey)
	}
	// 【校验必须在 newSSESink 之前】sink 一构造出来，响应就切进 SSE 模式，
	// 那时再想返回 400 problem+json 已经来不及了。超长键属于"请求本身不合法"，
	// 用正常的 400 拒绝比在流里推一条 error 帧更准确。
	// 【按字符数算，不能按字节】契约写的是 maxLength: 255，那是字符数。
	// len(string) 是字节数——一个纯中文的键会在 86 个字符时就被拒掉，
	// 而它完全合法。仓库里其余长度校验（cleanTitle / maxNameLen 之类）
	// 也都是按 rune 数算的。
	if utf8.RuneCountInString(idempotencyKey) > conversation.MaxIdempotencyKeyLen {
		s.fail(c, fmt.Errorf("Idempotency-Key must not exceed %d characters: %w",
			conversation.MaxIdempotencyKeyLen, platform.ErrInvalid))
		return
	}

	sink := newSSESink(c)
	// Send 的返回值只用于日志和兜底——一旦切到 SSE 模式，错误已经通过
	// error 事件传给客户端了（conversation.Usecase.Send 自己保证这一点），
	// 这里不需要、也不能再对 c 做任何响应相关的操作。
	if err := s.deps.Conversation.Send(c.Request.Context(), id, req.Text, idempotencyKey, sink); err != nil {
		s.deps.Logger.Warn("send message failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"conversation_id", id, "error", err)
		s.writeFallbackError(sink, err)
	}
}

// writeFallbackError 是"失败必须对客户端可见"的最后一道保障。
//
// 【为什么必须有它】Send 的 error 事件是先持久化再发送的——emitEvent 要用
// NextEventID 分配 event_id，分配不到就提前返回。于是"数据库整个不可用"
// 这一类失败恰好会把那条 error 帧也堵在库里：客户端拿到的是 200 +
// text/event-stream + 零字节 body，和一个空的成功流完全同形——前端
// streamChat 正常 resolve、onError 从不触发，用户看到自己发的消息、
// 没有回答、也没有任何错误提示。同一时刻所有非 SSE 端点都在返回 500
// Problem，只有聊天这条路径是静默的。
//
// 【为什么按"还没发过 error 帧"判断，而不是"一个帧都没发过"】数据库在
// 流到一半时才坏掉的情况，客户端已经收到若干 token 却再也等不到
// error/done，连接就那么挂着——补一帧同样是对的。
func (s *Server) writeFallbackError(sink *sseSink, cause error) {
	if sink.errorFrameSent {
		return
	}

	status, typ, _ := classify(cause)
	detail := innermostMessage(cause)
	if status >= http.StatusInternalServerError {
		// 和 fail 对 5xx 的处理一致：不把内部错误原文（可能带 SQL 片段、
		// 表名、连接串）漏给客户端。
		detail = "服务内部错误"
	}

	// 这一帧不查库也不写库，是唯一能在数据库不可用时送达的通道——
	// 所以写失败除了让连接自己坏掉，没有别的补救。
	_ = sink.writeFallbackError(sseErrorData{Type: typ, Detail: detail})
}

func (s *Server) SubscribeConversationEvents(c *gin.Context, id openapi_types.UUID, params SubscribeConversationEventsParams) {
	var after int64
	if params.AfterEventId != nil {
		after = *params.AfterEventId
	}

	events, err := s.deps.Conversation.EventsAfter(c.Request.Context(), id, after)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	for _, ev := range events {
		if err := sink.Emit(conversation.Event{ID: ev.ID, Type: ev.Type, Payload: ev.Payload}); err != nil {
			// 客户端在补发历史事件的过程中断开了——没有更多可做的，
			// 停止继续写,让 handler 正常返回。
			return
		}
	}
	_ = sink.Flush()

	// 【补发完历史事件后直接结束这次响应,不保持连接等待新事件】
	// 真正的"持续推送新事件"只发生在 SendMessage 那条连接里——
	// 这个端点是断线续传的两段式设计里的"补发"那一段：客户端收完
	// 历史事件后,如果原来的生成还没结束,需要的是重新发起一次
	// SendMessage（幂等键会命中，见开发文档 M4-B）或者简单地再连一次
	// 这个端点看有没有新事件。M2 阶段生成的整个过程都在一次 SendMessage
	// 请求的生命周期内完成（没有后台任务在生成完之后继续产生事件），
	// 所以这个端点长期持有连接、等待新事件并没有实际意义——这一点
	// 到 M4-A Agent 引入之后（生成可能跨越更长时间、更需要断线续传）
	// 需要重新评估要不要在这里加"继续等待"的逻辑。
}

// ────────────────────────────────────────────────────────────────
// Token 用量（issue #47）
// ────────────────────────────────────────────────────────────────

// GetUsageSummary 按模型聚合 token 用量。
//
// 【没有分页】聚合结果的条数 = 配过的模型数（个位数量级），不是调用次数。
//
// 【合计在 handler 里算】SQL 已经按模型分好组了，再为两个总数跑一次查询
// 不划算；这里的行数是个位数，累加一次比多一次往返便宜。
func (s *Server) GetUsageSummary(c *gin.Context, params GetUsageSummaryParams) {
	rows, err := s.deps.LLM.UsageSummary(c.Request.Context(), params.Since, params.Until)
	if err != nil {
		s.fail(c, err)
		return
	}

	// 【必须是空切片不是 nil】契约里 byModel 是数组，nil 会序列化成 null，
	// 前端拿到 null 去做 .map() 直接崩（和 toAPIKBList 那条同一个约定）。
	out := UsageSummary{ByModel: make([]UsageByModel, 0, len(rows))}
	for _, r := range rows {
		out.TotalPromptTokens += r.PromptTokens
		out.TotalCompletionTokens += r.CompletionTokens
		out.ByModel = append(out.ByModel, UsageByModel{
			ProviderId:       r.ProviderID,
			ModelId:          r.ModelID,
			ModelName:        r.ModelName,
			Kind:             UsageByModelKind(r.Kind),
			Calls:            r.Calls,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ────────────────────────────────────────────────────────────────
// Agent（M4-A）
// ────────────────────────────────────────────────────────────────

func toAPIAgent(a *agent.Agent) Agent {
	return Agent{
		Id: a.ID, Name: a.Name, Description: a.Description, Instruction: a.Instruction,
		ToolNames: a.ToolNames, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func toAPIAgentList(agents []*agent.Agent) []Agent {
	out := make([]Agent, len(agents))
	for i, a := range agents {
		out[i] = toAPIAgent(a)
	}
	return out
}

func toAPIAgentRun(r *agent.Run) AgentRun {
	return AgentRun{
		Id: r.ID, AgentId: r.AgentID, Status: AgentRunStatus(r.Status), CurrentStep: r.CurrentStep,
		Input: r.Input, Output: r.Output, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toAPIAgentRunList(runs []*agent.Run) []AgentRun {
	out := make([]AgentRun, len(runs))
	for i, r := range runs {
		out[i] = toAPIAgentRun(r)
	}
	return out
}

// toAPIRawJSON 把可能为空的 json.RawMessage 转成契约里 interface{}
// 字段期望的形状——nil/空切片必须转成 Go 的 nil,不能是 json.RawMessage(nil)
// 这个具体类型的零值,否则 encoding/json 序列化时会把它当成非 nil 的
// "长度为 0 的原始字节"处理,输出 `""` 而不是契约期望的字段整体消失
// （omitempty 只认 Go 层面的 nil interface{}，不认"底层是空切片的具名类型"）。
func toAPIRawJSON(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func toAPIAgentRunStep(s *agent.Step) AgentRunStep {
	step := AgentRunStep{
		Id: s.ID, RunId: s.RunID, Seq: s.Seq, Type: AgentRunStepType(s.Type),
		Status: AgentRunStepStatus(s.Status), ToolArgs: toAPIRawJSON(s.ToolArgs),
		ToolResult: toAPIRawJSON(s.ToolResult), CreatedAt: s.CreatedAt,
	}
	if s.ToolName != "" {
		step.ToolName = &s.ToolName
	}
	if s.LatencyMS != 0 {
		step.LatencyMs = &s.LatencyMS
	}
	if s.Error != "" {
		step.Error = &s.Error
	}
	return step
}

func toAPIAgentRunStepList(steps []*agent.Step) []AgentRunStep {
	out := make([]AgentRunStep, len(steps))
	for i, s := range steps {
		out[i] = toAPIAgentRunStep(s)
	}
	return out
}

func toAPIToolCatalog(entries []agent.ToolCatalogEntry) []ToolCatalogEntry {
	out := make([]ToolCatalogEntry, len(entries))
	for i, e := range entries {
		out[i] = ToolCatalogEntry{
			Name: e.Name, Description: e.Description,
			SideEffectLevel: ToolCatalogEntrySideEffectLevel(e.SideEffectLevel),
		}
	}
	return out
}

func (s *Server) ListToolCatalog(c *gin.Context) {
	entries, err := s.deps.Agent.ListToolCatalog(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIToolCatalog(entries))
}

func (s *Server) ListAgents(c *gin.Context) {
	agents, err := s.deps.Agent.ListAgents(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentList(agents))
}

func (s *Server) CreateAgent(c *gin.Context) {
	var req CreateAgentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return
	}

	description, instruction := "", ""
	if req.Description != nil {
		description = *req.Description
	}
	if req.Instruction != nil {
		instruction = *req.Instruction
	}
	var toolNames []string
	if req.ToolNames != nil {
		toolNames = *req.ToolNames
	}

	a, err := s.deps.Agent.CreateAgent(c.Request.Context(), req.Name, description, instruction, toolNames)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAPIAgent(a))
}

func (s *Server) GetAgent(c *gin.Context, id openapi_types.UUID) {
	a, err := s.deps.Agent.GetAgent(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgent(a))
}

func (s *Server) ListAgentRuns(c *gin.Context, id openapi_types.UUID, params ListAgentRunsParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	runs, next, err := s.deps.Agent.ListRuns(c.Request.Context(), id, cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, AgentRunPage{
		Items:      toAPIAgentRunList(runs),
		NextCursor: nullableCursor(next),
	})
}

// StartAgentRun 和 SendMessage 同一个模式：请求体解析失败还能用
// s.fail（还没切到 SSE 模式),之后 agent.Usecase.Start 内部的任何
// 错误都通过 error 事件传给客户端,不再改变 HTTP 状态码。
func (s *Server) StartAgentRun(c *gin.Context, id openapi_types.UUID, params StartAgentRunParams) {
	var req StartAgentRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
		return
	}

	idempotencyKey := ""
	if params.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(*params.IdempotencyKey)
	}
	// 和 SendMessage 同一条判据：校验必须在 newSSESink 之前，超长键是
	// "请求本身不合法"，用 400 拒绝比在流里推一帧更准确；长度按字符数算
	// （契约写的是 maxLength: 255，那是字符数）。
	if utf8.RuneCountInString(idempotencyKey) > conversation.MaxIdempotencyKeyLen {
		s.fail(c, fmt.Errorf("Idempotency-Key must not exceed %d characters: %w",
			conversation.MaxIdempotencyKeyLen, platform.ErrInvalid))
		return
	}

	sink := newSSESink(c)
	if _, err := s.deps.Agent.Start(c.Request.Context(), id, req.Input, idempotencyKey, sink); err != nil {
		s.deps.Logger.Warn("agent run failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"agent_id", id, "error", err)
		// 【和 SendMessage 一样要有兜底帧】失败事件是先持久化再发的，
		// 所以"数据库整个不可用"会把那条 error 帧也堵在库里——客户端
		// 拿到的是 200 + 空 body，和一个空的成功流完全同形（那条注释
		// 在 writeFallbackError 上写得更细）。
		s.writeFallbackError(sink, err)
	}
}

func (s *Server) ListRunSteps(c *gin.Context, runId openapi_types.UUID) {
	steps, err := s.deps.Agent.ListSteps(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentRunStepList(steps))
}

// ────────────────────────────────────────────────────────────────
// run 维度：重订阅 / 取消 / 恢复（issue #54 / #55 / #64）
// ────────────────────────────────────────────────────────────────

// SubscribeRunEvents 和会话维度那条 SubscribeConversationEvents 逐条同形：
// 补发完已有的历史就结束响应，不持有连接等新事件。
func (s *Server) SubscribeRunEvents(c *gin.Context, runId openapi_types.UUID, params SubscribeRunEventsParams) {
	var after int64
	if params.AfterEventId != nil {
		after = *params.AfterEventId
	}

	// 【为什么这里先查一次 run】它让"这条 run 不存在"在切进 SSE 模式之前
	// 就以 404 Problem 返回，而不是给客户端一条 200 + 空流——后者与
	// "这条 run 没有任何事件"完全同形。
	events, err := s.deps.Agent.RunEvents(c.Request.Context(), runId, after)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	for _, ev := range events {
		if err := sink.Emit(conversation.Event{ID: ev.ID, Type: ev.Type, Payload: ev.Payload}); err != nil {
			// 客户端在补发过程中断开了——没有更多可做的，停止继续写。
			return
		}
	}
	_ = sink.Flush()
}

// CancelAgentRun 取消一次在途运行（issue #55）。
//
// 【它是普通 JSON 端点，不是 SSE】取消的结果是"这条 run 现在是什么状态"，
// 一次性就答完了；响应里带的是**取消生效之后**的记录（Usecase.Cancel 会
// 等收尾写入落库），所以前端不需要再轮询一次。
func (s *Server) CancelAgentRun(c *gin.Context, runId openapi_types.UUID) {
	run, err := s.deps.Agent.Cancel(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentRun(run))
}

// ResumeAgentRun 从断点恢复一次被中断的运行（issue #64）。
//
// 【为什么先 PrepareResume 再开流】恢复会被拒绝的理由有三种，而它们都该
// 是正常的 409 Problem（前端按 type 给出不同的下一步提示）。一旦打开 SSE，
// 状态码就锁死在 200 上，那时只能推一帧笼统的 error——这正是 issue #34
// 修过的那类误报。所以校验和推流分成两步。
func (s *Server) ResumeAgentRun(c *gin.Context, runId openapi_types.UUID) {
	plan, err := s.deps.Agent.PrepareResume(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	if _, err := s.deps.Agent.Resume(c.Request.Context(), plan, sink); err != nil {
		s.deps.Logger.Warn("agent run resume failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"run_id", runId, "error", err)
		s.writeFallbackError(sink, err)
	}
}

// SearchKnowledgeBase 是检索调试视图（Hit Testing，issue #77）的后端。
//
// 【为什么按知识库限定范围】与 ReindexKnowledgeBase 同一条既有语义：
// 路径里的 id 就是范围。这里先查一次知识库，一是让不存在返回 404 而不是
// 一个空结果，二是避免对着一堆别的库的向量做一次没有意义的检索。
func (s *Server) SearchKnowledgeBase(c *gin.Context, id openapi_types.UUID) {
	var req KnowledgeSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		s.fail(c, fmt.Errorf("invalid request body: %w", platform.ErrInvalid))
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

	// 空切片不是 nil：契约里 hits 是数组，nil 会序列化成 null
	// （前端 .map() 会崩）——与 toAPIKBList 那条同一个约定。
	hits := make([]KnowledgeSearchHit, 0, len(chunks))
	for _, ch := range chunks {
		hits = append(hits, KnowledgeSearchHit{
			ChunkId:    ch.ID,
			DocumentId: ch.DocumentID,
			Filename:   ch.Filename,
			Snippet:    ch.Content,
			Score:      ch.Score,
		})
	}
	c.JSON(http.StatusOK, KnowledgeSearchResult{Hits: hits})
}

// 检索调试的 topK 边界。契约里的 min/max 与它们必须一致——
// 那一处是给客户端看的声明，这一处才是强制点。
const (
	knowledgeSearchDefaultTopK = 5
	knowledgeSearchMaxTopK     = 50
)
