package llm

import (
	"context"
	"encoding/json"

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
}
