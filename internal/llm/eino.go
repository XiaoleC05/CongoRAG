// eino.go 是全项目唯一 import cloudwego/eino 的文件。
//
// 换编排框架时,只有这一个文件需要重写——port.go 里的 ChatModel/Embedder/
// Registry 都是本包自己定义的接口,不是 Eino 的类型,其余七个业务包
// 拿到的永远是这些接口,从未见过 schema.Message 或 *openai.ChatModel。
package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/google/uuid"

	einoembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	einochatmodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/schema"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Registry = (*registry)(nil)

// registry 是 Registry 唯一的实现。
//
// 三个依赖：repo/db 用来把 modelID（llm_models.id）解析成
// "哪个 provider + 哪个模型名",box 用来把存的密文变回明文 Key——
// 每次真正要调用模型时才解密,不提前缓存明文。
type registry struct {
	repo ConfigRepo
	box  platform.SecretBox
	db   platform.Querier
	// usageRepo 是 token 用量记账（issue #47）。允许为 nil——测试里不关心
	// 记账时不必造一个；适配器会跳过记账，对话照常跑。
	usageRepo UsageRepo
}

// NewRegistry 装配一个 Registry。
//
// tiktokenCacheDir 只在进程启动时生效一次：weaviate/tiktoken-go 按
// TIKTOKEN_CACHE_DIR 环境变量决定 BPE 词表缓存到哪（tokenizer.go 的
// getTiktokenEncoding 最终调用它），这是那个库自己文档化的配置方式，
// 不是本项目发明的约定——这里只是把它接到 platform.Config 上，
// 不让缓存目录散落在系统临时目录里（原因见 config.go 里的注释）。
func NewRegistry(repo ConfigRepo, box platform.SecretBox, db platform.Querier, tiktokenCacheDir string, usageRepo UsageRepo) Registry {
	os.Setenv("TIKTOKEN_CACHE_DIR", tiktokenCacheDir)
	return &registry{repo: repo, box: box, db: db, usageRepo: usageRepo}
}

// RecordUsage 记一次模型调用的用量（issue #47）。
//
// 【为什么入口在这里】12 个 LLM 调用点里有 11 个通过 Registry 拿到模型句柄，
// 而它们的用量由适配器内部自动记（见下面 einoChatModel / einoEmbedder）。
// 唯一例外是 Agent 的 ADK 循环——它自己构造 Eino 原生模型、刻意不经过适配器
// （见 agent/eino_adk.go 的文件头注释），所以那条路径由调用方把用量报回来。
//
// 【为什么接收的是 modelID 字符串】调用方（agent.Usecase）手里只有从
// ActiveModelID 拿到的那个字符串。解析成行是这里的事。
func (r *registry) RecordUsage(ctx context.Context, modelID string, kind Kind, u Usage) {
	rec, err := r.recorderFor(ctx, modelID, kind)
	if err != nil {
		// 解析失败说明这个 modelID 已经不存在了（配置被换掉）。记账丢掉即可，
		// 不上抛——调用方是 Agent 的运行循环，不该为一个观测失败而中断。
		slog.Default().Warn("cannot resolve model for usage record",
			"model_id", modelID, "kind", kind, "error", err)
		return
	}
	rec.record(ctx, u.PromptTokens, u.CompletionTokens)
}

// recorderFor 解析出一个可用的记账句柄；usageRepo 缺失时返回不可用的那个。
func (r *registry) recorderFor(ctx context.Context, modelID string, kind Kind) (usageRecorder, error) {
	if r.usageRepo == nil {
		return usageRecorder{}, nil
	}
	id, err := uuid.Parse(modelID)
	if err != nil {
		return usageRecorder{}, fmt.Errorf("model id %q is not a valid uuid: %w", modelID, platform.ErrInvalid)
	}
	m, err := r.repo.GetModel(ctx, r.db, id)
	if err != nil {
		return usageRecorder{}, fmt.Errorf("get model %s: %w", modelID, err)
	}
	return usageRecorder{
		repo: r.usageRepo, db: r.db,
		providerID: m.ProviderID, modelID: m.ID, kind: kind,
	}, nil
}

// resolve 把 modelID 解析成"这个模型属于哪个 provider,该用哪个明文 Key"。
//
// modelID 是 llm_models.id 的字符串形式（port.go 的 Registry 注释解释了
// 为什么不用人类可读的 ModelID 字段）。
func (r *registry) resolve(ctx context.Context, modelID string) (*Model, *Provider, string, error) {
	id, err := uuid.Parse(modelID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("model id %q is not a valid uuid: %w", modelID, platform.ErrInvalid)
	}

	m, err := r.repo.GetModel(ctx, r.db, id)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get model %s: %w", modelID, err)
	}

	p, err := r.repo.GetProvider(ctx, r.db, m.ProviderID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get provider %s: %w", m.ProviderID, err)
	}

	ciphertext, err := r.repo.GetProviderKey(ctx, r.db, p.ID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get provider key %s: %w", p.ID, err)
	}

	plainKey, err := r.box.Open(ciphertext)
	if err != nil {
		return nil, nil, "", fmt.Errorf("decrypt provider key %s: %w", p.ID, err)
	}

	return m, p, string(plainKey), nil
}

func (r *registry) Chat(ctx context.Context, modelID string) (ChatModel, error) {
	m, p, key, err := r.resolve(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if m.Kind != KindChat {
		return nil, fmt.Errorf("model %s is not a chat model: %w", modelID, platform.ErrInvalid)
	}

	cm, err := einochatmodel.NewChatModel(ctx, &einochatmodel.ChatModelConfig{
		APIKey:  key,
		BaseURL: p.BaseURL,
		Model:   m.ModelID,
	})
	if err != nil {
		// 构造失败（不是调用失败）目前只有配置问题会触发——同样映射成
		// ErrUpstream（502）：对用户来说"建不起客户端"和"调用失败"
		// 都该引向同一个排查方向：检查 Base URL / Key / 模型名。
		return nil, fmt.Errorf("%w: init chat model %s: %v", platform.ErrUpstream, modelID, err)
	}
	// 记账句柄在构造时就解析好（provider/model 的 uuid 这里已经有了），
	// 调用时不必再查一遍。usageRepo 为 nil 时它是一个不可用的零值，
	// 适配器会跳过记账。
	rec, recErr := r.recorderFor(ctx, m.ID.String(), KindChat)
	if recErr != nil {
		rec = usageRecorder{}
	}
	return &einoChatModel{inner: cm, rec: rec}, nil
}

// ResolveChatEndpoint 见 port.go 的注释：只返回裸字符串,不返回 Eino 类型。
func (r *registry) ResolveChatEndpoint(ctx context.Context, modelID string) (baseURL, apiKey, modelName string, err error) {
	m, p, key, err := r.resolve(ctx, modelID)
	if err != nil {
		return "", "", "", err
	}
	if m.Kind != KindChat {
		return "", "", "", fmt.Errorf("model %s is not a chat model: %w", modelID, platform.ErrInvalid)
	}
	return p.BaseURL, key, m.ModelID, nil
}

func (r *registry) Embedder(ctx context.Context, modelID string) (Embedder, error) {
	m, p, key, err := r.resolve(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if m.Kind != KindEmbedding {
		return nil, fmt.Errorf("model %s is not an embedding model: %w", modelID, platform.ErrInvalid)
	}

	emb, err := einoembedding.NewEmbedder(ctx, &einoembedding.EmbeddingConfig{
		APIKey:  key,
		BaseURL: p.BaseURL,
		Model:   m.ModelID,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: init embedder %s: %v", platform.ErrUpstream, modelID, err)
	}
	rec, recErr := r.recorderFor(ctx, m.ID.String(), KindEmbedding)
	if recErr != nil {
		rec = usageRecorder{}
	}
	return &einoEmbedder{inner: emb, rec: rec}, nil
}

// ProbeEmbeddingDimension 直接用表单上的原始字段发一次真实请求,
// 不经过 ConfigRepo——探测这一步的存在理由就是"决定要不要把这些字段
// 写成一行 llm_models",不能要求先有那一行才能探测。
func (r *registry) ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error) {
	emb, err := einoembedding.NewEmbedder(ctx, &einoembedding.EmbeddingConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   modelID,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: init embedder: %v", platform.ErrUpstream, err)
	}

	// 探测文本内容不重要,只要上游把它当成一次合法的 embedding 请求处理。
	vecs, err := emb.EmbedStrings(ctx, []string{"ConGoRAG embedding dimension probe"})
	if err != nil {
		return 0, fmt.Errorf("%w: probe request: %v", platform.ErrUpstream, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("%w: embedding endpoint returned an empty vector", platform.ErrUpstream)
	}
	return len(vecs[0]), nil
}

func (r *registry) ActiveModel(ctx context.Context, kind Kind) (*Model, error) {
	models, err := r.repo.ListModels(ctx, r.db)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	m := LatestByKind(models, kind)
	if m == nil {
		return nil, fmt.Errorf("no %s model configured yet: %w", kind, platform.ErrNotFound)
	}
	return m, nil
}

// ActiveModelID 是 ActiveModel 的薄包装——「哪个模型是当前生效的」这条判据
// 只能有一处实现，多写一遍迟早会漂移。
func (r *registry) ActiveModelID(ctx context.Context, kind Kind) (string, error) {
	m, err := r.ActiveModel(ctx, kind)
	if err != nil {
		return "", err
	}
	return m.ID.String(), nil
}

func (r *registry) Capabilities(ctx context.Context, modelID string) (Capabilities, error) {
	id, err := uuid.Parse(modelID)
	if err != nil {
		return Capabilities{}, fmt.Errorf("model id %q is not a valid uuid: %w", modelID, platform.ErrInvalid)
	}
	m, err := r.repo.GetModel(ctx, r.db, id)
	if err != nil {
		return Capabilities{}, fmt.Errorf("get model %s: %w", modelID, err)
	}
	return m.Capabilities, nil
}

// Tokenizer 按 modelID 解析出 tokenizer_type，再交给 newTokenizer 建真实的
// tiktoken 实例（tokenizer.go）。三个真实字符串对官方 Python tiktoken 核对
// 过 cl100k_base/o200k_base 两种编码，token id 逐一相同（开发文档 §6.2）。
func (r *registry) Tokenizer(ctx context.Context, modelID string) (Tokenizer, error) {
	id, err := uuid.Parse(modelID)
	if err != nil {
		return nil, fmt.Errorf("model id %q is not a valid uuid: %w", modelID, platform.ErrInvalid)
	}
	m, err := r.repo.GetModel(ctx, r.db, id)
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", modelID, err)
	}
	return newTokenizer(m.TokenizerType)
}

// ────────────────────────────────────────────────────────────────
// ChatModel 适配器
// ────────────────────────────────────────────────────────────────

type einoChatModel struct {
	inner *einochatmodel.ChatModel
	rec   usageRecorder
}

func (c *einoChatModel) Generate(ctx context.Context, msgs []Message, opts ...CallOption) (*Message, error) {
	// TODO(M3): CallOption（Temperature/MaxTokens）还没有转成 Eino 的
	// model.Option 传下去。M1 不需要控制这两个参数,先占住接口形状,
	// 等 conversation.Send 真正需要覆盖默认值时再补这层转换。
	_ = opts

	out, err := c.inner.Generate(ctx, toEinoMessages(msgs))
	if err != nil {
		return nil, fmt.Errorf("%w: generate: %v", platform.ErrUpstream, err)
	}
	// 【非流式的 usage 在 ResponseMeta 上】fromEinoMessage 只复制正文，
	// ResponseMeta 被丢掉，所以要在转换之前读。
	//
	// 摘要压缩、偏好抽取、预算压缩走的都是这条路（它们调 Generate）。
	c.rec.recordFromMeta(ctx, out.ResponseMeta)
	return fromEinoMessage(out), nil
}

func (c *einoChatModel) Stream(ctx context.Context, msgs []Message, opts ...CallOption) (Stream, error) {
	_ = opts // 同上

	sr, err := c.inner.Stream(ctx, toEinoMessages(msgs))
	if err != nil {
		return nil, fmt.Errorf("%w: stream: %v", platform.ErrUpstream, err)
	}
	return &einoStream{inner: sr, rec: c.rec, ctx: ctx}, nil
}

type einoStream struct {
	inner *schema.StreamReader[*schema.Message]
	rec   usageRecorder

	// ctx 只用来取记账需要的东西（调用方注入的 message_id）。
	//
	// 【为什么把 ctx 存在结构体上】llm.Stream 接口的 Recv/Close 没有 ctx
	// 参数，而记账发生在 Recv 里。它不参与生命周期控制——记录时用的是
	// context.WithoutCancel（见 usageRecorder.record），所以这个 ctx 被取消
	// 不影响记账，也不会因为被存下来而泄漏什么东西。
	ctx context.Context

	// reported 保证一次调用只记一行：正常是 Recv 看到 usage 那一次，
	// 兜底是读到 EOF 时补一行 token=0 的。
	reported sync.Once
}

// recordOnce 记一行用量，且一次调用只记一行。
//
// 【msg 为 nil 是"上游根本没回 usage"那一档】它记一行 token 为 0 的。
// 行数代表调用次数，是可观测事实——少一行比 token 记 0 更难发现。
//
// 【为什么每个块都要过一遍】上游的 usage 可能挂在最后一个正文块上，也可能
// 单独发一个"只有 ResponseMeta、Content 为空"的块。两种都要认，reported
// 保证只落一行。
func (s *einoStream) recordOnce(msg *schema.Message) {
	if msg != nil && (msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil) {
		return
	}
	s.reported.Do(func() {
		if msg == nil {
			s.rec.record(s.ctx, 0, 0)
			return
		}
		s.rec.record(s.ctx, msg.ResponseMeta.Usage.PromptTokens, msg.ResponseMeta.Usage.CompletionTokens)
	})
}

func (s *einoStream) Recv() (*Message, error) {
	msg, err := s.inner.Recv()
	if err != nil {
		// io.EOF 原样传递——llm.Stream 接口的约定和 Eino 一致,
		// 调用方（M2 的 conversation.Send）靠它判断流结束,不靠额外的信号。
		//
		// 【EOF 时也要兜底记一行】正常路径下 usage 随最后一个块到达、上面
		// 那次 recordStreamUsage 已经记过了（reported 会挡住重复）。这里
		// 覆盖的是另一种结尾：上游根本没回 usage。行数代表调用次数，是
		// 可观测事实——少一行比 token 记 0 更难发现。
		s.recordOnce(nil)
		return nil, err
	}
	// 【在转换之前读】fromEinoMessage 只复制正文，ResponseMeta 会被丢掉。
	s.recordOnce(msg)
	return fromEinoMessage(msg), nil
}

func (s *einoStream) Close() error {
	s.inner.Close()
	return nil
}

// ────────────────────────────────────────────────────────────────
// Embedder 适配器
// ────────────────────────────────────────────────────────────────

// einoEmbedder 把 eino 核心的 embedding.Embedder（EmbedStrings 返回
// [][]float64）适配成 llm.Embedder（[][]float32）。
//
// 【为什么要转 float32】pgvector 的 halfvec 底层是 float32（存储时再压成
// float16,但 Go 侧的接口是 float32）,float64 的向量存不进去。
// 这个转换只在这一处发生,业务层往下传的向量全部是 float32。
type einoEmbedder struct {
	inner embedding.Embedder
	rec   usageRecorder

	mu  sync.Mutex
	dim int
}

func (e *einoEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	// 【embedding 的用量只能从回调里拿】eino 的 embedding.Embedder 接口只返回
	// [][]float64，用量不在这条返回值上——上游把它放在
	// embedding.CallbackOutput.TokenUsage 里通过 callbacks.OnEnd 发出来。
	// InitCallbacks 是公开包里唯一能在 standalone 调用上挂 handler 的入口
	// （不要用已废弃的全局 InitCallbackHandlers）。
	//
	// 【拿不到也要记一行】有些上游（本地 ollama、部分中转）不回 usage。这时
	// 记一行 token 为 0 的：行数代表调用次数，是可观测事实，少一行比 token
	// 记 0 更难发现——文档索引一份大文档是几十次调用，少记了看不出来。
	var captured *embedding.TokenUsage
	ctx = callbacks.InitCallbacks(ctx,
		&callbacks.RunInfo{Name: "congorag-embedder", Component: components.ComponentOfEmbedding},
		callbacks.NewHandlerBuilder().OnEndFn(
			func(ctx context.Context, _ *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
				if o, ok := out.(*embedding.CallbackOutput); ok && o.TokenUsage != nil {
					captured = o.TokenUsage
				}
				return ctx
			}).Build(),
	)

	vecs, err := e.inner.EmbedStrings(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("%w: embed: %v", platform.ErrUpstream, err)
	}

	if captured != nil {
		e.rec.record(ctx, captured.PromptTokens, captured.CompletionTokens)
	} else {
		e.rec.record(ctx, 0, 0)
	}

	out := make([][]float32, len(vecs))
	for i, v := range vecs {
		out[i] = float64SliceToFloat32(v)
	}

	// 记住这次调用量出的维度,供 Dim() 在探测流程里读回——
	// Dim() 本身不发请求,只报告"上一次 Embed 调用得到了几维"。
	if len(out) > 0 {
		e.mu.Lock()
		e.dim = len(out[0])
		e.mu.Unlock()
	}

	return out, nil
}

func (e *einoEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}

func float64SliceToFloat32(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return out
}

// ────────────────────────────────────────────────────────────────
// 消息类型的转换：llm.Message ↔ schema.Message
// ────────────────────────────────────────────────────────────────

func toEinoMessages(msgs []Message) []*schema.Message {
	out := make([]*schema.Message, len(msgs))
	for i, m := range msgs {
		out[i] = &schema.Message{
			Role:       toEinoRole(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toEinoToolCalls(m.ToolCalls),
		}
	}
	return out
}

func toEinoRole(r domain.Role) schema.RoleType {
	switch r {
	case domain.RoleSystem:
		return schema.System
	case domain.RoleAssistant:
		return schema.Assistant
	case domain.RoleTool:
		return schema.Tool
	default:
		return schema.User
	}
}

func toEinoToolCalls(tcs []ToolCall) []schema.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = schema.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      tc.Name,
				Arguments: string(tc.Args),
			},
		}
	}
	return out
}

func fromEinoMessage(m *schema.Message) *Message {
	return &Message{
		Role:       fromEinoRole(m.Role),
		Content:    m.Content,
		ToolCalls:  fromEinoToolCalls(m.ToolCalls),
		ToolCallID: m.ToolCallID,
	}
}

func fromEinoRole(r schema.RoleType) domain.Role {
	switch r {
	case schema.System:
		return domain.RoleSystem
	case schema.Assistant:
		return domain.RoleAssistant
	case schema.Tool:
		return domain.RoleTool
	default:
		return domain.RoleUser
	}
}

func fromEinoToolCalls(tcs []schema.ToolCall) []ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: []byte(tc.Function.Arguments),
		}
	}
	return out
}
