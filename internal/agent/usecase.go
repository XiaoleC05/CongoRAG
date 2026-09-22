package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// maxNameLen/maxDescriptionLen 和 knowledge.maxNameLen 同样的理由：
// 按字符数算，避免响应体随语言膨胀。
const (
	maxNameLen        = 200
	maxDescriptionLen = 2000
	maxInstructionLen = 4000
)

// finalizeTimeout 是"脱离请求生命周期"的收尾写入的上限——Run 的终结状态、
// 客户端断开那一刻补写的 Step/checkpoint。和 knowledge.statusWriteTimeout
// 同一个理由：这些写入必须活过请求，但也不能无界地活。
const finalizeTimeout = 5 * time.Second

// detachedWriteCtx 从请求 ctx 派生一个有界的、不受取消影响的写入 ctx。
//
// 【为什么不能用请求 ctx】SSE 端点的 ctx 就是 net/http 的请求 ctx
// （apps/api/internal/api/server.go 把它一路传进来），客户端断开时它
// 立刻被取消；pgx 拿着一个已取消的 ctx 连 pgxpool.Acquire 都过不去，
// UPDATE/INSERT 一条都不会执行。收尾恰恰是最需要写成功的时候——失败
// 状态写不下去，run 行就永久停在 'running'（issue #14）；Step 写不下去，
// 轨迹就断在断开的那一刻。摘掉取消信号（WithoutCancel）保留请求值，
// 另加超时——脱开而不是彻底无界，和 knowledge.markFailed 同一个写法。
func detachedWriteCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
}

// Usecase 是 Agent 的业务层。
//
// 【没有 convo *conversation.Usecase 字段】方案 §5.10 的原稿带这个
// 字段，但这一轮唯一用到 conversation 包的地方是 Start 方法参数的
// EventSink 类型——复用它，而不是本包另造一个结构相同但类型不同的
// 事件接口，是因为 Go 的具名类型不支持"字段结构一样就自动兼容",
// apps/api/internal/api/sse.go 的 sseSink 只能实现一个具体的 EventSink
// 类型。代码架构设计标注"agent → conversation 单向"允许这个依赖方向,
// 这里只是没有多存一个从没被调用过的 *conversation.Usecase 实例字段。
type Usecase struct {
	repo     Repo
	cp       CheckpointStore
	tools    Registry
	registry llm.Registry
	idem     IdempotencyStore
	txm      platform.TxManager
	logger   *slog.Logger
	db       platform.Querier

	// running 是"本进程里正在执行的 run"，用于取消（issue #55）。
	//
	// 【为什么取消不能只改数据库】取消要真的把那一次模型/工具调用拆掉，
	// 而拆它的唯一手段是 cancel 它那条 context——context 只活在执行它的
	// 那个 goroutine 里，数据库里没有它。这张表就是"run id → 那条 context
	// 的 cancel 函数"的映射。
	//
	// 【为什么它不需要跨进程一致】交付形态是单机单进程（README 的定位），
	// 一次 run 的执行生命周期绑在一条活的 SSE 请求上。多进程下这张表会
	// 各看各的，届时取消要走数据库信号（轮询或 pg NOTIFY），
	// 那是另一个设计，不是把这张表搬走就能解决的。
	mu      sync.Mutex
	running map[uuid.UUID]*runningRun
}

// runningRun 是一次在途运行的进程内句柄。
type runningRun struct {
	cancel context.CancelFunc

	// requested 记录"这次取消是用户点的"，与"客户端断开连接"区分开。
	//
	// 【为什么必须区分】两者的 ctx 都变成 Canceled，但终态不同：用户点
	// 取消是 cancelled（他想要的），客户端断开是 interrupted（他没说不要，
	// 只是连接没了）。只看 ctx.Err() 的话，点取消会得到一个 interrupted
	// 的 run——而前端正是按 cancelled 决定要不要显示"已停止"的。
	requested atomic.Bool

	// done 在这次运行的收尾写入**完成之后**被关闭。Cancel 等它，
	// 是为了让取消端点的响应里带的是真的终态，而不是一个还没落库的
	// 中间值。
	done chan struct{}
}

func NewUsecase(
	repo Repo,
	cp CheckpointStore,
	tools Registry,
	registry llm.Registry,
	idem IdempotencyStore,
	txm platform.TxManager,
	logger *slog.Logger,
	db platform.Querier,
) *Usecase {
	return &Usecase{
		repo: repo, cp: cp, tools: tools, registry: registry,
		idem: idem, txm: txm, logger: logger, db: db,
		running: make(map[uuid.UUID]*runningRun),
	}
}

// registerRunning / unregisterRunning 维护在途运行表。
func (u *Usecase) registerRunning(runID uuid.UUID, cancel context.CancelFunc) *runningRun {
	st := &runningRun{cancel: cancel, done: make(chan struct{})}
	u.mu.Lock()
	u.running[runID] = st
	u.mu.Unlock()
	return st
}

func (u *Usecase) unregisterRunning(runID uuid.UUID) {
	u.mu.Lock()
	delete(u.running, runID)
	u.mu.Unlock()
}

func (u *Usecase) lookupRunning(runID uuid.UUID) *runningRun {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running[runID]
}

// log 返回一个已经绑好 run 级追踪字段的 logger——没配 logger 时退回默认的，
// 让"忘了注入 logger"这件事只影响日志格式，不影响功能。
func (u *Usecase) log(ctx context.Context) *slog.Logger {
	return platform.LoggerFrom(ctx, u.logger)
}

// requireToolCapability 检查当前生效的 chat 模型是否声明了工具调用能力
// （issue #38）。创建与运行两条路径共用它，文案因此不会漂移。
//
// 【为什么必须包 platform.ErrInvalid，而不是 ErrConflict】
// 运行期这条拒绝只能走 SSE 的 error 帧——handler 在调 Start 之前就已经把
// 200 写出去了，改不了状态码。而 platform.SSEErrorType 只认
// ErrInvalid / ErrNotFound / ErrUpstream 三档，ErrConflict 会落到 default
// 变成 internal_error，前端 web/src/lib/errors.ts 会把它显示成"服务内部
// 错误"——排查方向从第一句话起就是错的（issue #34 修掉的正是这一类误报）。
//
// 语义上它也站得住：把一个不支持工具调用的模型配给要用工具的 Agent，
// 这个请求本身就是无效的。
//
// 【报文必须是 sentinel 的直接包装者】apps/api/internal/api/problem.go 的
// innermostMessage 只取错误链倒数第二层，也就是直接包装 sentinel 的那一层。
// 再往外包一层的话，客户端拿到的 detail 会变成一句无关的半截话，
// 而且不会报错——所以下面那个 fmt.Errorf 的 %w 必须直接落在
// platform.ErrInvalid 上。
func requireToolCapability(m *llm.Model, agentName string, toolNames []string) error {
	if m.Capabilities.ToolCalling {
		return nil
	}
	return fmt.Errorf(
		"当前生效的聊天模型 %q（llm_models.id=%s）没有声明工具调用能力，而 Agent %q 要用工具 %v；"+
			"请在引导页勾选「支持工具调用」后重新保存，或者把这个 Agent 的工具清空: %w",
		m.ModelID, m.ID, agentName, toolNames, platform.ErrInvalid)
}

// CreateAgent 校验并创建一个 Agent。toolNames 里任何一个不在 Registry
// 里注册过就拒绝——不允许创建一个引用了不存在工具的 Agent,那种配置
// 只会在真正执行时才报错,提前挡在创建这一步对用户更友好。
func (u *Usecase) CreateAgent(ctx context.Context, name, description, instruction string, toolNames []string) (*Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("agent name must not be empty: %w", platform.ErrInvalid)
	}
	if n := len([]rune(name)); n > maxNameLen {
		return nil, fmt.Errorf("agent name is too long (%d characters, max %d): %w", n, maxNameLen, platform.ErrInvalid)
	}
	if n := len([]rune(description)); n > maxDescriptionLen {
		return nil, fmt.Errorf("agent description is too long (%d characters, max %d): %w", n, maxDescriptionLen, platform.ErrInvalid)
	}
	if n := len([]rune(instruction)); n > maxInstructionLen {
		return nil, fmt.Errorf("agent instruction is too long (%d characters, max %d): %w", n, maxInstructionLen, platform.ErrInvalid)
	}
	for _, name := range toolNames {
		if _, err := u.tools.Get(name); err != nil {
			return nil, fmt.Errorf("tool %q is not registered: %w", name, platform.ErrInvalid)
		}
	}

	// nil slice 必须归一成空数组：contracts/openapi.yaml 里 toolNames 是
	// 可选项，客户端发 {"name":"x"} 时它是 nil，而 pgx 把 nil slice 编码
	// 成 SQL NULL——agents.tool_names 是 NOT NULL（列 DEFAULT 只在 INSERT
	// 语句里省略该列时才生效，这里显式绑定了 $5），INSERT 会被 23502
	// 顶回来变成 500（issue #18）。归一化放在业务层，所有调用方都覆盖。
	if toolNames == nil {
		toolNames = []string{}
	}

	// 创建期门控（issue #38）：让用户在"刚勾上工具"的那一刻就得到反馈，
	// 而不是等到发起运行时才发现。运行期那一道仍然保留——创建之后用户
	// 可能换了 chat 模型（当前生效模型由 LatestByKind 按 created_at 决定，
	// 重跑一次引导页就会换掉），只拦创建挡不住那条路径。
	//
	// 【零工具 Agent 不需要工具能力】它对模型没有这个要求，而且 ADK 对
	// 零工具走的是另一条路径。所以判据是"这个 Agent 是否声明了至少一个
	// 工具"，不是"模型有没有能力"。
	if len(toolNames) > 0 {
		m, err := u.registry.ActiveModel(ctx, llm.KindChat)
		if err != nil {
			return nil, fmt.Errorf("resolve active chat model: %w", err)
		}
		if err := requireToolCapability(m, name, toolNames); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	a := &Agent{
		ID: uuid.New(), Name: name, Description: description, Instruction: instruction,
		ToolNames: toolNames, CreatedAt: now, UpdatedAt: now,
	}
	if err := u.repo.CreateAgent(ctx, u.db, a); err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	return a, nil
}

func (u *Usecase) ListAgents(ctx context.Context) ([]*Agent, error) {
	agents, err := u.repo.ListAgents(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	return agents, nil
}

func (u *Usecase) GetAgent(ctx context.Context, id uuid.UUID) (*Agent, error) {
	a, err := u.repo.GetAgent(ctx, u.db, id)
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", id, err)
	}
	return a, nil
}

func (u *Usecase) ListToolCatalog(ctx context.Context) ([]ToolCatalogEntry, error) {
	entries, err := u.repo.ListToolCatalog(ctx, u.db)
	if err != nil {
		return nil, fmt.Errorf("list tool catalog: %w", err)
	}
	return entries, nil
}

func (u *Usecase) GetRun(ctx context.Context, id uuid.UUID) (*Run, error) {
	run, err := u.repo.GetRun(ctx, u.db, id)
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", id, err)
	}
	return run, nil
}

// ListRuns 取一个 Agent 的历史运行，keyset 分页（issue #45）。
//
// 第二个返回值是下一页的游标，没有下一页时为空串。
func (u *Usecase) ListRuns(ctx context.Context, agentID uuid.UUID, rawCursor string, limit int) ([]*Run, string, error) {
	limit, err := platform.ClampListLimit(limit)
	if err != nil {
		return nil, "", err
	}
	cur, err := platform.ParseCursor(rawCursor)
	if err != nil {
		return nil, "", err
	}

	runs, hasMore, err := u.repo.ListRunsByAgent(ctx, u.db, agentID, cur, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list runs of agent %s: %w", agentID, err)
	}

	next := ""
	if len(runs) > 0 {
		last := runs[len(runs)-1]
		next = platform.EncodeNextCursor(hasMore, last.CreatedAt.Format(time.RFC3339Nano), last.ID.String())
	}
	return runs, next, nil
}

func (u *Usecase) ListSteps(ctx context.Context, runID uuid.UUID) ([]*Step, error) {
	steps, err := u.repo.StepsByRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps of run %s: %w", runID, err)
	}
	return steps, nil
}

// runPlan 是一次运行在"还没有落库"之前就已经定下来的全部东西。
//
// 【为什么要先把校验和落库分开】被拒的那一轮不该在 agent_runs 里留下一条
// running 行（否则历史列表里会出现一次用户从没见过的失败运行，还要靠
// 收尾逻辑去补写终态）。prepare 只读、且全部可能失败的动作都在这一步；
// 通过之后才分配 run id、占幂等键、插行。
type runPlan struct {
	agent     *Agent
	tools     []Tool
	chatModel *llm.Model
	input     string
}

// Start 执行一次 Agent 运行,把 Eino ADK 的事件流归一化成 SSE 推给
// sink,同时把每一步落成 agent_run_steps 的一行、每条事件落成
// run_events 的一行。
//
// 【和 conversation.Usecase.Send 的"任何失败先推 error 事件"是同一个
// 模式】handler 一旦为这次请求打开了 SSE 连接,就不能再靠改 HTTP
// 状态码告诉客户端"这次失败了"——docs/sse-protocol.md 定义的 error
// 事件同样服务这里。
//
// 【idempotencyKey 非空时，命中重复键不重新执行】改为补发那条 run 已经
// 记录的事件，首帧是 run_started（同一个 run id）。语义见 ADR-008。
func (u *Usecase) Start(ctx context.Context, agentID uuid.UUID, input, idempotencyKey string, sink conversation.EventSink) (*Run, error) {
	plan, err := u.prepareRun(ctx, agentID, input)
	if err != nil {
		// 这一阶段失败时 run 行还不存在，所以这条 error 帧没有可挂的事件流
		// （也就没有 event_id）——见 emitUnpersisted 的注释。
		u.emitUnpersisted(sink, err)
		return nil, err
	}

	run, existing, err := u.claimRun(ctx, plan, idempotencyKey)
	if err != nil {
		u.emitUnpersisted(sink, err)
		return nil, err
	}
	if existing != nil {
		// 幂等命中：不重新执行，把那条 run 已经记录的事件补发一遍。
		return existing, u.replayRun(ctx, existing.ID, sink)
	}

	return u.execute(ctx, plan, run, nil, sink)
}

// prepareRun 做全部只读校验，返回一个可以立刻执行的 plan。
func (u *Usecase) prepareRun(ctx context.Context, agentID uuid.UUID, input string) (*runPlan, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("run input must not be empty: %w", platform.ErrInvalid)
	}

	ag, err := u.repo.GetAgent(ctx, u.db, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", agentID, err)
	}

	tools := make([]Tool, 0, len(ag.ToolNames))
	for _, name := range ag.ToolNames {
		t, err := u.tools.Get(name)
		if err != nil {
			return nil, fmt.Errorf("resolve tool %q for agent %s: %w", name, agentID, err)
		}
		tools = append(tools, t)
	}

	chatModel, err := u.registry.ActiveModel(ctx, llm.KindChat)
	if err != nil {
		return nil, fmt.Errorf("resolve active chat model: %w", err)
	}

	// 运行期门控（issue #38）——它是权威的那一道，因为"创建之后又换了
	// 模型"只有这里拦得住。
	if len(ag.ToolNames) > 0 {
		if err := requireToolCapability(chatModel, ag.Name, ag.ToolNames); err != nil {
			return nil, err
		}
	}

	return &runPlan{agent: ag, tools: tools, chatModel: chatModel, input: input}, nil
}

// runsEndpoint 是幂等键的作用域字符串（issue #56 / ADR-008）。
//
// 【为什么把 Agent id 编进来】主键是 (endpoint, idempotency_key)，同一个键
// 在另一个 Agent 上不该命中。这与会话那一侧把会话 id 编进 endpoint 是同一个
// 做法（见 conversation.messagesEndpoint 的注释）。
func runsEndpoint(agentID uuid.UUID) string {
	return "POST /api/v1/agents/" + agentID.String() + "/runs"
}

// claimRun 抢占幂等键并插入 run 行。
//
// 返回值：created 是这次真正新建的 run（existing 为 nil），
// existing 非 nil 表示命中幂等键、这次不执行（created 为 nil）。
//
// 【顺序：先占键，再插行】反过来的话，命中重复键时那条已经插入的 run 行
// 就得删掉——多一次写、多一个"删失败"的中间态。先占键则重复请求根本不会
// 产生 run 行。
//
// 【被拒的那一轮为什么不留 run 行】见 runPlan 的注释；这里是在
// prepareRun 之后才走到，所以被拒的情况（空输入、agent 不存在、模型不支持
// 工具）都不会进这个方法。
func (u *Usecase) claimRun(ctx context.Context, plan *runPlan, idempotencyKey string) (created, existing *Run, err error) {
	runID := uuid.New()
	now := time.Now()

	if idempotencyKey != "" {
		rec := &IdempotencyRecord{
			Endpoint:     runsEndpoint(plan.agent.ID),
			Key:          idempotencyKey,
			ResourceType: "agent_run",
			ResourceID:   runID,
			// run 的事件号从 1 开始（migrations/0009 的 run_counters），
			// 所以"从头补发这个 run"就是 after_event_id = 0。会话那一侧
			// 需要 first_event_id 是因为它的事件号按会话发号、不分轮次；
			// run 维度天然按 run 分，不需要那个起点。
			FirstEventID:       0,
			RequestFingerprint: fingerprint(plan.input),
			CreatedAt:          now,
		}

		err := u.idem.ReserveIdempotencyKey(ctx, u.db, rec, now.Add(-platform.IdempotencyKeyTTL))
		if errors.Is(err, platform.ErrIdempotentHit) {
			existing, lerr := u.lookupExistingRun(ctx, plan.agent.ID, idempotencyKey, rec.RequestFingerprint)
			if lerr != nil {
				return nil, nil, lerr
			}
			return nil, existing, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reserve idempotency key for agent run: %w", err)
		}
	}

	run := &Run{
		ID: runID, AgentID: plan.agent.ID, Status: RunRunning, Input: plan.input,
		StateSchemaVersion: CurrentStateSchemaVersion, CreatedAt: now, UpdatedAt: now,
	}
	if err := u.repo.InsertRun(ctx, u.db, run); err != nil {
		return nil, nil, fmt.Errorf("insert run: %w", err)
	}
	return run, nil, nil
}

// lookupExistingRun 取回幂等键指向的那条 run。
//
// 【正文不同必须报错，不能重放】同一个键配不同的输入时，重放等于把上一次
// 的那次运行的结果端给用户，而他新写的那句话既没执行、也不会报错——
// 界面上只是旧结果又出现了一遍。这是本项目最忌讳的那类静默失败。
func (u *Usecase) lookupExistingRun(ctx context.Context, agentID uuid.UUID, key, wantFingerprint string) (*Run, error) {
	rec, err := u.idem.LookupIdempotencyKey(ctx, u.db, runsEndpoint(agentID), key)
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency key after hit: %w", err)
	}
	if rec.RequestFingerprint != wantFingerprint {
		return nil, fmt.Errorf("idempotency key reused with a different input: %w", platform.ErrInvalid)
	}
	run, err := u.repo.GetRun(ctx, u.db, rec.ResourceID)
	if err != nil {
		return nil, fmt.Errorf("load run %s of idempotency key: %w", rec.ResourceID, err)
	}
	return run, nil
}

// execute 是真正跑一次运行的那段：注册在途句柄、发 run_started、
// 驱动 Agent、落每一步、写终态。
//
// priorTurns 是恢复时重建的历史（新运行时为空）。
func (u *Usecase) execute(ctx context.Context, plan *runPlan, run *Run, priorTurns []resumeTurn, sink conversation.EventSink) (*Run, error) {
	// 【把 run id 塞进 ctx 是这一层做的，不是 handler】run id 是这一刻才
	// 生成的，而 handler 拿不到它（它只知道 Agent id）。塞进去之后，
	// 这条路径上每一层用 platform.LoggerFrom 打的日志都会带上
	// agent_run_id（issue #71）。
	ctx = platform.WithAgentRunID(ctx, run.ID.String())

	// runCtx 是这次运行自己的生命周期。consumeEvents 一旦返回——正常结束、
	// 出错、被取消、或者客户端断开——它就必须结束：drainIterator 的每一次
	// 发送都 select 这个 ctx，只有取消能让生产者从"没有接收者的通道"上退出来
	// （issue #35 的 goroutine/iterator 泄漏）。取消后 ADK 那边的模型请求
	// 也会被一起拆掉。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// 注册在途句柄，取消端点靠它拿到这条 ctx 的 cancel 函数（issue #55）。
	// done 在最外层 defer 里关闭：Cancel 等它，等的就是"收尾写入做完了"。
	st := u.registerRunning(run.ID, cancelRun)
	defer func() {
		u.unregisterRunning(run.ID)
		close(st.done)
	}()

	// 首帧必须是 run_started（ADR-008）：客户端凭它拿到这次运行的 id，
	// 而取消端点、run 级重订阅、幂等重放都要用这个 id。
	// 【取消按钮没有它就无从下手】此前五种事件里没有任何一种带 run id。
	if _, err := u.emitRunEvent(ctx, run.ID, sink, "run_started", map[string]string{"runId": run.ID.String()}); err != nil {
		// 连首帧都发不出去，说明客户端已经走了或者库写不进去。
		// 该收的尾照收，但要把它当失败处理——不能留下一条永远 running 的行。
		return u.finishRun(ctx, run, st, err)
	}

	events, err := runAgent(runCtx, u.registry, plan.chatModel.ID.String(), plan.agent, plan.tools, plan.input, priorTurns)
	if err != nil {
		return u.finishRun(ctx, run, st, fmt.Errorf("start agent run: %w", err))
	}

	output, runErr := u.consumeEvents(ctx, run.ID, plan.chatModel.ID.String(), events, sink, st)
	run.Output = output
	// 消费端已经退出，通知生产者收摊。
	cancelRun()

	return u.finishRun(ctx, run, st, runErr)
}

// finishRun 把 run 推进终态并把失败告诉客户端。
//
// 【终态的选择顺序：用户取消 > ctx 被取消 > 失败/成功】
//   - st.requested 为真 → cancelled（用户点了停止，终态就该是它点的那个）
//   - ctx 已取消（客户端断开、服务端退出）→ interrupted（他没说不要，
//     只是连接没了）
//   - 其余失败 → failed
//
// 【不覆盖已有的终态】走的是 CAS（UpdateRunStatus 的 WHERE status = running）。
// 取消与收尾同时发生时只有一个能成功，另一个拿到 ErrConflict——
// 这正是"不覆盖"这条要求的强制点，不是靠先检查后写入。
func (u *Usecase) finishRun(ctx context.Context, run *Run, st *runningRun, cause error) (*Run, error) {
	// done 事件发出之后客户端立刻断开是常事（关标签页），跟着请求 ctx 走
	// 的话这一条 CAS 会失败——而且这条路径不会走 failRun，run 行会直接停在
	// 'running'（issue #14）。所以终态写入一律用脱离请求的 ctx。
	wctx, cancelWrite := detachedWriteCtx(ctx)
	defer cancelWrite()

	to := RunCompleted
	switch {
	case st != nil && st.requested.Load():
		to = RunCancelled
	case cause != nil && ctx.Err() != nil:
		to = RunInterrupted
	case cause != nil:
		to = RunFailed
	}

	if to != RunCompleted {
		// 失败/中断/取消都要给客户端一条 error 帧——它是最外层能说话的通道。
		// type 走全项目共享的那套枚举（platform.SSEErrorType）：空输入是
		// invalid_argument、agent 不存在是 not_found、上游模型失败是
		// upstream_llm_error。以前这里硬编码 internal_error，把客户端
		// 自己能纠正的错误说成服务端故障，前端也只能显示"服务内部错误"
		// （issue #34）。
		u.emitRunError(ctx, run.ID, cause)
	}

	if err := u.repo.UpdateRunStatus(wctx, u.db, run.ID, RunRunning, to); err != nil {
		// 【取消撞上正常的收尾，不算故障】用户点取消的那一刻这次运行
		// 恰好跑完了——CAS 会因为状态已经不是 running 而失败。这时
		// 真正的终态是数据库里那个（completed），把它读回来返回，
		// 而不是把一次成功的运行报成错误。
		if errors.Is(err, platform.ErrConflict) {
			if latest, gerr := u.repo.GetRun(wctx, u.db, run.ID); gerr == nil {
				u.log(ctx).Info("run reached a terminal state before this finalize",
					"wanted", string(to), "actual", string(latest.Status))
				return latest, cause
			}
		}
		if cause != nil {
			return run, errors.Join(cause, fmt.Errorf("mark run %s %s: %w", run.ID, to, err))
		}
		return run, fmt.Errorf("mark run %s %s: %w", run.ID, to, err)
	}

	run.Status = to
	if cause != nil {
		u.log(ctx).Warn("agent run ended with an error", "status", string(to), "error", cause)
	} else {
		u.log(ctx).Info("agent run finished", "status", string(to))
	}
	return run, cause
}

// Cancel 取消一次在途运行（issue #55）。
//
// §8.5 的四件事在这里的落点：
//
//  1. **context 取消传进 Eino 流**——在途表里拿到的是这次运行的 cancel
//     函数，它取消了 runCtx，ADK 那边的模型请求跟着被拆掉。
//  2. **run 置 cancelled、step 顶多 interrupted**——终态由收尾路径按
//     st.requested 决定（`cancelled`），此刻没跑完的那一步保持 running，
//     由收尾路径标 `interrupted`——取消发生在 run 层，step 没有 cancelled
//     这一态。
//  3. **排空 SSE writer**——收尾路径发完最后一条事件就返回，handler 随之
//     结束响应；此后没有任何代码再往这条连接写（emitting 的 goroutine 已经
//     退出）。这正是"不能取消完还往已关闭的连接写"的落实方式。
//  4. **与优雅退出的顺序一致**——取消路径不碰 River：run 的执行在本进程内
//     （不走队列），所以"River job 可重入"这一条对它不适用；api 的优雅退出
//     走的是拒新请求 → drain 在途 SSE（apps/api/internal/app/shutdown.go），
//     那条路径上客户端断开会把 run 收成 interrupted。
//
// 【为什么等 st.done（有上限）】取消端点的响应里如果带的是取消前读到的
// 状态（running），前端只能靠轮询判断"到底停下来没有"。等一下收尾写入，
// 响应里就是真的终态。上限是为了不让一个卡住的运行把取消请求也拖死。
func (u *Usecase) Cancel(ctx context.Context, runID uuid.UUID) (*Run, error) {
	run, err := u.repo.GetRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", runID, err)
	}

	// 状态机是权威判据（issue #59）：终态的 run 不可再取消，
	// interrupted 的 run 可以（用户放弃一条没跑完的运行）。
	if !run.Status.CanTransition(RunCancelled) {
		return nil, fmt.Errorf("run %s is %s, cannot be cancelled: %w", runID, run.Status, platform.ErrConflict)
	}

	if st := u.lookupRunning(runID); st != nil {
		st.requested.Store(true)
		st.cancel()
		select {
		case <-st.done:
		case <-time.After(cancelFinalizeTimeout):
			u.log(ctx).Warn("cancel did not observe the run finalize in time", "run_id", runID)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else if run.Status == RunRunning {
		// 【为什么这条分支存在】在途表里没有、数据库里却是 running，
		// 说明那条运行的进程已经不在了（被 KILL），而启动扫描还没跑到
		// （或扫描之后又有人手工把状态改了）。这里做一次 CAS 兜底，
		// 让用户至少能把这条卡住的行收掉。
		if err := u.repo.UpdateRunStatus(ctx, u.db, runID, RunRunning, RunCancelled); err != nil {
			return nil, fmt.Errorf("cancel orphan run %s: %w", runID, err)
		}
	}

	latest, err := u.repo.GetRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("reload run %s after cancel: %w", runID, err)
	}
	return latest, nil
}

// cancelFinalizeTimeout 是取消端点等收尾写入的上限。
const cancelFinalizeTimeout = 3 * time.Second

// pendingCall 追踪一次尚未收到结果的工具调用——tool_call 和
// tool_result 是两个独立的事件,中间可能夹着其它工具调用（模型一次性
// 请求多个工具时),用 toolCallID 关联,不能假设"上一条 tool_call
// 对应下一条 tool_result"这种 FIFO 顺序。
type pendingCall struct {
	// step 是这次调用**在工具执行之前**就已经落库的那一行（status=running）。
	// 它有两个作用：崩溃现场里能看到"这一步走到哪了"，以及 tool_result
	// 到达时用同一个 step id 记效果账本（issue #63/#64）。
	step      *Step
	toolName  string
	toolArgs  json.RawMessage
	startedAt time.Time
}

// consumeEvents 排空 adkEvent 流,每步落一行 agent_run_steps,把事件
// 转成 SSE 推给 sink（同时持久化进 run_events）,返回累积的最终文本输出。
//
// 【事件号问数据库要，不再是一个进程内计数器】issue #54 把 Agent 的运行
// 事件升级到了 run 维度：run_events 按 run 独立发号，断线重订阅与幂等重放
// 都读它。进程内计数器在进程重启后会从零开始，而重放要读的正是"这个 run
// 已经发到几号了"——那必须是数据库事实。
func (u *Usecase) consumeEvents(ctx context.Context, runID uuid.UUID, chatModelID string, events <-chan adkEvent, sink conversation.EventSink, st *runningRun) (string, error) {
	var output strings.Builder
	seq := 0
	pending := map[string]*pendingCall{}

	var llmStep *Step
	var llmStepStarted time.Time

	// roundStepClosed 表示"这一轮模型生成的 Step 已经记过账了"。
	//
	// 【为什么需要它】模型一次可以并行请求多个工具，eino_adk 的 emitToolCalls
	// 会为每一个调用各发一个 adkEventToolCall。tool_call 分支每个事件都
	// open + close 一次的话，一次并行调用就会写出两条 llm 行——轨迹页上
	// "模型跑了几轮"和每轮的 LatencyMS 都跟着错（issue #33：一条 llm 行
	// 对应一轮生成，不是一次工具调用）。
	//
	// 复位点在 tool_result：工具结果交回模型就是下一轮生成的开始。
	var roundStepClosed bool

	// insertStep 落一行 Step。写库用脱离请求生命周期的 ctx（见
	// detachedWriteCtx）：客户端一断开请求 ctx 就被取消，用它 INSERT 会
	// 一条都写不下去，轨迹恰好断在最需要留痕的那一刻（issue #14）。
	insertStep := func(s *Step) {
		wctx, cancel := detachedWriteCtx(ctx)
		defer cancel()
		if err := u.repo.InsertStep(wctx, u.db, s); err != nil {
			// 轨迹写不进去不是致命的（run 本身还能跑完），但必须留痕——
			// 静默丢掉会让轨迹页看起来"模型什么都没做"。
			u.log(ctx).Error("failed to record agent run step",
				"run_id", runID, "seq", s.Seq, "type", s.Type, "error", err)
		}
	}

	// updateStep 把一行已经存在的 Step 补上终态与结果。
	updateStep := func(s *Step) {
		wctx, cancel := detachedWriteCtx(ctx)
		defer cancel()
		if err := u.repo.UpdateStep(wctx, u.db, s); err != nil {
			u.log(ctx).Error("failed to finalize agent run step",
				"run_id", runID, "step_id", s.ID, "seq", s.Seq, "error", err)
		}
	}

	// openLLMStep 在"这一轮的第一个助手事件"上开一条模型轮次记录。
	//
	// 【为什么 token 和 tool_call 都要开】模型不发正文、直接请求调用工具
	// 是 ReAct 的常态；只在 token 上开 step 会让这种轮次一行都不落，
	// 轨迹里看起来"模型什么都没做"就把工具跑了一遍，schema 里
	// type=llm 的定义（migrations/0005_agents.up.sql:66）明确包含
	// "工具调用请求"这一种输出（issue #33）。
	//
	// 【这一行在轮次开始时落库，不是结束时】issue #64 要求"崩溃时该 Step 标
	// interrupted，不假装成功"——要做到这一点，崩溃现场里必须已经有这一行。
	// 轮次结束时只是把它更新成终态（见 closeLLMStep）。
	openLLMStep := func() {
		if llmStep == nil {
			seq++
			llmStep = &Step{
				ID: uuid.New(), RunID: runID, Seq: seq, Type: StepTypeLLM,
				Status: StepRunning, CreatedAt: time.Now(),
			}
			insertStep(llmStep)
			llmStepStarted = llmStep.CreatedAt
		}
	}

	// checkpoint 落一次 Step 边界的快照——CheckpointStore.Save 的调用点。
	//
	// 【state_snapshot 仍然传 nil，但 state_schema_version 是有意义的】
	// 快照结构留给真正需要它的那一刻再定（不提前猜一个序列化格式），
	// 而版本号在 Resume 入口被比较（issue #65）——所以 run 行上那个数字
	// 必须是真的，不能是硬编码的 1 而和常量脱节。
	checkpoint := func() {
		wctx, cancel := detachedWriteCtx(ctx)
		defer cancel()
		_ = u.cp.Save(wctx, u.db, runID, seq, output.String(), nil)
	}

	closeLLMStep := func(status StepStatus, errMsg string) {
		if llmStep == nil {
			return
		}
		llmStep.Status = status
		llmStep.LatencyMS = int(time.Since(llmStepStarted).Milliseconds())
		llmStep.Error = errMsg
		updateStep(llmStep)
		llmStep = nil
		checkpoint()
	}

	// closeOpenStepsOnExit 保证任何一条退出路径都不会留下"永远 running"的
	// step：那些行本来是崩溃现场的记录，而这里是**正常返回**，正常返回
	// 之后它们再也不会被更新。
	closeOpenStepsOnExit := func() {
		if llmStep != nil {
			closeLLMStep(StepInterrupted, "运行在收到 done 之前结束")
		}
	}

	for event := range events {
		// 【这里刻意不检查 ctx.Err() 就退出】
		//
		// 请求 ctx 被取消的时刻意味着两件不同的事——客户端断开、或用户点了
		// 取消——而两者都不该让"已经发生的事"从轨迹里消失：Eino 会在拆掉
		// 这次运行时推一条 error 事件过来，那一条正是 issue #14 要留住的东西
		// （轨迹恰好断在最需要留痕的那一刻）。
		//
		// 停止是自然发生的：取消会让生产者（drainIterator）从通道上退出，
		// 缓冲区排空之后循环就结束了。所以下面每一轮照常处理，而所有写库都走
		// 脱离请求的 ctx（detachedWriteCtx），发不出去的帧由 sink 自己报错。
		switch event.kind {
		case adkEventToken:
			openLLMStep()
			// 有正文说明这是新一轮生成（上一条 llm 行已经在 tool_call 或
			// done 上收掉了）。
			roundStepClosed = false
			output.WriteString(event.text)
			if _, err := u.emitRunEvent(ctx, runID, sink, "token", map[string]string{"text": event.text}); err != nil {
				closeOpenStepsOnExit()
				return output.String(), err
			}

		case adkEventToolCall:
			// 这一轮请求调用工具——它同样是模型这一轮的输出，所以这一轮也要
			// 有 llm 行：模型不发正文、直接调工具时（ReAct 的常态）靠的就是
			// 这里开的那一条（issue #33）。
			//
			// 【同一轮只记一次】一次并行调用会连着来 N 个 tool_call 事件，
			// 每个都 open+close 就会写出 N 条 llm 行，把"模型跑了几轮"和
			// 每轮延迟都算错。roundStepClosed 保证这轮只记一次。
			if !roundStepClosed {
				openLLMStep()
				closeLLMStep(StepCompleted, "")
				roundStepClosed = true
			}
			seq++
			toolStep := &Step{
				ID: uuid.New(), RunID: runID, Seq: seq, Type: StepTypeTool, Status: StepRunning,
				ToolName: event.toolName, ToolArgs: event.toolArgs, CreatedAt: time.Now(),
			}
			// 【必须在工具执行之前落库】这一行就是崩溃现场里"这一步走到了
			// 哪"的证据（ADR-007）。晚于执行的话，被 KILL 那一刻这一行
			// 根本不存在，恢复逻辑连该不该重放都无从判断。
			insertStep(toolStep)
			pending[event.toolCallID] = &pendingCall{
				step: toolStep, toolName: event.toolName,
				toolArgs: event.toolArgs, startedAt: toolStep.CreatedAt,
			}
			if _, err := u.emitRunEvent(ctx, runID, sink, "tool_call", map[string]any{
				"id": event.toolCallID, "name": event.toolName, "args": json.RawMessage(event.toolArgs),
			}); err != nil {
				closeOpenStepsOnExit()
				return output.String(), err
			}

		case adkEventToolResult:
			p, ok := pending[event.toolCallID]
			var latencyMS int
			var toolArgs json.RawMessage
			var toolStep *Step
			if ok {
				toolStep, latencyMS, toolArgs = p.step, int(time.Since(p.startedAt).Milliseconds()), p.toolArgs
				delete(pending, event.toolCallID)
			} else {
				// Eino 没给出这次调用的 toolCallID（比如某些非流式路径）
				// 时退化成"追加一条新 seq"——宁可序号多一个,不能因为
				// 关联不上就整条丢弃工具的执行结果。
				seq++
				toolStep = &Step{
					ID: uuid.New(), RunID: runID, Seq: seq, Type: StepTypeTool,
					ToolName: event.toolName, ToolArgs: toolArgs, CreatedAt: time.Now(),
				}
				insertStep(toolStep)
			}

			// ① 先记效果账本，再写步骤结果——顺序不能反（issue #63/ADR-007）。
			//
			// 账本用**独立事务**提交（传 u.db 而不是某个事务），所以它不会
			// 跟着步骤写入一起回滚。两次写入之间的崩溃窗口留下的现场是
			// "账本有、步骤还是 running"——这正是崩溃表第二行里那个
			// **可区分**的子情况：恢复路径据此判定副作用已经发生，拒绝自动
			// 重放。反过来先写步骤的话，这个窗口里两个记录都没有，
			// "工具跑了"和"工具根本没跑"就真的分不开了。
			effectKey := EffectKey(event.toolName, toolArgs)
			if err := u.repo.RecordToolEffect(ctx, u.db, toolStep.ID, effectKey); err != nil {
				if errors.Is(err, platform.ErrToolEffectApplied) {
					// 【看到这个冲突 = 工具被重复执行了 = resume 没生效】
					// 项目文档 §9.5 就是这么判读的，而且特意提醒别搞反。
					// 这里不能继续往下走：整条恢复机制的全部意义就是
					// "不要静默重复执行"，把它咽下去等于把那句话作废。
					closeOpenStepsOnExit()
					return output.String(), fmt.Errorf(
						"tool %q was invoked twice for step %s within run %s: %w",
						event.toolName, toolStep.ID, runID, platform.ErrToolEffectApplied)
				}
				u.log(ctx).Error("failed to record tool effect",
					"run_id", runID, "step_id", toolStep.ID, "tool", event.toolName, "error", err)
			}

			// ② 再补步骤的终态与结果。
			toolStep.Status = StepCompleted
			toolStep.ToolResult = event.toolResult
			toolStep.LatencyMS = latencyMS
			updateStep(toolStep)
			checkpoint()

			if _, err := u.emitRunEvent(ctx, runID, sink, "tool_result", map[string]any{
				"id": event.toolCallID, "result": json.RawMessage(event.toolResult),
			}); err != nil {
				closeOpenStepsOnExit()
				return output.String(), err
			}
			// 工具结果交回模型，下一轮生成从这里开始——这一轮再出现
			// tool_call 就该是一条新的 llm 行了。
			roundStepClosed = false

		case adkEventUsage:
			// 【ADK 路径的记账要自己上报（issue #47）】它绕开 llm 包的适配器
			// 构造 Eino 原生模型（见 eino_adk.go 的文件头注释），所以那边
			// 自动记账覆盖不到它——而 Agent 恰恰是最贵的一条路径（一次运行
			// 最多 20 轮模型调用）。绕开封装的代价就是要自己把观测数据报出来。
			//
			// 记账失败不上抛（RecordUsage 内部只记日志）：它是观测，
			// 不该让一次运行因为记不上账而失败。
			u.registry.RecordUsage(ctx, chatModelID, llm.KindChat, llm.Usage{
				PromptTokens:     event.usage.PromptTokens,
				CompletionTokens: event.usage.CompletionTokens,
			})

		case adkEventError:
			// 错误可能在任何助手事件之前就到达（模型第一轮就挂了），这时
			// 还没有开着的轮次——先开一条再标失败，让"在这里断的"这件事
			// 在轨迹里看得见，而不是整个 run 一行 Step 都没有。
			openLLMStep()
			closeLLMStep(StepFailed, event.err.Error())
			// 【不能一律当成上游故障】这一条错误来自整张 ReAct 图，不只有
			// provider：工具自己失败（knowledge_search 查不到那一行 →
			// ErrNotFound）、请求 ctx 被取消，都会走到这里。一律 Join
			// ErrUpstream 的话 type 是 upstream_llm_error，前端照文案提示
			// 用户"检查 API Key 和配额"——排查方向从第一句话起就是错的
			// （issue #34）。已经带 sentinel 的原样放行，剩下的才归到上游。
			return output.String(), agentEventError(event.err)

		case adkEventDone:
			closeLLMStep(StepCompleted, "")
			if _, err := u.emitRunEvent(ctx, runID, sink, "done", struct{}{}); err != nil {
				return output.String(), err
			}
			return output.String(), nil
		}
	}
	closeOpenStepsOnExit()
	return output.String(), fmt.Errorf("agent event stream closed without a done event")
}

// agentEventError 给 ADK 事件流上的错误归类。
//
// 【先说清楚什么不是问题，免得改错方向】事件流上的错误来自整张 ReAct 图，
// 不只有 provider。但"一律包一层 ErrUpstream"并不会把工具的错误类型吃掉：
// errors.Is 对 errors.Join 出来的错误是看每个成员的，而
// platform.SSEErrorType 的判断顺序是 ErrInvalid → ErrNotFound → ErrUpstream，
// 所以带 ErrNotFound 的工具错误本来就分类成 not_found（这一条有测试钉着，
// 见 usecase_test.go 的 TestConsumeEvents_ErrorEvent_TypeMapping）。
//
// 【真正的缺口只有一个：请求 ctx 被取消】context.Canceled 不在
// SSEErrorType 认的那三个 sentinel 里，被 Join 进 ErrUpstream 之后
// 整个错误就变成 upstream_llm_error——客户端断开导致的收尾，会在日志和
// type 上被说成"上游模型服务出错"。这里把已有 sentinel（含 ctx 的两个）
// 原样放行，剩下真正没归类的才按上游故障包一层。
func agentEventError(err error) error {
	for _, sentinel := range []error{
		platform.ErrInvalid,
		platform.ErrNotFound,
		platform.ErrConflict,
		platform.ErrUpstream,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return fmt.Errorf("agent run failed: %w", errors.Join(platform.ErrUpstream, err))
}

// fingerprint 是请求正文的摘要，用于识别"同一个幂等键配了不同的正文"。
func fingerprint(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// marshalEvent 组装一条事件的 payload（{"type":..., "data":...}）。
func marshalEvent(eventType string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}{Type: eventType, Data: payload})
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}
	return body, nil
}

// emitRunEvent 持久化一条 run 事件再发给客户端——顺序不能反。
//
// 【为什么必须先落库再发】客户端拿到 event_id 之后会把它当成续传游标。
// 先发后写的话，客户端拿着一个数据库里还不存在的号去重订阅，
// 服务端"event_id > N"的查询会跳过紧随其后的那一条（它此刻还没落库，
// 但发号已经发出去了）——静默丢事件，而且不报错。
//
// 【发号与写入在同一个事务里】见 port.go 的注释（与 conversation 那一侧
// 同一套理由）。
func (u *Usecase) emitRunEvent(ctx context.Context, runID uuid.UUID, sink conversation.EventSink, eventType string, payload any) (int64, error) {
	body, err := marshalEvent(eventType, payload)
	if err != nil {
		return 0, err
	}

	// 持久化走脱离请求生命周期的 ctx：这一条最常见的触发场景恰恰是
	// "客户端刚断开"——跟着请求 ctx 走的话，最后那条 error 事件写不进去，
	// 而它正是重订阅与重放要读到的收尾帧。
	wctx, cancel := detachedWriteCtx(ctx)
	defer cancel()

	var id int64
	err = u.txm.InTx(wctx, func(q platform.Querier) error {
		allocated, aerr := u.repo.NextRunEventID(wctx, q, runID)
		if aerr != nil {
			return aerr
		}
		id = allocated
		return u.repo.AppendRunEvent(wctx, q, runID, RunEvent{ID: allocated, Type: eventType, Payload: body})
	})
	if err != nil {
		return 0, fmt.Errorf("persist %s event of run %s: %w", eventType, runID, err)
	}

	if err := sink.Emit(conversation.Event{ID: id, Type: eventType, Payload: body}); err != nil {
		return id, fmt.Errorf("emit event to client: %w", err)
	}
	return id, sink.Flush()
}

// emitUnpersisted 往连接上写一条**没有持久化**的 error 帧。
//
// 【什么时候会用到它】run 行还没落库就失败的那些情况：空输入、agent 不存在、
// 当前没有可用的聊天模型、模型不支持工具调用。这些失败发生在分配 run id
// 之前，没有 run_events 可以挂，也就没有真实的 event_id。
//
// 【event id 用 0，而不是编一个】0 不是任何一条已持久化事件的号
//（两张计数器表的 next_event_id 都从 1 开始），sseSink 因此把 `id:` 那一行
// 整个省掉——与 docs/sse-protocol.md 里"没有 id 的帧不参与发号"那条一致。
// 编一个号会更糟：客户端会把游标推到一个不存在的位置。
func (u *Usecase) emitUnpersisted(sink conversation.EventSink, cause error) {
	body, err := marshalEvent("error", map[string]string{
		"type":   platform.SSEErrorType(cause),
		"detail": cause.Error(),
	})
	if err != nil {
		return
	}
	// 发送失败没有补救手段（连接已经坏了），不向上抛——调用方此刻正要
	// 返回一个更有价值的错误。
	_ = sink.Emit(conversation.Event{ID: 0, Type: "error", Payload: body})
	_ = sink.Flush()
}

// emitRunError 把一次失败的收尾告诉客户端（持久化在 run 的事件流里）。
func (u *Usecase) emitRunError(ctx context.Context, runID uuid.UUID, cause error) {
	if cause == nil {
		return
	}
	// 收尾写入要脱离请求 ctx（见 detachedWriteCtx）：客户端断开正是这条
	// 路径最常见的触发原因，用请求 ctx 连事件都写不下去。
	wctx, cancel := detachedWriteCtx(ctx)
	defer cancel()
	if _, err := u.emitRunEvent(wctx, runID, nopSink{}, "error", map[string]string{
		"type":   platform.SSEErrorType(cause),
		"detail": cause.Error(),
	}); err != nil {
		// 写不进去也不上抛：调用方正在返回那个更重要的原始错误。
		u.log(ctx).Error("failed to record run error event", "run_id", runID, "error", err)
	}
}

// nopSink 是一个不往任何连接写东西的 EventSink。
//
// 【它的用途：只持久化、不发送】收尾的 error 事件必须落进 run_events
// （这样重放和重订阅能看到"这次是怎么结束的"），但**不该**再往客户端的
// 连接上写——那是原始请求的连接，它的读端此刻可能已经关了，写它只会得到
// 一次注定失败的写操作（§8.5 第 3 条："不能取消完还往已关闭的连接写"）。
type nopSink struct{}

func (nopSink) Emit(conversation.Event) error { return nil }
func (nopSink) Flush() error                  { return nil }
func (nopSink) Done() <-chan struct{}         { return nil }

// replayRun 把一条已经存在的 run 的事件补发一遍（幂等命中，ADR-008）。
//
// 【补发完即结束响应，不持有连接等新事件】与会话维度那条
// GET /conversations/{id}/events 保持同一条语义：要看后续内容，客户端凭
// 首帧里的 run id 再连一次 run 级重订阅端点。
func (u *Usecase) replayRun(ctx context.Context, runID uuid.UUID, sink conversation.EventSink) error {
	events, err := u.repo.RunEventsAfter(ctx, u.db, runID, 0)
	if err != nil {
		return fmt.Errorf("replay run %s: %w", runID, err)
	}
	for _, ev := range events {
		if err := sink.Emit(conversation.Event{ID: ev.ID, Type: ev.Type, Payload: ev.Payload}); err != nil {
			return fmt.Errorf("replay event %d of run %s: %w", ev.ID, runID, err)
		}
	}
	return sink.Flush()
}

// RunEvents 读一条 run 的事件流（run 级断线重订阅，issue #54）。
func (u *Usecase) RunEvents(ctx context.Context, runID uuid.UUID, afterEventID int64) ([]RunEvent, error) {
	// run 不存在时返回 not_found，而不是一个空洞的空列表——"这个 run 没有
	// 更多事件"和"根本没有这个 run"对客户端是两件事。
	if _, err := u.repo.GetRun(ctx, u.db, runID); err != nil {
		return nil, err
	}
	events, err := u.repo.RunEventsAfter(ctx, u.db, runID, afterEventID)
	if err != nil {
		return nil, fmt.Errorf("list events of run %s: %w", runID, err)
	}
	return events, nil
}

// InterruptRunningRuns 是启动扫描（ADR-007 决策二）。
//
// 升级时正在飞的 run 标 interrupted 而不是 failed：语义准确（被打断了，
// 可以从断点继续），且与 Resume 入口衔接。扫描出来的 run id 逐个记日志——
// 这是这次升级唯一能看出"有几条运行被就地掐断"的地方。
func (u *Usecase) InterruptRunningRuns(ctx context.Context) (int, error) {
	ids, err := u.repo.InterruptRunningRuns(ctx, u.db)
	if err != nil {
		return 0, fmt.Errorf("interrupt runs left running by a previous process: %w", err)
	}
	for _, id := range ids {
		u.log(ctx).Warn("run left running by a previous process was marked interrupted", "run_id", id)
	}
	return len(ids), nil
}

// PruneCheckpoints 回收终态 run 的快照（issue #67 / CheckPointDeleter）。
//
// 【保留窗口】终态之后 24 小时。它和幂等键的窗口是同一个数量级的理由：
// 恢复要用到的快照只在"刚崩完、用户马上点恢复"这个窗口里有意义；
// 一天之后没人会再恢复一条已经结束的运行，而快照本身也不再变化。
const checkpointRetention = 24 * time.Hour

func (u *Usecase) PruneCheckpoints(ctx context.Context) (int64, error) {
	n, err := u.repo.ClearTerminalRunCheckpoints(ctx, u.db, time.Now().Add(-checkpointRetention))
	if err != nil {
		return 0, fmt.Errorf("prune run checkpoints: %w", err)
	}
	if n > 0 {
		u.log(ctx).Info("pruned run checkpoints", "count", n)
	}
	return n, nil
}

// ── 恢复（M4-C / issue #64）────────────────────────────────────

// ResumePlan 是一次恢复的全部检查结果。
//
// 【为什么校验和执行分成两步】所有拒绝理由（终态、版本不兼容、副作用
// 已经生效、工具不允许重放）都必须在**响应还来得及改 HTTP 状态码**的时候
// 判掉。一旦打开 SSE，状态码就锁死在 200 上了，那时只能推一条 error 帧，
// 而客户端拿到的 type 会退化成笼统一句。分成两步之后，这些拒绝是正常的
// 409 Problem，前端能按 type 给出准确的下一步提示。
type ResumePlan struct {
	run       *Run
	agent     *Agent
	tools     []Tool
	chatModel *llm.Model

	// pendingToolSteps 是崩溃时没走完、且**允许重放**的工具步骤。
	// 恢复时先直接重跑它们（不经过模型），把结果补齐再交给模型继续。
	pendingToolSteps []*Step

	// interruptedLLMSteps 是崩溃时正在生成的那一轮。它没有可重放的东西
	// （模型重新生成就是），恢复时标 interrupted——"不假装成功"。
	interruptedLLMSteps []*Step

	// priorTurns 已经完成、可以直接拼进历史的工具调用。
	priorTurns []resumeTurn
}

// PrepareResume 做恢复前的全部校验，通过则返回一个可以直接执行的计划。
//
// 每一道拒绝的理由都写在返回值里，API 层按 sentinel 给出对应的 type
// （platform.ErrConflict / ErrStateSchemaVersionMismatch /
// ErrToolEffectApplied / ErrReplayUnsafe）。
func (u *Usecase) PrepareResume(ctx context.Context, runID uuid.UUID) (*ResumePlan, error) {
	run, err := u.repo.GetRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", runID, err)
	}

	// 【只有 interrupted 可以恢复】ADR-007 决策：终态（completed / failed /
	// cancelled）没有可恢复的东西，failed 重放只会以同样的方式再失败一次
	// ——那消耗的是用户自己配的额度；running 说明它正在跑，恢复它等于让它
	// 跑两遍。状态机的出边就是这条规则的编码（model.go 的 runTransitions）。
	if run.Status != RunInterrupted {
		if run.Status.IsTerminal() {
			return nil, fmt.Errorf("run %s has already finished as %s: %w", runID, run.Status, platform.ErrConflict)
		}
		return nil, fmt.Errorf("only interrupted runs can be resumed, run %s is %s: %w",
			runID, run.Status, platform.ErrConflict)
	}

	// 版本校验（issue #65）：快照是旧代码写的、结构已经变了，就不能硬着头皮
	// 反序列化。这一条和"recover 不了就报错"的区别在于——它给的是一个
	// **有类型的**错误，API 层能把它翻译成"请重新发起一次运行"。
	if run.StateSchemaVersion != CurrentStateSchemaVersion {
		return nil, fmt.Errorf(
			"run %s was checkpointed with state schema version %d but this build speaks version %d; "+
				"refusing to resume: %w",
			runID, run.StateSchemaVersion, CurrentStateSchemaVersion, platform.ErrStateSchemaVersionMismatch)
	}

	steps, err := u.repo.StepsByRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("load steps of run %s: %w", runID, err)
	}

	ag, err := u.repo.GetAgent(ctx, u.db, run.AgentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %s of run %s: %w", run.AgentID, runID, err)
	}

	tools := make([]Tool, 0, len(ag.ToolNames))
	for _, name := range ag.ToolNames {
		t, err := u.tools.Get(name)
		if err != nil {
			// Agent 的工具集在崩溃之后被改过（删了工具或换了实现）。
			// 恢复一条引用了不存在工具的 run 只会走到"执行时才发现"，
			// 提前拒绝更清楚。
			return nil, fmt.Errorf("tool %q of agent %s is no longer registered, cannot resume run %s: %w",
				name, ag.ID, runID, platform.ErrConflict)
		}
		tools = append(tools, t)
	}

	chatModel, err := u.registry.ActiveModel(ctx, llm.KindChat)
	if err != nil {
		return nil, fmt.Errorf("resolve active chat model: %w", err)
	}
	if len(ag.ToolNames) > 0 {
		if err := requireToolCapability(chatModel, ag.Name, ag.ToolNames); err != nil {
			return nil, err
		}
	}

	plan := &ResumePlan{run: run, agent: ag, tools: tools, chatModel: chatModel}

	for _, s := range steps {
		switch {
		case s.Status == StepCompleted && s.Type == StepTypeTool:
			plan.priorTurns = append(plan.priorTurns, resumeTurn{
				ToolName: s.ToolName, ToolArgs: s.ToolArgs, ToolResult: s.ToolResult,
			})

		case s.Status == StepCompleted:
			// 已完成的 llm 轮次：没有什么要重建的（见 resumeTurn 的注释）。

		case s.Type == StepTypeTool:
			if err := u.gateToolReplay(ctx, s); err != nil {
				return nil, err
			}
			plan.pendingToolSteps = append(plan.pendingToolSteps, s)

		default:
			// 没走完的 llm 轮次：模型重新生成就是，标 interrupted 即可。
			plan.interruptedLLMSteps = append(plan.interruptedLLMSteps, s)
		}
	}

	return plan, nil
}

// gateToolReplay 判断一步工具调用能不能被自动重放（issue #61）。
//
// 【判据来自 ToolMetadata，不是硬编码的工具名白名单】这是 §9.2 明确要求
// 的：恢复的安全性完全建立在这个字段上，而写成 `switch toolName` 的话，
// 新加一个工具有副作用时没人会想起来改这里。
//
// 三道门，顺序有意：
//
//  1. side_effect_level：WRITE_NON_IDEMPOTENT 直接拒绝——方案 §8 的恢复
//     边界明说了不保证它们被自动安全重放。
//  2. retry_policy：never 也拒绝（它比副作用等级更严格，是工具自己的声明）。
//  3. 效果账本：这一步的副作用**已经记过账**了 → 说明工具跑过了，而我们
//     没有它的结果（有结果的话这一步早就是 completed）。这是崩溃表里
//     "工具跑了但结果没落库"**可区分**的那一半，必须拒绝——重放它才是
//     真正的重复执行。
func (u *Usecase) gateToolReplay(ctx context.Context, s *Step) error {
	t, err := u.tools.Get(s.ToolName)
	if err != nil {
		return fmt.Errorf("tool %q of interrupted step %s is not registered: %w", s.ToolName, s.ID, platform.ErrConflict)
	}
	meta := t.Metadata()

	if meta.SideEffectLevel == WriteNonIdempotent {
		return fmt.Errorf(
			"step %s calls %q which is WRITE_NON_IDEMPOTENT; the platform will not replay it automatically: %w",
			s.ID, s.ToolName, platform.ErrReplayUnsafe)
	}
	if meta.RetryPolicy == RetryNever {
		return fmt.Errorf(
			"step %s calls %q whose retry policy is never; the platform will not replay it automatically: %w",
			s.ID, s.ToolName, platform.ErrReplayUnsafe)
	}

	applied, err := u.repo.ToolEffectApplied(ctx, u.db, s.ID, EffectKey(s.ToolName, s.ToolArgs))
	if err != nil {
		return fmt.Errorf("check tool effect of step %s: %w", s.ID, err)
	}
	if applied {
		return fmt.Errorf(
			"step %s already applied the effect of %q but its result was not recorded; "+
				"replaying it would execute the tool a second time: %w",
			s.ID, s.ToolName, platform.ErrToolEffectApplied)
	}
	return nil
}

// Resume 执行一次恢复。
//
// 步骤与 §9.3 的语义一一对应：
//  1. 没走完的 llm 轮次标 interrupted（不假装成功）；
//  2. 允许重放的工具步骤**用同一个 step 行**重跑（同一行是关键：效果账本
//     的键是 (step_id, effect_key)，换一行就等于绕开了那道判据）；
//  3. 重建历史，交给模型从下一个 Step 继续。
func (u *Usecase) Resume(ctx context.Context, plan *ResumePlan, sink conversation.EventSink) (*Run, error) {
	// interrupted → running 的 CAS（issue #59 的状态机）。失败说明并发地
	// 有另一次恢复或一次取消先动了它。
	if err := u.repo.UpdateRunStatus(ctx, u.db, plan.run.ID, RunInterrupted, RunRunning); err != nil {
		return nil, fmt.Errorf("claim run %s for resume: %w", plan.run.ID, err)
	}
	plan.run.Status = RunRunning

	ctx = platform.WithAgentRunID(ctx, plan.run.ID.String())
	log := u.log(ctx)
	log.Info("resuming agent run",
		"replay_tool_steps", len(plan.pendingToolSteps),
		"interrupted_llm_steps", len(plan.interruptedLLMSteps),
		"prior_turns", len(plan.priorTurns))

	// ① 没走完的 llm 轮次标 interrupted——项目文档 §9.1 的"不假装成功"。
	for _, s := range plan.interruptedLLMSteps {
		s.Status = StepInterrupted
		if s.Error == "" {
			s.Error = "进程在这次生成完成之前退出"
		}
		if err := u.repo.UpdateStep(ctx, u.db, s); err != nil {
			return nil, fmt.Errorf("mark step %s interrupted: %w", s.ID, err)
		}
	}

	// ② 重放没走完的工具步骤。用同一个 step 行 + 同一个 effect_key，
	// 所以"重复执行"会被 tool_effect_log 的唯一约束抓住（issue #63）。
	turns := append([]resumeTurn(nil), plan.priorTurns...)
	for _, s := range plan.pendingToolSteps {
		turn, err := u.replayToolStep(ctx, s)
		if err != nil {
			// 重放失败：这一步没恢复成功，run 落 failed（不是 interrupted
			// ——它已经尝试过了，再标 interrupted 会让用户以为还能再恢复一次
			// 而结果只会一样）。
			_ = u.repo.UpdateRunStatus(ctx, u.db, plan.run.ID, RunRunning, RunFailed)
			return nil, err
		}
		turns = append(turns, turn)
	}

	// ③ 交给模型继续。
	run, err := u.execute(ctx, &runPlan{
		agent:     plan.agent,
		tools:     plan.tools,
		chatModel: plan.chatModel,
		input:     plan.run.Input,
	}, plan.run, turns, sink)
	if err != nil {
		// execute 内部已经写过终态与 error 事件，这里只把错误带出去记日志。
		u.log(ctx).Warn("resumed run ended with an error", "error", err)
	}
	return run, err
}

// replayToolStep 直接重跑一个没走完的工具步骤（不经过模型）。
//
// 【为什么直接跑，而不是让模型再决定一次】§9.3 的"从下一个 Step 重放"说的
// 就是这一步：我们**知道**它该跑什么（tool_name 与 tool_args 都在库里），
// 让模型重新决定一次会引入一个不必要的随机性——它可能换一个工具、
// 换一组参数，那恢复出来的轨迹和原来那条就不是一回事了。
func (u *Usecase) replayToolStep(ctx context.Context, s *Step) (resumeTurn, error) {
	t, err := u.tools.Get(s.ToolName)
	if err != nil {
		return resumeTurn{}, fmt.Errorf("tool %q of step %s: %w", s.ToolName, s.ID, err)
	}

	started := time.Now()
	result, err := t.Invoke(ctx, s.ToolArgs)
	if err != nil {
		s.Status = StepFailed
		s.Error = err.Error()
		s.LatencyMS = int(time.Since(started).Milliseconds())
		_ = u.repo.UpdateStep(ctx, u.db, s)
		return resumeTurn{}, fmt.Errorf("replay tool %q of step %s: %w", s.ToolName, s.ID, err)
	}

	// 先记账本再写结果——与正常执行路径同一顺序，同一套判据
	// （见 consumeEvents 里那段注释）。
	if err := u.repo.RecordToolEffect(ctx, u.db, s.ID, EffectKey(s.ToolName, s.ToolArgs)); err != nil {
		if errors.Is(err, platform.ErrToolEffectApplied) {
			// 检查与执行之间被另一个恢复抢先了。
			return resumeTurn{}, fmt.Errorf("step %s was replayed concurrently: %w", s.ID, platform.ErrToolEffectApplied)
		}
		u.log(ctx).Error("failed to record replayed tool effect",
			"run_id", s.RunID, "step_id", s.ID, "error", err)
	}

	s.Status = StepCompleted
	s.ToolResult = result
	s.LatencyMS = int(time.Since(started).Milliseconds())
	if err := u.repo.UpdateStep(ctx, u.db, s); err != nil {
		return resumeTurn{}, fmt.Errorf("finalize replayed step %s: %w", s.ID, err)
	}

	return resumeTurn{ToolName: s.ToolName, ToolArgs: s.ToolArgs, ToolResult: result}, nil
}
