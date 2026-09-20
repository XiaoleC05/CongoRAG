// Package conversation 拥有会话、消息、长期记忆和 SSE 事件出口。
//
// 长期记忆放在这个包（不是独立包）的理由见代码架构设计 §5.6：
// 上一版 memory 是独立包时需要 MemoryRetriever / PreferenceExtractor
// 两个 port；合并进 conversation 后，这两个 port 直接消失——记忆
// 现在是包内的事，不需要跨包声明"我需要什么"。
package conversation

import (
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// MessageStatus 是一条消息的生成状态。
type MessageStatus string

const (
	MsgStreaming MessageStatus = "streaming"
	MsgCompleted MessageStatus = "completed"
	MsgFailed    MessageStatus = "failed"
)

// Message 是持久化实体，字段与 migrations/0003_conversations.up.sql 的
// messages 表一一对应。
//
// 【和 llm.Message 不是一回事】那是线路层类型（发给模型的一条消息，
// 没有 id/sequence_no/status 这些数据库概念），这里是数据库里真实的一行。
type Message struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	Role           domain.Role
	Content        string
	Status         MessageStatus
	SequenceNo     int64
	TokenUsage     *domain.TokenUsage
	CreatedAt      time.Time
}

// Conversation 是一个会话。
//
// 【KnowledgeBaseID 是可空的，方案文档没有定义这个关联，这是实现时
// 补上的决策】一个会话可选关联一个知识库：关联了，Send 检索增强时
// 用它做检索范围；nil 就是一次不带 RAG 的纯对话。删除知识库不会删除
// 引用过它的会话（migrations/0003 用 ON DELETE SET NULL，不是 CASCADE）。
type Conversation struct {
	ID              uuid.UUID
	Title           string
	KnowledgeBaseID *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Summary 是一个会话的滚动摘要，字段对应
// migrations/0004_conversation_summaries.up.sql。
//
// 【一个会话最多一行，不是历史版本序列】MaintainSummary 每次触发都整体
// 替换 Summary 和 CoveredUntilSequenceNo，不追加新行——旧摘要的价值
// 已经被"合并进新摘要"这个动作吸收了，单独保留没有意义。
type Summary struct {
	ConversationID         uuid.UUID
	Summary                string
	CoveredUntilSequenceNo int64
	UpdatedAt              time.Time
}

// Event 是随流下发、同时持久化的一条 SSE 事件，字段对应
// docs/sse-protocol.md 定义的线路格式。
//
// Payload 是已经序列化好的 JSON——本包不需要认识每种 event 类型的具体
// 结构（token/citation/tool_call/...），那些形状由调用方
// （conversation.Usecase.Send）在构造 Event 时决定，本包只管存和转发。
type Event struct {
	ID      int64
	Type    string
	Payload []byte
}
