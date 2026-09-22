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

// SummaryCandidate 是"这个会话的摘要该更新了"这条判据，连同更新时需要的
// 三个输入（issue #118）。
//
// 【它不是 Summary 的一部分，也不是一条消息】判据（LatestSequenceNo 与
// CoveredUntilSequenceNo 的差越过门槛）和取数在一条 JOIN 里一次算完，
// 逐会话的那两三次往返就消失了（见 Repo.SummaryMaintenanceCandidates）。
// 把输入随判据一起带回来是同一件事的第二步：MaintainSummary 拼 prompt
// 需要的正是 PriorSummary，让它再查一次 GetSummary 是把同一条 SQL 发两遍。
type SummaryCandidate struct {
	ConversationID uuid.UUID

	// LatestSequenceNo 是该会话已定稿（status='completed'）消息里最大的
	// sequence_no，没有定稿消息的会话不会出现在候选集里。
	LatestSequenceNo int64

	// CoveredUntil 与 PriorSummary 来自 conversation_summaries 的同一行，
	// 没有摘要时是 0 和空串（GetSummary 的 ErrNotFound 那条路径在这里
	// 由 LEFT JOIN 的 COALESCE 表达）。
	CoveredUntil int64
	PriorSummary string
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

// 幂等键指向的资源类型。目前只有一种：那一轮生成的 assistant 消息。
//
// 单独定义成常量而不是散在 SQL 里，是为了命中时有东西可以判断
// 「这行记录说的是哪种资源」——将来如果别端点也用上这张表，
// 这里会出现第二种取值，而补发逻辑必须能区分。
const resourceTypeAssistantMessage = "assistant_message"

// IdempotencyRecord 是 idempotency_keys 表的一行，字段与
// migrations/0003_conversations.up.sql + 0006_idempotency_replay.up.sql
// 两张迁移合起来建出来的表一一对应。
type IdempotencyRecord struct {
	// Endpoint 是「哪个资源上的哪次操作」，形如
	// "POST /api/v1/conversations/<uuid>/messages"。
	//
	// 【为什么把会话 id 编进这一列】表的主键是 (endpoint, idempotency_key)，
	// 把会话 id 写进 endpoint 就等于把作用域收窄到会话——同一个键在另一个
	// 会话里不会命中。这样既不用改主键（pgerr.go 的 23505 分流依赖
	// idempotency_keys_pkey 这个约束名），也不用动表结构。
	//
	// 【全局作用域会出什么错】同一个键在会话 B 复用时命中会话 A 的行，
	// 客户端会拿到对不上的东西。
	Endpoint string
	Key      string

	ResourceType string
	ResourceID   uuid.UUID

	// FirstEventID 是预留这个键的那一刻、该会话已经发出的最后一个
	// event_id。补发从 event_id > FirstEventID 开始，正好是本轮产生的事件。
	FirstEventID int64

	// RequestFingerprint 是请求正文的 sha256 十六进制，用来识别
	// 「同一个键配了不同的正文」。
	RequestFingerprint string

	CreatedAt time.Time
}
