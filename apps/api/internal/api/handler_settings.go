// 模型服务（BYOK）与用量端点：provider/model 的增删改查、按模型聚合 token。
//
// 【安全边界】API Key 只在这个文件里"进"（CreateProvider），且立刻交给
// llm.Usecase 加密落库；任何响应都不回显 Key 明文，只回显是否配置过。
// 这是 BYOK 设计成立的前提：服务端只持有密文，界面永远看不到明文。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

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
	if !s.bindJSON(c, &req) {
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

// UpdateModel / DeleteModel 是设置页管理模型条目用的（issue #83）。
//
// 【业务规则在 usecase 里，不在 handler 里】"当前生效的模型删不掉"这条判据
// 依赖 LatestByKind 那套规则，而它属于业务层；handler 只做契约类型与业务类型
// 之间的转换（和 toAPIModelSummary 是同一个方向上的另一件事）。
func (s *Server) UpdateModel(c *gin.Context, id openapi_types.UUID) {
	var req UpdateModelRequest
	if !s.bindJSON(c, &req) {
		return
	}

	m, err := s.deps.LLM.UpdateModel(c.Request.Context(), id, llm.UpdateModelRequest{
		ModelID:         req.ModelId,
		Capabilities:    fromAPICapabilities(&req.Capabilities),
		ContextWindow:   req.ContextWindow,
		MaxOutputTokens: req.MaxOutputTokens,
		TokenizerType:   req.TokenizerType,
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIModelSummary(m))
}

func (s *Server) DeleteModel(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.LLM.DeleteModel(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

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
