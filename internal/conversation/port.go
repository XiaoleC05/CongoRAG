package conversation

import (
	"context"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Repo 是会话/消息/事件的存取接口。
//
// 事件的持久化和发号也放在这个接口里，不单独拆一个 EventRepo——
// 它们和会话/消息共用同一批表（conversation_events/conversation_counters
// 都用 conversation_id 做外键），拆开只会多一层参数传递,没有实际的
// 解耦收益（对比 §5.6 长期记忆合并进本包的理由：同一张表族的读写
// 天然属于同一个存取接口）。
type Repo interface {
	CreateConversation(ctx context.Context, q platform.Querier, c *Conversation) error

	// GetConversation 找不到时返回 platform.ErrNotFound。
	// Send 用它取出 KnowledgeBaseID，决定这次要不要做检索增强。
	GetConversation(ctx context.Context, q platform.Querier, id uuid.UUID) (*Conversation, error)

	AppendMessage(ctx context.Context, q platform.Querier, m *Message) error

	// NextSequenceNo 返回一个会话下一个可用的消息序号（当前最大值 + 1，
	// 空会话返回 1）。【只能在 LockedWriter.WithConversationLock 内调用】——
	// advisory lock 保证了这里不会有并发的另一次调用同时读到相同的
	// "当前最大值"，脱离锁调用这个方法不提供任何顺序保证。
	NextSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error)

	// RecentMessages 按 sequence_no 倒序取最近 limit 条，且只取
	// sequence_no > afterSequenceNo 的部分——afterSequenceNo 传当前摘要的
	// CoveredUntilSequenceNo（没有摘要就传 0），这样已经被摘要吸收的历史
	// 不会和摘要重复出现在同一次 ctxmgr.Request 里（memory.go 顶部注释）。
	// 调用方负责按需要的顺序重新排列——这里只管"取最近的"。
	RecentMessages(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error)

	// ListMessages 按 sequence_no 正序返回一个会话的全部消息，
	// 供 GET /conversations/{id}/messages 使用（前端刷新页面重载历史）。
	ListMessages(ctx context.Context, q platform.Querier, convID uuid.UUID) ([]*Message, error)

	// MessagesAfter 按 sequence_no 正序取 sequence_no > afterSequenceNo 的
	// 消息，最多 limit 条——供 MaintainSummary 增量滚动摘要用。正序（旧到新）
	// 是为了压缩时保持叙事顺序；有 limit 是为了单次调用的查询成本有上界，
	// 一轮处理不完的部分,covered_until_sequence_no 只会前进到这批处理到的
	// 位置，剩下的留给下一轮周期任务，不会永久遗漏（memory.go 的说明）。
	MessagesAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error)

	// LatestSequenceNo 返回一个会话当前用到的最大 sequence_no，没有消息
	// 时返回 0。
	//
	// 【和 NextSequenceNo 的区别】NextSequenceNo 的文档明确要求只能在
	// WithConversationLock 内调用——它服务的是"分配一个新号"这种需要严格
	// 顺序保证的场景。这里只是读一个近似的进度水位（MaintainSummary
	// 判断"新消息攒够了没有"），不需要那个保证，脱离锁调用是安全的、
	// 故意设计成这样的，不是疏忽。
	LatestSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error)

	// ListConversationIDs 返回全部会话的 id，不分页、不过滤。
	//
	// 【为什么不在 SQL 里过滤"哪些会话需要维护"】方案的部署边界是本地
	// 单机应用（开发文档 §1），会话数量在这个量级下全表扫描后在 Go 里
	// 判断每个会话是否需要处理，比写一条聚合子查询更直接——和
	// knowledge.Usecase.StartReconciler 的做法一致（先拿到全集，再在 Go
	// 里筛出真正要处理的那些）。
	ListConversationIDs(ctx context.Context, q platform.Querier) ([]uuid.UUID, error)

	// UpdateMessageContent 是流式过程中按批次落库的那次 UPDATE——
	// 每 500ms 或每 N token 调一次（技术方案 §三），content 是当前已经
	// 生成的全部内容（不是增量），status 通常是 MsgStreaming 或终态。
	UpdateMessageContent(ctx context.Context, q platform.Querier, id uuid.UUID, content string, status MessageStatus) error

	// NextEventID 在给定会话的计数器上原子地取下一个号并返回。
	// 必须和调用方那一次的消息写入在同一个事务里调用——
	// docs/sse-protocol.md「事件与续传」一节解释了为什么。
	NextEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error)

	// AppendEvent 持久化一条事件，和 NextEventID 通常在同一个事务里，
	// 紧跟在分配到号码之后。
	AppendEvent(ctx context.Context, q platform.Querier, convID uuid.UUID, ev Event) error

	// EventsAfter 返回 event_id > afterEventID 的全部事件，按 event_id
	// 升序——断线续传用它把错过的部分补发给客户端。
	EventsAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterEventID int64) ([]Event, error)

	// GetSummary 取一个会话当前的摘要。没有摘要（从没压缩过）时返回
	// platform.ErrNotFound——这不是异常情况，是"这个会话还短，压缩阶梯
	// 从没触发过第 2 步"的正常状态，调用方（RetrieveSummary）把它
	// 翻译成"传一个空 Summary 给 ctxmgr"，不是把错误继续往上抛。
	GetSummary(ctx context.Context, q platform.Querier, convID uuid.UUID) (*Summary, error)

	// UpsertSummary 整体替换一个会话的摘要——见 Summary 类型定义的注释,
	// 不追加新行,ON CONFLICT 更新已有的那一行。
	UpsertSummary(ctx context.Context, q platform.Querier, s *Summary) error
}

// LockedWriter 把 PG advisory lock 和 sequence_no 分配封进一个 port。
//
// 【业务代码永远不该直接写 pg_advisory_xact_lock】代码架构设计 §5.8
// 原话——这个接口存在的意义就是不让那句 SQL 出现在 usecase.go 里。
type LockedWriter interface {
	// WithConversationLock 在会话级咨询锁内执行 fn。fn 里可以安全地
	// 读 max(sequence_no) 或调用 Repo 的写方法而不用担心并发写入
	// 导致序号冲突——同一个会话的两次 WithConversationLock 调用
	// 会被数据库串行化。
	WithConversationLock(ctx context.Context, convID uuid.UUID, fn func(q platform.Querier) error) error
}

// ChunkSearcher 是本包声明的唯一跨包 port（规则 A）。
// 由 retrieval.Usecase 实现——签名用 domain.SearchRequest/domain.Chunk
// 这两个中性类型，所以不需要写适配器（规则 B）。
type ChunkSearcher interface {
	Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error)
}

// MemoryRepo 是长期记忆表（memories，0001 就建好了）的存取接口。
//
// 【为什么在这个包里,不是独立的 port】代码架构设计 §5.6：长期记忆的读写
// 天然属于 conversation 包内部——上一版把 memory 独立成包时需要
// MemoryRetriever / PreferenceExtractor 两个跨包 port，合并进来之后
// 两者都消失了，这里直接是包内的一个 interface，供 usecase.go 和
// postgres.go 之间解耦（方便测试用假实现,不是为了跨包给别人实现）。
type MemoryRepo interface {
	Insert(ctx context.Context, q platform.Querier, m *domain.Memory, vec []float32, embeddingModel string) error

	// SearchByRelevance 按余弦距离找出 Top-K 最相关的记忆条目，
	// 结果按相关度从高到低排好——和 retrieval.PgRepo.Search 同样的调用
	// 约定（vec 由调用方 embed 好再传进来,这里不认识 llm.Registry）。
	SearchByRelevance(ctx context.Context, q platform.Querier, req domain.MemoryRequest, vec []float32, embeddingModel string) ([]domain.Memory, error)
}

// EventSink 是 SSE 的出口。
//
// 【它是 Send 方法的参数，不是 Usecase 的字段】如果做成单例字段，
// 两个并发的聊天请求会共用一个 sink，后连上的那个会劫持先连上的那一个
// 的 SSE 流——代码架构设计 §5.8 的原话。
type EventSink interface {
	Emit(ev Event) error
	Flush() error
	// Done 在客户端断开连接时关闭，Send 的流式循环监听它来判断
	// 要不要继续往一个没人听的连接写数据。
	Done() <-chan struct{}
}
