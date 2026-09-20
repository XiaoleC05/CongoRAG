// Package ctxmgr 是 Context Manager——技术方案称它为"本项目技术核心"。
//
// 【这个包里没有一个 pgx，没有一个 http】唯一的外部依赖是 llm.Tokenizer
// （一个接口，不是具体实现）。代码架构设计 §2.1 依赖表：`ctxmgr -> domain llm`，
// 连 platform 都不许依赖——纯计算是它能被"注入假 tokenizer 直接测"的
// 前提，如果这个包里藏着一次 DB 查询，那类测试根本没法写。
package ctxmgr

import (
	"errors"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

// Budget 是一次 Build 调用的预算参数。
//
// 【为什么 Tokenizer 在这里而不是 Usecase 的字段】token_cost 必须按
// "这次请求实际要用的模型"的 tokenizer_type 核算——同一个 Usecase 单例
// 要服务不同的会话（可能配了不同的模型），tokenizer 因此必须是每次调用
// 传入的，不能在构造时定死。调用方（conversation.Usecase.Send）负责
// 从 llm.ConfigRepo 查 tokenizer_type，再问 llm.Registry.Tokenizer(...)
// 拿到实例，见代码架构设计 §5.7。
type Budget struct {
	// Input = llm_models.context_window − max_output_tokens，
	// 调用方算好传进来，这个包不认识 llm_models 这张表。
	Input     int
	MaxOutput int
	Tokenizer llm.Tokenizer
}

// AgentState 是 Agent 执行到一半时的状态摘要（M4-A 才会真的非空）。
// 普通聊天场景下 Request.AgentState 是 nil。
type AgentState struct {
	CurrentStep int
	ToolCalls   []string
}

// Request 是拼装一次 Final Context 所需的全部原材料。
//
// 【七类来源，字段名对应技术方案 §七的七个来源】
// 这个包不会自己去数据库查 RecentMessages/Summary/Chunks/Memories——
// 全部由调用方查好、排好序再传进来，ctxmgr 只做"怎么组合、怎么裁剪"
// 这一层纯计算。
type Request struct {
	SystemPrompt string
	UserInput    string
	AgentState   *AgentState

	// RecentMessages 已经按"旧 → 新"排好（调用方查的时候排的），
	// 压缩时从旧的那一端开始丢。每条消息的 Role 必须是真的说话人——
	// 这个包原样把它带到 Item.Role，再由调用方转回线路消息，
	// 中途没有任何一层有资格替它猜一个角色。
	RecentMessages []llm.Message
	// Summary 是会话摘要，"刚才聊了什么"。
	Summary string
	// Chunks 已经按 Score 从高到低排好（retrieval.Usecase.Search 保证）。
	Chunks []domain.Chunk
	// Memories 已经按相关度从高到低排好。
	Memories []domain.Memory

	Budget Budget
}

// Item 是拼进最终上下文的一个条目。
type Item struct {
	Source  domain.Source
	Content string
	// Role 是这条条目在对话里的说话人。只有 SourceRecent 有真实取值
	// （从 Request.RecentMessages 的 llm.Message.Role 带过来），其余来源
	// 留空。它必须存在：FinalContext 的唯一消费方要把每条 Recent 条目
	// 转回一条线路消息，没有这个字段它只能自己编一个角色，而编出来的
	// 角色会把助手的回答说成是用户说的。
	//
	// 【角色原样穿过这一层，包括 tool】不在这里过滤或替换任何角色。
	// tool 角色的消息现在不会出现在 messages 表里（Agent 的执行轨迹
	// 存在 agent_steps，不写 messages），真要重放时 Item 还得带上
	// ToolCalls/ToolCallID，那是 M4-A 接 Agent 时才需要决定的事——
	// 现在的契约是"生产者填什么就带什么"，不是"这个包挑一个安全的角色"。
	Role      domain.Role
	TokenCost int
	// Relevance 只在同一个 Source 内部可比——RAG 的相似度分和 Memory 的
	// 检索分是两个不同的量纲，不能跨 Source 比大小。
	Relevance float64
	// Compressible 标记这一条属不属于"可压缩区"（技术方案 §七的分区）：
	// Recent Messages 和 Summary 是 true，其余（System/User/AgentState/
	// Chunk/Memory）是 false——不是"不重要"，是"压缩这个动作对它没意义"
	//（System Prompt 没法被摘要，RAG 片段的处理方式是整条删掉而不是压缩）。
	Compressible bool
}

// FinalContext 是 Build 的产物：一份已经确定放得下预算的上下文。
type FinalContext struct {
	Items     []Item
	TokenCost int
	// Citations 是从存活下来的 Chunk 转换来的——调用方（conversation.Usecase）
	// 把它们随流下发给前端，SSE 的 citation 事件用它们（docs/sse-protocol.md）。
	Citations []domain.Citation
}

// ErrOverflow 是预算阶梯走到底仍然装不下时返回的错误。
//
// 【为什么不静默截断】技术方案 §七明确要求"直接返回 context overflow
// 错误，不使用静默截断"——截断可能删掉一句话中间，产出的上下文送进
// 模型会产生误导性的结果，比明确报错更糟。
var ErrOverflow = errors.New("context overflow")
