package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// Tool 是一个可以被 Agent 调用的工具。
//
// 【为什么不是 Eino 的 tool.BaseTool】那个接口服务的是"喂给 Eino 的
// ChatModelAgent"这一件事,不携带 Metadata()（副作用等级/重试策略）——
// 这些信息是本项目自己的 M4-C 恢复逻辑需要的,Eino 不认识。
// eino_adk.go 里的适配器把这个接口包成 Eino 认识的形状,反过来的转换
// （Eino → 本接口）从不需要发生。
type Tool interface {
	Name() string
	Description() string
	Metadata() Metadata
	Spec() ToolSpec
	Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// Registry 是工具的注册表——技术方案 §4.1 的注册表模式,工具增删不改
// Agent 主循环。
type Registry interface {
	Register(t Tool)
	Get(name string) (Tool, error)
	List() []Tool
}

// Repo 是 agents/agent_runs/agent_run_steps 三张表的存取接口。
type Repo interface {
	CreateAgent(ctx context.Context, q platform.Querier, a *Agent) error
	GetAgent(ctx context.Context, q platform.Querier, id uuid.UUID) (*Agent, error)
	ListAgents(ctx context.Context, q platform.Querier) ([]*Agent, error)

	// UpdateAgent 改一个已存在的 Agent 的可改字段（名称 / 描述 / system
	// prompt / 工具集）与 updated_at。找不到返回 ErrNotFound。
	//
	// 【改不动的那几列】id / created_at 不在 SET 列表里——它们不是"配置"。
	UpdateAgent(ctx context.Context, q platform.Querier, a *Agent) error

	InsertRun(ctx context.Context, q platform.Querier, r *Run) error
	GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error)
	// ListRunsByAgent 按 created_at 倒序取一个 Agent 的历史运行，最多 limit 条。
	//
	// 【keyset 分页的理由和文档列表一样】agent_runs 没有删除路径，历史会
	// 一直涨；而前端每 1.5 秒轮询一次这个列表。OFFSET 分页在两次轮询之间
	// 有新运行插到头部时会让上一页的尾部重复出现，而游标不会。
	//
	// cur 为 nil 表示第一页；返回的 bool 是 hasMore（还有没有下一页）。
	ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID, cur *platform.ListCursor, limit int) ([]*Run, bool, error)

	// UpdateRunStatus 用 CAS（WHERE status = from）——和
	// knowledge.PgDocRepo.UpdateStatus 同样的模式,防止并发写入把一个
	// 已经终态的 Run 又改回中间态。
	UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error

	InsertStep(ctx context.Context, q platform.Querier, s *Step) error
	StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*Step, error)

	// MaxStepSeq 返回这条 run 已经用过的最大 seq，没有步骤时返回 0。
	//
	// 【为什么它是个独立方法，而不是让调用方从 StepsByRun 里自己算】
	// Step 的编号必须**接着已有的往下排**（恢复时尤其重要：从 1 重来会撞
	// UNIQUE (run_id, seq)，而那个失败是静默的）。让唯一需要这个数的地方
	// 直接从数据里读，比"算好之后一路当参数传下去"少三个可以漏的环节——
	// 而漏掉的表现恰好是静默的。见 usecase.go 里 consumeEvents 的注释。
	MaxStepSeq(ctx context.Context, q platform.Querier, runID uuid.UUID) (int, error)

	// UpdateStep 更新一行**已经存在**的 Step。
	//
	// 【为什么需要它：工具步骤要在调用之前就落库（issue #64 / ADR-007）】
	// 崩溃表的前两行共用同一个现场——「一行 running 的 step，没有效果账本」。
	// 要做到这一点，step 行必须在工具**开始执行之前**就存在，否则崩溃后
	// 现场里根本没有这一行，业务层连"这一步走到了哪"都不知道。
	// 返回之后再用这个方法补上结果、延迟与终态。
	//
	// 【只更新会变的那几列】seq / run_id / type / tool_name / tool_args /
	// created_at 是一次写入就定死的，改它们意味着"这根本是另一步"，
	// 不该通过 UpdateStep 发生。
	UpdateStep(ctx context.Context, q platform.Querier, s *Step) error

	// ── run 维度的事件流（issue #54）──────────────────────────
	//
	// 与会话维度的三个同名方法（conversation.Repo 的 NextEventID /
	// AppendEvent / EventsAfter）是刻意分开的第二份：两张表并存、各自编号，
	// 理由见 migrations/0009_run_events.up.sql。
	//
	// 【NextRunEventID 必须和 AppendRunEvent 在同一个事务里调用】
	// 理由与 conversation 那一侧相同：计数器递增和事件写入必须原子，
	// 否则会出现"号分配了但事件没落库"的空洞——而 run 维度没有会话那样
	// 复杂的多写者场景，一次 InTx 就够了。
	NextRunEventID(ctx context.Context, q platform.Querier, runID uuid.UUID) (int64, error)
	AppendRunEvent(ctx context.Context, q platform.Querier, runID uuid.UUID, ev RunEvent) error

	// RunEventsAfter 返回 event_id > afterEventID 的全部事件，按 event_id
	// 升序——run 级断线重订阅（GET /agents/runs/{runId}/events）与幂等重放
	// 都用它。afterEventID 传 0 表示"从头补发整个 run"。
	RunEventsAfter(ctx context.Context, q platform.Querier, runID uuid.UUID, afterEventID int64) ([]RunEvent, error)

	// ── 工具效果账本（issue #63）──────────────────────────────
	//
	// 【RecordToolEffect 必须用独立事务提交，不能和步骤的写入共用一个事务】
	// 项目文档 §9.5 特意点了这一条：混在可能回滚的步骤事务里的话，崩溃时
	// 账本会跟着一起回滚，裁判就没了——恢复路径会以为这个副作用没发生过。
	// "独立事务"在这里的具体含义：调它时传的是 *pgxpool.Pool（自动提交），
	// 不是某个 InTx 里的 tx。
	//
	// 冲突（23505）会被 platform.WrapPgErr 分流成 ErrToolEffectApplied，
	// 不是 ErrDuplicateKey——见 0010 迁移与 sentinel.go 的注释。
	RecordToolEffect(ctx context.Context, q platform.Querier, stepID uuid.UUID, effectKey string) error

	// ToolEffectApplied 查这一步的这个效果是不是已经记过账了。
	//
	// 恢复路径在重放工具步骤**之前**调它：返回 true 就跳过重放
	// （判读方向见 0010 迁移的注释，别搞反）。
	ToolEffectApplied(ctx context.Context, q platform.Querier, stepID uuid.UUID, effectKey string) (bool, error)

	// ── 崩溃扫描与 checkpoint 回收 ────────────────────────────
	//
	// InterruptRunningRuns 把库里所有 running 的 run 推进 interrupted，
	// 并把它那些没跑完的 step 一起标 interrupted（ADR-007 的崩溃表前两行
	// 落到数据上的那一步）。返回受影响的 run id 供调用方记日志。
	//
	// 【只在进程启动时调用】run 的执行生命周期绑在一条活的 SSE 请求上
	// （StartAgentRun handler 跑在 api 进程里），所以"进程刚起来"这一刻
	// 不可能有真正在飞的 run——见 ADR-007 决策二的理由。
	InterruptRunningRuns(ctx context.Context, q platform.Querier) ([]uuid.UUID, error)

	// ClearTerminalRunCheckpoints 回收已经进入终态、且更新时间早于
	// olderThan 的 run 的 checkpoint（把 state_snapshot 置空），返回回收行数。
	//
	// 【为什么是置空 state_snapshot 而不是删 run 行】run 的历史本身有价值
	// （轨迹页要看、用量要统计）；膨胀的是快照这一列。issue #67 要的是
	// "这个纯增长的表不要一直堆积"，置空正好解决它而不损失别的。
	ClearTerminalRunCheckpoints(ctx context.Context, q platform.Querier, olderThan time.Time) (int64, error)

	// ListToolCatalog 读 tools 表的种子数据,给创建 Agent 的表单渲染
	// 勾选列表用——它是纯只读目录查询,和 Registry（真正能被调用的工具
	// 实现）是两件独立的事：目录可能列出比 Registry 里实际注册的更多
	// 或更少的名字（比如种子数据加了新工具但代码还没实现完），两者故意
	// 不强制同步,由部署时的纪律保证一致,不是代码层面的约束。
	ListToolCatalog(ctx context.Context, q platform.Querier) ([]ToolCatalogEntry, error)
}

// ToolCatalogEntry 是 tools 表一行的只读投影。
type ToolCatalogEntry struct {
	Name            string
	Description     string
	SideEffectLevel SideEffectLevel
}

// KnowledgeSearcher 是 tool_knowledge_search.go 声明的唯一跨包 port
// （规则 A）。由 retrieval.Usecase 实现——签名用 domain.SearchRequest/
// domain.Chunk 这两个中性类型，自动满足，不需要适配器（规则 B）。
// 和 conversation.ChunkSearcher 是同一个方法签名，两个包各自declare
// 自己的 port 而不是共用一个 interface 类型，是"消费方声明"这条规则的
// 直接体现——即使碰巧长得一样。
type KnowledgeSearcher interface {
	Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error)
}

// ConversationSearcher 是 tool_conversation_search.go 声明的唯一跨包
// port。由 conversation.Usecase.SearchMessages 实现。
type ConversationSearcher interface {
	SearchMessages(ctx context.Context, convID uuid.UUID, query string) ([]domain.MessageSnippet, error)
}

// CheckpointStore 落 Step 边界的快照——这一轮只有 Save 真正被
// Usecase.Start 调用（每步执行完存一次 current_step + state_snapshot，
// 让 GET /agents/{id}/runs/{runId} 在执行中途也能看到准确进度）。
//
// 【Load 这一轮没有调用点,但不是半成品】它是一个完整、正确、可独立
// 测试的只读方法——只是 M4-C 的 Resume 入口（会调用 Load 决定从哪个
// Step 继续）现在还不存在。等 M4-C 开工时,Resume 直接调用这个已经在
// 这里的方法,不需要回头改 CheckpointStore 的接口形状。
type CheckpointStore interface {
	Save(ctx context.Context, q platform.Querier, runID uuid.UUID, step int, output string, snap json.RawMessage) error
	Load(ctx context.Context, q platform.Querier, runID uuid.UUID) (*Run, error)
}

// IdempotencyStore 是幂等键的存取接口（issue #56 / ADR-008）。
//
// 【为什么 agent 包自己声明一份，而不是复用 conversation.Repo】
// 依赖规则是"消费方声明自己需要什么"：conversation.Repo 是会话/消息/摘要
// /事件的一大坨方法，agent 只需要其中两个，把它整个拖进来会让 agent 包
// 凭空依赖一整套它不用的读写能力。底层是同一张表（idempotency_keys），
// 但**约束名的分流是共享的**——两边都过 platform.WrapPgErr，
// 所以"幂等命中不是 409"这条性质只有一个实现点。
//
// 【作用域 = (endpoint, key)】endpoint 字符串里编着 Agent id
// （"POST /api/v1/agents/<uuid>/runs"），所以同一个键在另一个 Agent 上
// 不会命中。这与会话那一侧把会话 id 编进 endpoint 是同一个做法。
type IdempotencyStore interface {
	// ReserveIdempotencyKey 抢一个键，成功即代表「这次请求由我执行」。
	// 撞上已存在的键返回 ErrIdempotentHit（不是 ErrDuplicateKey）——
	// 分流在 platform.WrapPgErr 里按约束名做。
	//
	// 顺带清掉 created_at < expiredBefore 的行（过期键可以重新执行，
	// 理由见 platform.IdempotencyKeyTTL 的注释）。
	ReserveIdempotencyKey(ctx context.Context, q platform.Querier, rec *IdempotencyRecord, expiredBefore time.Time) error

	// LookupIdempotencyKey 按 (endpoint, key) 取回一条记录，找不到返回
	// ErrNotFound。
	LookupIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) (*IdempotencyRecord, error)
}

// IdempotencyRecord 是 idempotency_keys 表一行的投影。
//
// 【为什么和 conversation.IdempotencyRecord 长得一样还要各写一份】
// 两个包不允许互相依赖对方的私有实现细节——这是本仓库一贯的取舍
// （tokenUsageJSON 在 conversation 与 agent 里各有一份副本，注释里
// 写明了同样的理由）。差异只会在真正需要时出现，而那时分开的类型
// 正好是可以各自演进的前提。
type IdempotencyRecord struct {
	Endpoint      string
	Key           string
	ResourceType  string
	ResourceID    uuid.UUID
	FirstEventID  int64
	// RequestFingerprint 是请求正文的 sha256。同一个键配不同的正文时要
	// 报错，而不是把上一次的运行重放给用户——否则用户新写的那句输入
	// 既没执行、也不会报错，界面上只是旧结果又出现了一遍。
	RequestFingerprint string
	CreatedAt          time.Time
}
