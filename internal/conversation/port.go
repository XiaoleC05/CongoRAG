package conversation

import (
	"context"
	"time"

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

	// RecentMessages 取最近 limit 条，并且**按 sequence_no 正序（旧 → 新）
	// 返回**——顺序由这里负责，调用方不需要也不应该再重排一次：
	// ctxmgr.Request.RecentMessages 的契约就是"旧 → 新"，唯一真正知道
	// 时间方向的查询层如果返回倒序，下游每一层都只能靠猜。
	//
	// 只取 sequence_no > afterSequenceNo 的部分——afterSequenceNo 传当前
	// 摘要的 CoveredUntilSequenceNo（没有摘要就传 0），这样已经被摘要吸收
	// 的历史不会和摘要重复出现在同一次 ctxmgr.Request 里（memory.go 顶部注释）。
	//
	// beforeSequenceNo 是排他上界，传 0 表示无上界。send 传的是本轮
	// 用户消息分配到的 sequence_no：调用方在加载历史之前已经写下了本轮
	// 的用户行和 assistant 占位行，不挡住它们的话，当前提问会被当成
	// 历史上的一条再回放一遍（问题被送两遍、空占位行也跟着进 prompt）。
	RecentMessages(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo, beforeSequenceNo int64, limit int) ([]*Message, error)

	// ListMessagesPage 取一个会话里「比 beforeSequenceNo 更早」的最新 limit 条
	// 消息，按 sequence_no 正序返回，供 GET /conversations/{id}/messages 使用
	// （前端刷新页面重载历史）。
	//
	// beforeSequenceNo 传 0 表示没有上界（第一页给最新的 limit 条）。
	// 返回的 bool 是 hasMore：还有没有更早的消息。
	//
	// 【它和 RecentMessages 不是一回事，不要合并】RecentMessages 服务的是
	// 喂给模型的上下文窗口（自带 limit 与摘要水位线），这个服务的是前端
	// 列表。两者的排序方向、过滤条件、调用点都不同，改一个不要顺手改另一个。
	ListMessagesPage(ctx context.Context, q platform.Querier, convID uuid.UUID, beforeSequenceNo int64, limit int) ([]*Message, bool, error)

	// MessagesAfter 按 sequence_no 正序取 sequence_no > afterSequenceNo 的
	// 消息，最多 limit 条——供 MaintainSummary 增量滚动摘要用。正序（旧到新）
	// 是为了压缩时保持叙事顺序；有 limit 是为了单次调用的查询成本有上界，
	// 一轮处理不完的部分,covered_until_sequence_no 只会前进到这批处理到的
	// 位置，剩下的留给下一轮周期任务，不会永久遗漏（memory.go 的说明）。
	//
	// 它不按状态过滤：这一批里可能夹着未定稿的行（本轮正在生成的那条
	// status='streaming' 占位行，或生成中断时留下的、正文已经冻结的
	// status='failed' 那条——客户端断开、上游中途报错都会留下它）。
	// 调用方只能把水位线推进到批次里最后一条已定稿的消息为止——理由见
	// MaintainSummary 里那段截断的注释。
	MessagesAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error)

	// LatestSequenceNo 返回一个会话已定稿（status='completed'）的消息里
	// 最大的 sequence_no，没有定稿消息时返回 0。
	//
	// 【为什么不是"所有消息里的最大 sequence_no"】它是"进度水位"：门控
	// 判断的是"攒够了多少条可以压缩的消息"。生成中的占位行正文还没定稿，
	// 把它算进来会让摘要任务在一个空行上被触发，甚至让水位线跨过它——
	// 那条回答之后既不在摘要里也不在 RecentMessages 里（postgres.go 的实现
	// 注释详细解释了这条链）。
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

	// ReserveIdempotencyKey 抢一个幂等键，成功即代表「这次请求由我执行」。
	//
	// 【只能返回 ErrIdempotentHit，绝不能返回 ErrDuplicateKey】两者在 SQL 层
	// 都是 23505，区别只在约束名。撞上主键说明这个键已经被用掉了，是正常的
	// 重放场景；映射成 ErrDuplicateKey 的话调用方会当成 409 冲突报给用户，
	// 而正确答案是「把上一轮的结果补发给他」——这正是 pgerr.go 里
	// constraintIdempotencyKey 那个常量存在的理由。
	//
	// 【expiredBefore 由调用方算好传进来】保留窗口是业务参数，不在 SQL 里
	// 写 now() - interval——本包所有时间戳都由 Go 侧生成后传入，这样测试能
	// 控制时间，也避免同一件事在后端和数据库里各写一份。
	//
	// 【必须在同一事务里既清理过期键又 INSERT】否则两次并发预留可能都先
	// 清理、再各自插入，中间出现一个谁也没删掉的窗口。
	ReserveIdempotencyKey(ctx context.Context, q platform.Querier, rec *IdempotencyRecord, expiredBefore time.Time) error

	// LookupIdempotencyKey 按 (endpoint, key) 取回一条记录，找不到返回
	// platform.ErrNotFound。
	//
	// 【为什么预留时不能顺手把整行读回来】预留只关心「抢到了没有」；
	// 而真正的补发发生在 Send 的另一条分支上，那时才需要 ResourceID 和
	// FirstEventID。分成两个方法让两条路径各自只拿自己需要的东西。
	LookupIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) (*IdempotencyRecord, error)

	// LastIssuedEventID 返回一个会话已经发出的最后一个 event_id，
	// 该会话还没有任何事件时返回 0。
	//
	// 【这一列的真实语义是「最后发出的号」，不是「下一个要发的号」】
	// conversation_counters.next_event_id 的名字与 0003 里那句注释都容易让人
	// 以为是后者，但 NextEventID 的 SQL 是 INSERT ... VALUES ($1, 1) ...
	// RETURNING next_event_id——第一次返回 1，之后 2、3……所以它存的是
	// 已经发出去的那个号。幂等键预留要的正是这个值（本轮事件的严格下界），
	// 所以这里必须按真实语义命名与注释，不要再沿用一个反话。
	LastIssuedEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error)

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

	// ListNeedingEmbedding 取一批「向量不是当前生效模型生成的」记忆
	// （包括向量为 NULL 的），按创建时间升序，最多 limit 条。
	//
	// 【issue #39：为什么记忆的重建是一条独立的路径】它的向量由周期任务
	// 产生，重嵌入的成本与时机和文档不一样：换 embedding 模型时它只是被
	// 清空（清空与文档共用同一段 SQL，在 api 的切换事务里完成），重建交给
	// worker 的下一次周期 tick 自愈。两个进程之间因此不需要任何协调。
	ListNeedingEmbedding(ctx context.Context, q platform.Querier, activeModel string, limit int) ([]*domain.Memory, error)

	// UpdateEmbedding 把一条记忆的向量与模型标记写回去。
	UpdateEmbedding(ctx context.Context, q platform.Querier, id uuid.UUID, vec []float32, embeddingModel string) error
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
