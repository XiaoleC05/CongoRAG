package llm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ────────────────────────────────────────────────────────────────
// 配置存取（BYOK）
// ────────────────────────────────────────────────────────────────

// ConfigRepo 是 Provider/Model 的存取接口,由 postgres.go 实现。
//
// 【为什么 Key 单独一个方法】GetProviderKey 只在真正要调用模型时才被叫到
// （Registry 解析出 Provider 之后取密文再解密),ListProviders/GetProvider
// 这类给列表页/装配用的方法完全不碰它——密文离"可能被记进日志"的路径越远越好。
type ConfigRepo interface {
	// UpsertProvider 插入或更新一个 Provider。
	// keyCiphertext 是 platform.SecretBox.Seal 的输出,repo 只管存,不关心怎么加密的。
	UpsertProvider(ctx context.Context, q platform.Querier, p *Provider, keyCiphertext []byte) error

	ListProviders(ctx context.Context, q platform.Querier) ([]*Provider, error)

	GetProvider(ctx context.Context, q platform.Querier, id uuid.UUID) (*Provider, error)

	// GetProviderKey 只返回密文,解密由调用方（ConfigUsecase）用 SecretBox 做——
	// repo 不认识 SecretBox,这样密码学实现换掉时 postgres.go 不用动。
	GetProviderKey(ctx context.Context, q platform.Querier, id uuid.UUID) ([]byte, error)

	UpsertModel(ctx context.Context, q platform.Querier, m *Model) error

	ListModels(ctx context.Context, q platform.Querier) ([]*Model, error)

	GetModel(ctx context.Context, q platform.Querier, id uuid.UUID) (*Model, error)
}

// ────────────────────────────────────────────────────────────────
// 线路层类型：模型调用的输入输出
//
// 【和 conversation.Message 不是一回事】那是持久化实体（有 id/sequence_no/
// status),这里只是"发给模型的一条消息"，没有任何数据库概念。
// ────────────────────────────────────────────────────────────────

type Message struct {
	Role domain.Role
	// Content 是这条消息的文本。assistant 消息如果只是工具调用请求,
	// Content 可能是空字符串,内容在 ToolCalls 里。
	Content string
	// ToolCalls 只在 assistant 消息上有意义：模型请求执行哪些工具。
	ToolCalls []ToolCall
	// ToolCallID 只在 role = tool 的消息上有意义：对应上面哪一次 ToolCall。
	ToolCallID string
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// CallConfig 是一次 Generate/Stream 调用的可选参数,用 Functional Options
// （技术方案 §4.1）设置——大多数调用不需要覆盖任何一项,不应该逼调用方
// 每次都传一个完整的配置结构体。
type CallConfig struct {
	Temperature *float32
	MaxTokens   *int
}

type CallOption func(*CallConfig)

func WithTemperature(t float32) CallOption {
	return func(c *CallConfig) { c.Temperature = &t }
}

func WithMaxTokens(n int) CallOption {
	return func(c *CallConfig) { c.MaxTokens = &n }
}

// ChatModel 是"能对话"的模型。
//
// 【Stream 返回接口,不是 *Stream】指针指向接口是 Go 反模式——
// 调用方拿到 *Stream 之后 st.Recv() 这行根本编译不过（undefined）,
// 只有先解引用成接口值才能调用方法。见代码架构设计 §5.1 的同一条注释。
type ChatModel interface {
	Generate(ctx context.Context, msgs []Message, opts ...CallOption) (*Message, error)
	Stream(ctx context.Context, msgs []Message, opts ...CallOption) (Stream, error)
}

type Stream interface {
	// Recv 读下一个增量。流结束时返回 io.EOF。
	Recv() (*Message, error)
	Close() error
}

// Embedder 把文本转成向量。
//
// Dim 在第一次成功的 Embed 调用之后才有意义——探测维度这件事
// （BootstrapEmbedding）本质就是"调一次 Embed,再读 Dim()"。
// 调用之前 Dim() 返回 0,这是"还没探测过"的信号,不是"维度是 0"。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
}

// Tokenizer 只有一个方法：数一段文本占多少 token。
//
// 生产实现是 tokenizer.go 里按 tokenizer_type 建的 tiktoken 实例；
// ctxmgr 的预算阶梯测试用假 tokenizer（Count() 返回固定值），
// 不依赖这里的真实精度，见代码架构设计 §5.7。
type Tokenizer interface {
	Count(text string) int
}

// Registry 是配置到可调用组件的工厂（技术方案 §4.1 的工厂模式）。
//
// modelID 参数统一是 llm_models.id（uuid 的字符串形式),不是 Model.ModelID
// 那个人类可读的名字——一个 uuid 唯一对应"哪个 provider + 哪把 key +
// 哪个模型名"这一整套,人类可读名字在不同 provider 下可能重复。
//
// 【本轮先不放 ToolCalling/WithTools】技术方案 §8 已经把 flow/react 那条路
// 标成旧路,现在推荐 ADK 的 adk.ChatModelAgent + adk.Runner——它自己接管
// 工具绑定和 ReAct 循环,不需要业务层先拿到一个 ToolCallingModel 再手动
// WithTools。M4-A 真正接 Agent 时验证过：ADK 需要的是 Eino 原生的
// model.ToolCallingChatModel（它的 WithTools 签名收 []*schema.ToolInfo,
// 返回 model.ToolCallingChatModel——都是 Eino 的类型),不是本包这里的
// ChatModel 包装类型,所以这个方法确实不需要加。ADK 的接入点在
// internal/agent/eino_adk.go,该文件是 agent 包唯一 import Eino 的地方
// （和这个包里 eino.go 的角色对称),下面的 ResolveChatEndpoint 就是
// 为它开的窄口子。
type Registry interface {
	Chat(ctx context.Context, modelID string) (ChatModel, error)
	Embedder(ctx context.Context, modelID string) (Embedder, error)
	Tokenizer(ctx context.Context, modelID string) (Tokenizer, error)
	Capabilities(ctx context.Context, modelID string) (Capabilities, error)

	// ResolveChatEndpoint 把 modelID 解析成裸的连接三元组（base_url、
	// 解密后的明文 key、模型名字符串)。
	//
	// 【为什么不直接返回一个 Eino 类型】这个包只有 eino.go 一个文件
	// import Eino——如果这个方法返回 model.ToolCallingChatModel,
	// port.go 本身就要 import github.com/cloudwego/eino/components/model,
	// 那条隔离线就穿了。返回三个裸字符串,让 agent 包自己用这三个值
	// 去构造它自己需要的 Eino 类型（eino_adk.go 里几行和这个包的
	// Chat() 方法结构相同、但类型不同的构造代码——一点小重复,换来的是
	// 两个包各自的 Eino 隔离线都不破）。
	ResolveChatEndpoint(ctx context.Context, modelID string) (baseURL, apiKey, modelName string, err error)

	// ActiveModelID 解析出"当前配置的那个 kind 类型的模型"的 uuid
	//（LatestByKind 的定义，见 model.go）。conversation.Usecase.Send 用它
	// 找到 chat 模型的 id，再把这个 id 传给上面几个方法——本包内部只有
	// 这一处需要遍历 ListModels，其余方法统一只接 modelID 这个已解析好的
	// 身份，port.go 的其它接口不必因为"怎么找到当前模型"这件事而变复杂。
	//
	// 只需要 id 时用它；需要整行（比如读 Capabilities 或给用户看模型名）
	// 时用下面的 ActiveModel，别去 ListModels 里自己找一遍。
	ActiveModelID(ctx context.Context, kind Kind) (modelID string, err error)

	// ActiveModel 返回当前生效的那一行模型，判据与 ActiveModelID 完全一致
	//（同一套 LatestByKind）。
	//
	// 【为什么需要整行】agent 的工具门控要在报文里说清"是哪个模型缺哪个
	// 能力"，而 llm_models 的 uuid 对用户没有意义——用户认得的是他自己在
	// 引导页里敲进去的那个模型名（ModelID 字段）。顺带也省掉了
	// "先拿 id、再 GetModel 查回来"的那一次往返。
	ActiveModel(ctx context.Context, kind Kind) (*Model, error)

	// ProbeEmbeddingDimension 是引导页"保存并开始"那一刻用的：
	// 直接用表单上填的 base_url/key/model 发一次真实的 embedding 请求,
	// 量出返回向量的长度。
	//
	// 【故意不走 modelID 那一套】此刻这个模型还没有 llm_models 行——
	// 探测本身就是"决定要不要建这一行"的前提,不能反过来要求先有行才能探测。
	ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error)

	// RecordUsage 记一次模型调用的 token 用量（issue #47）。
	//
	// 【为什么业务代码也能调它】绝大多数调用点由适配器内部自动覆盖（见
	// eino.go 的 chat / embedding 适配器），但 Agent 的 ADK 循环自己构造
	// Eino 原生模型、刻意不经过适配器（见 agent/eino_adk.go 的文件头注释），
	// 那条路径只能由调用方把用量报回来。
	//
	// 记账失败不上抛、只记日志——它是观测，不是业务正确性的一部分。
	RecordUsage(ctx context.Context, modelID string, kind Kind, u Usage)
}

// UsageRepo 是 token 用量表的读写。
//
// 【为什么和 ConfigRepo 分开】它服务的是"记录与统计"，不是"接入配置"；
// 两张表虽然都在 llm 包手里（token_usage 的两个外键恰好指向 llm_providers
// 与 llm_models），但读写时机完全不同——配置是用户改的，用量是每次调用
// 自动写的。分开之后测试可以只替身其中一个。
type UsageRepo interface {
	// InsertUsage 落一行用量。provider_id / model_id 是 NOT NULL 外键，
	// 所以调用方必须先解析出真实存在的模型行。
	InsertUsage(ctx context.Context, q platform.Querier, u *Usage) error

	// UsageSummary 按模型聚合，since/until 是左闭右开区间（nil 表示不限）。
	//
	// 【为什么没有 created_at 索引】行数 = LLM 调用次数，本地单机一年的量级
	// 是几千到几万行，一次顺序扫 + 哈希聚合在毫秒级。等到十万行再加
	// `CREATE INDEX token_usage_created_at_idx ON token_usage (created_at DESC)`。
	UsageSummary(ctx context.Context, q platform.Querier, since, until *time.Time) ([]*UsageByModel, error)
}

// UsageByModel 是读端点的一行：某个模型累计用了多少。
type UsageByModel struct {
	ProviderID       uuid.UUID
	ModelID          uuid.UUID
	ModelName        string // llm_models.model_id —— 用户在引导页里敲的那个名字
	Kind             Kind
	Calls            int64
	PromptTokens     int64
	CompletionTokens int64
}

// DocumentReindexer 把"把所有文档重新排队处理"这件事，从 knowledge 包带进
// llm 的重新配置流程里（issue #39，规则 A：端口由消费方声明）。
//
// 【它为什么必须存在】换 embedding 模型有三步：清空旧向量、ALTER 列到新
// 维度、全部文档重新排队。第三步如果在外层单独提交，「ALTER 成功但入队失败」
// 会留下一个既没有旧向量、也没有任何任务在重建的库——正是这条 issue 要消掉
// 的状态。所以第三步必须和前面两步在同一个事务里，而那个事务在 llm.Usecase
// 手里，于是需要一个接口让它能驱动 knowledge 的动作。
//
// 【边界论证，别把它当成可以先例随意扩大的口子】它在依赖表上完全合法：
// 端口由消费方（llm）声明，实现方（knowledge.Usecase）零 import 边——
// knowledge 不认识 llm，llm 也不认识 knowledge，唯一的连接点是
// apps/api/internal/app/app.go 的装配顺序。它打破的是「llm 只管模型接入」
// 这个直觉，换来的是上面那条原子性。要再加类似方法之前，先确认它同样
// 换到了别的办法拿不到的性质。
type DocumentReindexer interface {
	// RequeueAllDocuments 把所有可重建的文档标回 queued 并批量入队，
	// 返回这次真的排进去的数量。它不自己开事务——q 由调用方给，
	// 而调用方保证那是一个已经打开的事务。
	RequeueAllDocuments(ctx context.Context, q platform.Querier) (int, error)
}
