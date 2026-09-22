package llm

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Usecase 是 BYOK 引导页背后的业务层。
//
// 五个依赖对应五个职责：repo 存取配置,box 加解密 Key,registry 是
// 探测维度和（将来）真正调用模型的入口,txm 圈定"建 provider/model +
// ALTER 向量列"这一整块的原子边界,db 给不需要事务的只读查询用。
type Usecase struct {
	repo     ConfigRepo
	box      platform.SecretBox
	registry Registry
	txm      platform.TxManager
	db       platform.Querier
	// reindexer 只在"换 embedding 模型、需要把全部文档重新排队"时用到
	// （issue #39），所以允许为 nil——只跑引导流程的测试不必造一个。
	// 装配根传的是 *knowledge.Usecase（见 apps/api/internal/app/app.go）。
	reindexer DocumentReindexer
	// usageRepo 服务的是读端点（issue #47）：按模型聚合的 token 用量。
	// 写入不走这里——那是适配器内部的事（见 usage_record.go）。
	usageRepo UsageRepo
}

func NewUsecase(repo ConfigRepo, box platform.SecretBox, registry Registry, txm platform.TxManager, db platform.Querier, reindexer DocumentReindexer, usageRepo UsageRepo) *Usecase {
	return &Usecase{
		repo: repo, box: box, registry: registry, txm: txm, db: db,
		reindexer: reindexer, usageRepo: usageRepo,
	}
}

// UsageSummary 按模型聚合 token 用量（issue #47）。
//
// since / until 是左闭右开区间，nil 表示不限。
func (u *Usecase) UsageSummary(ctx context.Context, since, until *time.Time) ([]*UsageByModel, error) {
	if u.usageRepo == nil {
		return nil, fmt.Errorf("usage repo is not configured")
	}
	rows, err := u.usageRepo.UsageSummary(ctx, u.db, since, until)
	if err != nil {
		return nil, fmt.Errorf("summarise token usage: %w", err)
	}
	return rows, nil
}

// ChatModelInput 是引导页表单里"聊天模型"那一节的字段,全部手填
// （技术方案 §五：OpenAI 兼容 API 不保证能自动查询这些）。
type ChatModelInput struct {
	ModelID         string
	Capabilities    Capabilities
	ContextWindow   int
	MaxOutputTokens int
	TokenizerType   string
}

// BootstrapRequest 是"保存并开始"那一次提交的全部内容。
//
// 【embedding 模型只要一个 ID】不像 ChatModelInput 那样要手填一堆字段——
// 维度是探测出来的,不需要用户填；embedding 模型不需要 ContextWindow 之类
// 只对生成式模型有意义的字段。
type BootstrapRequest struct {
	BaseURL          string
	APIKey           string
	ChatModel        ChatModelInput
	EmbeddingModelID string

	// AllowEmbeddingReset 是用户已经确认"换 embedding 模型会清空已有向量"的
	// 标记（issue #39）。
	//
	// 默认 false：库里有不属于该模型的向量、或者改列类型会清空已有向量时，
	// 保存直接失败并返回 ErrEmbeddingResetRequired，由前端弹确认框。
	// true：在同一个事务里清空、改列、把全部文档重新排队重建。
	AllowEmbeddingReset bool
}

// BootstrapResult 是三行落库之后返回给 handler 的东西。
type BootstrapResult struct {
	Provider       *Provider
	ChatModel      *Model
	EmbeddingModel *Model

	// RequeuedDocuments 是这次真的被重新排队的文档数。
	//
	// 【只有真的重建过才非零】同模型同维度地重存一次也会传
	// allowEmbeddingReset=true（前端不必判断"这次到底会不会清"——它判不出来），
	// 但那种情况下一条向量都没丢，不该触发全库重建。判据是
	// alterVectorColumns 返回的 resetPerformed，不是请求里的那个开关。
	RequeuedDocuments int
}

// Bootstrap 是引导页"保存并开始"按钮触发的整条流程（开发文档 §4.4）：
//
//	① 探测：直接用表单填的 base_url/key/embeddingModelId 发一次真实请求，量出维度 N
//	② 校验 N：1 <= N <= 4000（HNSW 上限）
//	③ 一个事务里：插 provider → 插 chat model → 插 embedding model(embedding_dim=N)
//	  → ALTER document_chunks/memories 的 embedding 列成 halfvec(N) → 建 HNSW 索引
//
// 【探测必须在事务开始之前】它是一次跨网络的真实 HTTP 调用，耗时不可控
// （几十毫秒到几秒，取决于用户的网络和上游服务）。事务里握着数据库连接，
// 在事务内做慢速网络调用会占住连接池的一个连接不释放——别的请求跟着排队。
// 探测失败时（Key 错、网络不通、模型不存在）什么都不会被写进数据库，
// 这也是"先探测再开事务"的另一个好处：失败态天然是"什么都没发生"。
func (u *Usecase) Bootstrap(ctx context.Context, req BootstrapRequest) (*BootstrapResult, error) {
	if err := validateBootstrapRequest(req); err != nil {
		return nil, err
	}

	// ① 探测。
	dim, err := u.registry.ProbeEmbeddingDimension(ctx, req.BaseURL, req.APIKey, req.EmbeddingModelID)
	if err != nil {
		return nil, fmt.Errorf("probe embedding dimension: %w", err)
	}

	// ② 校验。HNSW 对 halfvec 的上限是 4000 维（技术方案 §六）；
	// 探测本身量出 0 或负数说明上游返回了不该出现的东西，同样拒绝。
	if dim < 1 || dim > maxEmbeddingDim {
		return nil, fmt.Errorf(
			"embedding model %q reports dimension %d, must be between 1 and %d: %w",
			req.EmbeddingModelID, dim, maxEmbeddingDim, platform.ErrInvalid)
	}

	ciphertext, err := u.box.Seal([]byte(req.APIKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt api key: %w", err)
	}

	now := time.Now()
	result := &BootstrapResult{
		Provider: &Provider{ID: uuid.New(), BaseURL: req.BaseURL, CreatedAt: now},
		ChatModel: &Model{
			ID:              uuid.New(),
			ModelID:         req.ChatModel.ModelID,
			Kind:            KindChat,
			Capabilities:    req.ChatModel.Capabilities,
			ContextWindow:   req.ChatModel.ContextWindow,
			MaxOutputTokens: req.ChatModel.MaxOutputTokens,
			TokenizerType:   strings.TrimSpace(req.ChatModel.TokenizerType),
			CreatedAt:       now,
		},
		EmbeddingModel: &Model{
			ID:           uuid.New(),
			ModelID:      req.EmbeddingModelID,
			Kind:         KindEmbedding,
			Capabilities: Capabilities{Embedding: true},
			EmbeddingDim: dim,
			CreatedAt:    now,
		},
	}
	result.ChatModel.ProviderID = result.Provider.ID
	result.EmbeddingModel.ProviderID = result.Provider.ID

	// ③ 一个事务：三行 INSERT + 两组 ALTER/CREATE INDEX。
	//
	// 全部放一个事务里而不是分步骤：如果 ALTER 半途失败（比如表已经有
	// 冲突的索引），provider/model 那三行也应该跟着回滚——不留下
	// "配置存在但向量列没建好"这种一半的状态。
	err = u.txm.InTx(ctx, func(q platform.Querier) error {
		if err := u.repo.UpsertProvider(ctx, q, result.Provider, ciphertext); err != nil {
			return fmt.Errorf("insert provider: %w", err)
		}
		if err := u.repo.UpsertModel(ctx, q, result.ChatModel); err != nil {
			return fmt.Errorf("insert chat model: %w", err)
		}
		if err := u.repo.UpsertModel(ctx, q, result.EmbeddingModel); err != nil {
			return fmt.Errorf("insert embedding model: %w", err)
		}
		reset, err := alterVectorColumns(ctx, q, dim, req.EmbeddingModelID, req.AllowEmbeddingReset)
		if err != nil {
			return fmt.Errorf("alter vector columns to halfvec(%d): %w", dim, err)
		}

		// 这一步必须在同一个事务里（见 DocumentReindexer 的注释）：
		// "ALTER 成功但入队失败"会留下一个既没有旧向量、也没有任何任务在
		// 重建的库，而调用方已经收到了 201。
		//
		// 【按 reset 判，不按 req.AllowEmbeddingReset 判】同模型同维度的
		// 重存也会传 true，但那种情况一条向量都没丢，不该触发全库重建。
		if reset && u.reindexer != nil {
			n, err := u.reindexer.RequeueAllDocuments(ctx, q)
			if err != nil {
				return fmt.Errorf("requeue documents after embedding reset: %w", err)
			}
			result.RequeuedDocuments = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// validateBootstrapRequest 在探测之前挡掉明显不合法的输入——
// 没有理由为一个空的 API Key 去打一次网络请求再报错。
func validateBootstrapRequest(req BootstrapRequest) error {
	if strings.TrimSpace(req.BaseURL) == "" {
		return fmt.Errorf("base url must not be empty: %w", platform.ErrInvalid)
	}
	if _, err := url.ParseRequestURI(req.BaseURL); err != nil {
		return fmt.Errorf("base url %q is not a valid url: %w", req.BaseURL, platform.ErrInvalid)
	}
	if strings.TrimSpace(req.APIKey) == "" {
		return fmt.Errorf("api key must not be empty: %w", platform.ErrInvalid)
	}
	if strings.TrimSpace(req.ChatModel.ModelID) == "" {
		return fmt.Errorf("chat model id must not be empty: %w", platform.ErrInvalid)
	}
	if req.ChatModel.ContextWindow <= 0 {
		return fmt.Errorf("chat model context window must be positive: %w", platform.ErrInvalid)
	}
	if req.ChatModel.MaxOutputTokens <= 0 {
		return fmt.Errorf("chat model max output tokens must be positive: %w", platform.ErrInvalid)
	}
	if strings.TrimSpace(req.ChatModel.TokenizerType) == "" {
		return fmt.Errorf("chat model tokenizer type must not be empty: %w", platform.ErrInvalid)
	}
	// 只校验非空不够：任意字符串都会落库、引导页照样 201，而聊天路径上
	// 唯一的消费者 newTokenizer 只认 ValidTokenizerTypes 里那两个值——
	// 坏值会在之后每一条消息上爆发，还被归因到用户的消息。保存时挡住它。
	if !ValidTokenizerTypes[strings.TrimSpace(req.ChatModel.TokenizerType)] {
		return fmt.Errorf(
			"chat model tokenizer type %q is not supported, must be one of cl100k_base/o200k_base: %w",
			req.ChatModel.TokenizerType, platform.ErrInvalid)
	}
	if strings.TrimSpace(req.EmbeddingModelID) == "" {
		return fmt.Errorf("embedding model id must not be empty: %w", platform.ErrInvalid)
	}
	return nil
}

// ListProviders 给引导页/设置页展示"已经配置了什么"。
//
// 【不返回 Key,连打码后的都不返回】Provider 结构体本身就没有 Key 字段
// （见 model.go 的注释），这里只是原样转给 repo，没有额外要做的事。
func (u *Usecase) ListProviders(ctx context.Context) ([]*Provider, error) {
	providers, err := u.repo.ListProviders(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return providers, nil
}

// ListModels 给引导页/设置页展示每个 Provider 下挂了哪些模型。
func (u *Usecase) ListModels(ctx context.Context) ([]*Model, error) {
	models, err := u.repo.ListModels(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	return models, nil
}

// UpdateModelRequest 是 UpdateModel 的入参。
//
// 【为什么在业务包里再定义一份，而不是直接收契约生成的类型】业务层不认识、
// 也不该认识 oapi-codegen 生成的结构体——那条边界在 apps/api 的 handler 上
// （它负责把契约类型转成业务类型，和 toAPIModelSummary 是同一个方向上的
// 另一件事）。这一份只有 5 个字段，转一次的代价远小于"业务包 import 生成物"。
type UpdateModelRequest struct {
	ModelID         string
	Capabilities    Capabilities
	ContextWindow   int
	MaxOutputTokens int
	TokenizerType   string
}

// UpdateModel 改一个模型条目（issue #83）。
//
// 【哪些改不了，以及为什么】
//   - `kind`：它的语义是"这个模型是干什么用的"，改它等于换了一个模型。
//   - provider：会让"这个模型名在哪家接入点下有效"无从判断。
//   - embedding 模型的**模型名**：向量列的维度是引导时按那个模型探测出来
//     并 ALTER 到表上的（ADR-004），改名字会让列里那些向量和配置对不上。
//     要换 embedding 模型，走引导页那条会清空重建的流程——那里有
//     `allowEmbeddingReset` 那道确认。
//
// 【校验与 Bootstrap 用同一批常量】tokenizer_type 必须在 ValidTokenizerTypes
// 里（理由见 tokenizer.go 那段注释：这一个点不挡，非法值会先落库、之后每条
// 消息才在聊天路径上失败）。context_window / max_output_tokens 必须为正。
func (u *Usecase) UpdateModel(ctx context.Context, id uuid.UUID, req UpdateModelRequest) (*Model, error) {
	m, err := u.repo.GetModel(ctx, u.db, id)
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, err)
	}

	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		return nil, fmt.Errorf("model name must not be empty: %w", platform.ErrInvalid)
	}
	if m.Kind == KindEmbedding && modelID != m.ModelID {
		return nil, fmt.Errorf(
			"cannot rename an embedding model (%s → %s): the vector column's dimension was probed from %q "+
				"and altering the name would leave the stored vectors unattributable; "+
				"use the onboarding flow to switch embedding models: %w",
			m.ModelID, modelID, m.ModelID, platform.ErrConflict)
	}
	if req.ContextWindow <= 0 {
		return nil, fmt.Errorf("contextWindow must be positive, got %d: %w", req.ContextWindow, platform.ErrInvalid)
	}
	if req.MaxOutputTokens <= 0 {
		return nil, fmt.Errorf("maxOutputTokens must be positive, got %d: %w", req.MaxOutputTokens, platform.ErrInvalid)
	}
	tokenizerType := strings.TrimSpace(req.TokenizerType)
	if !ValidTokenizerTypes[tokenizerType] {
		return nil, fmt.Errorf(
			"unknown tokenizerType %q (supported: cl100k_base, o200k_base): %w",
			tokenizerType, platform.ErrInvalid)
	}

	m.ModelID = modelID
	m.Capabilities = Capabilities(req.Capabilities)
	m.ContextWindow = req.ContextWindow
	m.MaxOutputTokens = req.MaxOutputTokens
	m.TokenizerType = tokenizerType

	if err := u.repo.UpdateModel(ctx, u.db, m); err != nil {
		return nil, fmt.Errorf("update model %s: %w", id, err)
	}
	return m, nil
}

// DeleteModel 删掉一个模型条目（issue #83）。
//
// 【当前生效的那个删不掉】当前生效模型由 LatestByKind 按 created_at 决定，
// 而它是发消息 / 检索 / 跑 Agent 都要用的东西。删掉它之后失败点离用户的
// 操作很远（发下一条消息时才炸）。判据用**同一条规则**（LatestByKind），
// 不是另写一个查询——两处规则不一致的话，会出现"以为删的是没在用的那条、
// 实际删掉了在用的那条"。
//
// 【用量记录会跟着消失】token_usage.model_id 的外键是 ON DELETE CASCADE，
// 这是既有的表结构（migrations/0003）。契约与前端确认框里都写明了这一点。
func (u *Usecase) DeleteModel(ctx context.Context, id uuid.UUID) error {
	m, err := u.repo.GetModel(ctx, u.db, id)
	if err != nil {
		return fmt.Errorf("get model %s: %w", id, err)
	}

	models, err := u.repo.ListModels(ctx, u.db)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	if active := LatestByKind(models, m.Kind); active != nil && active.ID == m.ID {
		return fmt.Errorf(
			"model %s (%s, kind=%s) is the active %s model; deleting it would break every request "+
				"that needs one — create its replacement first: %w",
			m.ID, m.ModelID, m.Kind, m.Kind, platform.ErrConflict)
	}

	if err := u.repo.DeleteModel(ctx, u.db, id); err != nil {
		return fmt.Errorf("delete model %s: %w", id, err)
	}
	return nil
}
