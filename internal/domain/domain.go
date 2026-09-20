// Package domain 存放跨模块共享的中性数据类型。
//
// 这个包只放数据，不放行为，也不 import 项目内任何其他包。
// 一旦它依赖了 platform 或 llm，所有消费者都会被拽着一起依赖那两个包。
// uuid 和 time 可以用，它们是纯类型，没有 IO。
package domain

import "github.com/google/uuid"

// Role 是消息在对话里的角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Chunk 是检索出来的一段文本。
//
// 它只定义这一次，所有包的 port 签名都用它——这样 *retrieval.Usecase
// 能自动满足 conversation.ChunkSearcher 和 knowledge.ChunkIndexer 两个 port，
// 不需要写适配器（Go 是结构化类型）。
//
// 【Filename 只在 Search 的返回值里有意义】IndexDocument 那条写路径
// 构造 Chunk 时不填它——存的是 document_id，不是文件名的冗余副本；
// Search 读出来时才 JOIN documents 表把文件名带出来，给 SSE 的
// citation 事件和前端展示用（domain.Citation 需要 Filename）。
type Chunk struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	Content    string
	Score      float64 // 余弦相似度，越大越相关
	Filename   string  // 只有 Search 返回的 Chunk 会填这个字段
}

// Memory 是长期记忆的一条。
type Memory struct {
	ID       uuid.UUID
	Scope    string
	Content  string
	Metadata map[string]any
}

// SearchRequest 是向量检索的请求。
//
// 多个消费者共用同一个形状，这是"数据用中性定义"那条规则的落地。
type SearchRequest struct {
	KnowledgeBaseID uuid.UUID
	Text            string
	TopK            int
}

// MemoryRequest 是长期记忆的检索请求。
type MemoryRequest struct {
	Text string
	K    int
}

// Citation 是随流下发给前端的引用源。
type Citation struct {
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
	Filename   string
	Snippet    string
	Score      float64
}

// TokenUsage 是一次模型调用的用量。
type TokenUsage struct {
	Prompt     int
	Completion int
}

// Source 标记一个上下文条目来自七类来源中的哪一类。
//
// 顺序即预算填充的优先级顺序，见技术方案 §7。
type Source string

const (
	SourceSystem     Source = "system"
	SourceUser       Source = "user"
	SourceAgentState Source = "agent_state"
	SourceRecent     Source = "recent"  // Conversation Memory：近期消息
	SourceSummary    Source = "summary" // Conversation Memory：会话摘要
	SourceChunk      Source = "chunk"   // RAG 检索片段
	SourceMemory     Source = "memory"  // 长期记忆
)

// MessageSnippet 是会话历史里一条消息的中性投影，供跨包消费方（比如
// agent 包的 conversation_search 工具）使用——完整的 conversation.Message
// 带 ID/Status/TokenUsage 这些该包内部才关心的字段，中性类型只留检索
// 场景真正需要的三个。
type MessageSnippet struct {
	Role       Role
	Content    string
	SequenceNo int64
}
