// Package llm 拥有模型接入（BYOK）和对 cloudwego/eino 的唯一一层封装。
//
// 两件事绑在一个包里的理由（代码架构设计 §5.2）：BYOK 配置（Provider/Model）
// 和 Registry（配置 → 可调用的模型）读写同一批表、用同一份解密后的 Key，
// 拆成两个包只会多一层跳转。
//
// 【本包是唯一 import cloudwego/eino 的包】其余七个业务包完全不认识 Eino——
// 换编排框架不动业务代码。这条边界靠 eino.go 一个文件守住,port.go/usecase.go/
// postgres.go 里没有一处出现 eino 的类型。
package llm

import (
	"time"

	"github.com/google/uuid"
)

// Kind 区分一条 llm_models 记录是聊天模型还是 embedding 模型。
//
// 两者字段的意义不同（聊天模型要 ContextWindow/MaxOutputTokens,embedding
// 模型要 EmbeddingDim),但共用一张表——分成两张表的话,provider 下面
// "这个 provider 有哪些模型"这个最常见的查询要 UNION 两张表。
type Kind string

const (
	KindChat      Kind = "chat"
	KindEmbedding Kind = "embedding"
)

// Capabilities 记录一个模型声明支持的能力。
//
// 全部由用户在引导页手工勾选,不是探测出来的——OpenAI 兼容 API 不保证
// 能自动查询"这个模型支不支持 tool calling"(技术方案 §五)。
//
// 【ToolCalling 现在真的参与判断了（issue #38）】agent.Usecase 在创建和
// 运行两条路径上都会读它：模型没有声明这一位而 Agent 要用工具时，
// 请求在**发起运行前**被拒（见 internal/agent/usecase.go 的
// requireToolCapability）。引导页的复选框默认勾选，迁移 0007 也把升级前
// 就存在的 chat 行回填了——这两件事是同一件事的两半，因为升级之前平台在
// 行为上把每个 chat 模型都当成支持工具调用。
//
// 【其余几位仍然是展示位】Streaming / Reasoning 存进 llm_models.capabilities、
// 由 GET /providers 原样回显，没有任何生产路径读它们。
//
// 【已知的不精确，将来要动的话在这里】这一位把"用户没声明"和"用户明确
// 声明不支持"混成了一个 false。要分开就得把它变成三态（未声明 / 支持 /
// 不支持），那要改契约 schema、改 UI、还要改门控判据——超出 #38 的范围，
// 但方向是明确的。
type Capabilities struct {
	Chat        bool
	Streaming   bool
	ToolCalling bool
	Embedding   bool
	Reasoning   bool
}

// 五个能力名字,和数据库列 llm_models.capabilities（text[]）的取值一一对应。
// 只在这一处出现字符串常量，toSlice/capabilitiesFromSlice 都靠它,
// 拼错一个字母的话两个函数会一起错,不会出现"存的时候是 tool_calling,
// 读回来判断时写成了 toolcalling"这种两处各写一份导致的漂移。
const (
	capChat        = "chat"
	capStreaming   = "streaming"
	capToolCalling = "tool_calling"
	capEmbedding   = "embedding"
	capReasoning   = "reasoning"
)

// toSlice 把结构化的 Capabilities 转成数据库列要存的 text[]。
func (c Capabilities) toSlice() []string {
	out := make([]string, 0, 5)
	if c.Chat {
		out = append(out, capChat)
	}
	if c.Streaming {
		out = append(out, capStreaming)
	}
	if c.ToolCalling {
		out = append(out, capToolCalling)
	}
	if c.Embedding {
		out = append(out, capEmbedding)
	}
	if c.Reasoning {
		out = append(out, capReasoning)
	}
	return out
}

// capabilitiesFromSlice 是 toSlice 的逆运算,读数据库列时用。
//
// 认不出的字符串直接跳过,不报错——将来加新能力名字时,老版本代码
// 读到新数据不会崩,只是看不见那一位（前向兼容）。
func capabilitiesFromSlice(names []string) Capabilities {
	var c Capabilities
	for _, n := range names {
		switch n {
		case capChat:
			c.Chat = true
		case capStreaming:
			c.Streaming = true
		case capToolCalling:
			c.ToolCalling = true
		case capEmbedding:
			c.Embedding = true
		case capReasoning:
			c.Reasoning = true
		}
	}
	return c
}

// Provider 是一个 OpenAI 兼容端点：Base URL + 一把 Key。
//
// 【故意不放明文 Key,也不放密文】密文由 ConfigRepo 单独存取
// （GetProviderKey/UpsertProvider 的 keyCiphertext 参数),不放进这个结构体——
// 这样"这一条 Provider 长什么样"（供列表页展示）和"它的 Key 是什么"
// （只在真正需要调用模型时才解密）在类型层面就分开了,不会因为一次
// 手误的日志打印（比如 log.Printf("%+v", provider)）就把密文甚至明文吐进日志。
type Provider struct {
	ID        uuid.UUID
	BaseURL   string
	CreatedAt time.Time
}

// Model 挂在某个 Provider 下的一个模型（chat 或 embedding）。
//
// ModelID / ContextWindow / MaxOutputTokens / TokenizerType 全部手填——
// 这四个手填字段是 ctxmgr.Budget（M3）唯一可靠的输入来源。
//
// EmbeddingDim 只对 Kind == KindEmbedding 有意义,由 BootstrapEmbedding
// 探测得到,不是用户填的。chat 模型这一位恒为 0。
type Model struct {
	ID              uuid.UUID
	ProviderID      uuid.UUID
	ModelID         string
	Kind            Kind
	Capabilities    Capabilities
	ContextWindow   int
	MaxOutputTokens int
	TokenizerType   string
	EmbeddingDim    int
	CreatedAt       time.Time
}

// maxEmbeddingDim 是 HNSW 索引对 halfvec 的维度上限（技术方案 §六）。
// 超过这个数,pgvector 建不出索引,即使插入本身不报错——
// 所以校验要在探测完成、还没写进数据库之前就做,不能等到 CREATE INDEX 才发现。
const maxEmbeddingDim = 4000

// LatestByKind 从一批模型里选出"最近创建的那个属于 kind 的模型"。
//
// 【这是"当前生效模型"的唯一定义】v1.0 的引导页一次只配一套接入
// （技术方案 §4.1），没有"切换当前生效模型"的界面,所以"最近创建的"
// 和"唯一存在的"是同一件事。换模型时（技术方案 §六）会走 ALTER + 全量
// 重嵌入,新模型的 CreatedAt 天然比旧的晚,这个判据不用改就能跟着切换。
//
// 【为什么在 llm 包导出这个函数】retrieval（找 embedding 模型）与
// llm.Registry.ActiveModel / ActiveModelID（找 chat 模型）需要同一段
// "按 kind 筛、按时间取最新"的逻辑——写两遍容易在两处产生不一致的判断，
// 这里统一成一个函数。前端也复刻了同一段判据（web/src/lib/activeModel.ts），
// 改这里要一起改。
//
// 找不到时返回 nil，调用方决定报什么错误（不同调用方的错误上下文不同）。
func LatestByKind(models []*Model, kind Kind) *Model {
	var latest *Model
	for _, m := range models {
		if m.Kind != kind {
			continue
		}
		if latest == nil || m.CreatedAt.After(latest.CreatedAt) {
			latest = m
		}
	}
	return latest
}
