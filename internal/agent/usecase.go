package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	db       platform.Querier
}

func NewUsecase(repo Repo, cp CheckpointStore, tools Registry, registry llm.Registry, db platform.Querier) *Usecase {
	return &Usecase{repo: repo, cp: cp, tools: tools, registry: registry, db: db}
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

func (u *Usecase) ListRuns(ctx context.Context, agentID uuid.UUID) ([]*Run, error) {
	runs, err := u.repo.ListRunsByAgent(ctx, u.db, agentID)
	if err != nil {
		return nil, fmt.Errorf("list runs of agent %s: %w", agentID, err)
	}
	return runs, nil
}

func (u *Usecase) ListSteps(ctx context.Context, runID uuid.UUID) ([]*Step, error) {
	steps, err := u.repo.StepsByRun(ctx, u.db, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps of run %s: %w", runID, err)
	}
	return steps, nil
}

// Start 执行一次 Agent 运行,把 Eino ADK 的事件流归一化成 SSE 推给
// sink,同时把每一步落成 agent_run_steps 的一行。
//
// 【和 conversation.Usecase.Send 的"任何失败先推 error 事件"是同一个
// 模式】handler 一旦为这次请求打开了 SSE 连接,就不能再靠改 HTTP
// 状态码告诉客户端"这次失败了"——docs/sse-protocol.md 定义的 error
// 事件同样服务这里。
func (u *Usecase) Start(ctx context.Context, agentID uuid.UUID, input string, sink conversation.EventSink) (*Run, error) {
	// eventID 由这个方法从 0 开始持有,一路传给 start/consumeEvents——
	// 保证不管失败发生在哪个阶段（Run 还没插入、consumeEvents 已经
	// 发出去几个 token 之后),最终这一条 error 事件的 id 永远比之前
	// 任何一个已发出的事件大,不会在同一个 SSE 流里出现重复或倒退的 id。
	eventID := new(int64)
	run, err := u.start(ctx, agentID, input, sink, eventID)
	if err != nil {
		*eventID++
		// type 走全项目共享的那套枚举（platform.SSEErrorType）：空输入是
		// invalid_argument、agent 不存在是 not_found、上游模型失败是
		// upstream_llm_error。以前这里硬编码 internal_error，把客户端
		// 自己能纠正的错误说成服务端故障，前端也只能显示"服务内部错误"
		// （issue #34）。
		_ = u.emitEvent(sink, *eventID, "error", map[string]string{
			"type": platform.SSEErrorType(err), "detail": err.Error(),
		})
	}
	return run, err
}

func (u *Usecase) start(ctx context.Context, agentID uuid.UUID, input string, sink conversation.EventSink, eventID *int64) (*Run, error) {
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
	//
	// 【必须在 InsertRun 之前】被拒的这一轮不该在 agent_runs 里留下一条
	// running 行，否则历史列表里会出现一次用户从没见过的失败运行，还要靠
	// failRun 去补写终态。
	if len(ag.ToolNames) > 0 {
		if err := requireToolCapability(chatModel, ag.Name, ag.ToolNames); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	run := &Run{
		ID: uuid.New(), AgentID: agentID, Status: RunRunning, Input: input,
		StateSchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := u.repo.InsertRun(ctx, u.db, run); err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}

	// runCtx 是这次运行自己的生命周期。consumeEvents 一旦返回——正常结束、
	// 出错、或者客户端断开——它就必须结束：drainIterator 的每一次发送都
	// select 这个 ctx，只有取消能让生产者从"没有接收者的通道"上退出来
	// （issue #35 的 goroutine/iterator 泄漏）。取消后 ADK 那边的模型请求
	// 也会被一起拆掉。
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	events, err := runAgent(runCtx, u.registry, chatModel.ID.String(), ag, tools, input)
	if err != nil {
		if ferr := u.failRun(ctx, run.ID); ferr != nil {
			return run, errors.Join(fmt.Errorf("start agent run: %w", err), ferr)
		}
		return run, fmt.Errorf("start agent run: %w", err)
	}

	output, runErr := u.consumeEvents(ctx, run.ID, events, sink, eventID)
	run.Output = output
	// 消费端已经退出，通知生产者收摊。
	cancelRun()

	if runErr != nil {
		// failRun 的错误不能丢：以前这里是 `_ =`，失败状态写不进去这件事
		// 完全不可见（issue #14）。并进返回值里，server.go 会记成日志。
		if ferr := u.failRun(ctx, run.ID); ferr != nil {
			return run, errors.Join(runErr, ferr)
		}
		return run, runErr
	}

	// 成功终态同样用脱离请求的 ctx：done 事件发出之后客户端立刻断开是
	// 常事（关标签页），跟着请求 ctx 走的话这一条 CAS 会失败——而且这条
	// 路径不会走 failRun，run 行会直接停在 'running'（issue #14）。
	wctx, cancelWrite := detachedWriteCtx(ctx)
	defer cancelWrite()
	if err := u.repo.UpdateRunStatus(wctx, u.db, run.ID, RunRunning, RunCompleted); err != nil {
		return run, fmt.Errorf("mark run %s completed: %w", run.ID, err)
	}
	run.Status = RunCompleted
	return run, nil
}

// pendingCall 追踪一次尚未收到结果的工具调用——tool_call 和
// tool_result 是两个独立的事件,中间可能夹着其它工具调用（模型一次性
// 请求多个工具时),用 toolCallID 关联,不能假设"上一条 tool_call
// 对应下一条 tool_result"这种 FIFO 顺序。
type pendingCall struct {
	seq       int
	toolName  string
	toolArgs  json.RawMessage
	startedAt time.Time
}

// consumeEvents 排空 adkEvent 流,每步落一行 agent_run_steps,把事件
// 转成 SSE 推给 sink,返回累积的最终文本输出。
//
// 【eventID 是调用方持有的单调计数器,不问数据库要号】Agent 运行这
// 一轮没有会话那样的持久化事件表/断线重订阅（M4-B 才做,见计划文档),
// 一个进程内计数器就够,不需要 conversation.Repo.NextEventID 那一套
// 跨进程重启也要保持单调的机制。用指针而不是返回值传出最终计数,
// 是因为 Start 的最外层 error 事件（这个函数从没跑过、或者跑到中途
// 失败退出之后）也要接着这个计数继续分配,不能各自从零起跳。
func (u *Usecase) consumeEvents(ctx context.Context, runID uuid.UUID, events <-chan adkEvent, sink conversation.EventSink, eventID *int64) (string, error) {
	var output strings.Builder
	seq := 0
	pending := map[string]*pendingCall{}

	var llmStepStarted time.Time
	var llmStepOpen bool

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

	// openLLMStep 在"这一轮的第一个助手事件"上开一条模型轮次记录。
	//
	// 【为什么 token 和 tool_call 都要开】模型不发正文、直接请求调用工具
	// 是 ReAct 的常态；只在 token 上开 step 会让这种轮次一行都不落，
	// 轨迹里看起来"模型什么都没做"就把工具跑了一遍，schema 里
	// type=llm 的定义（migrations/0005_agents.up.sql:66）明确包含
	// "工具调用请求"这一种输出（issue #33）。
	openLLMStep := func() {
		if !llmStepOpen {
			llmStepStarted = time.Now()
			llmStepOpen = true
		}
	}

	// insertStep 落一行 Step。写库用脱离请求生命周期的 ctx（见
	// detachedWriteCtx）：客户端一断开请求 ctx 就被取消，用它 INSERT 会
	// 一条都写不下去，轨迹恰好断在最需要留痕的那一刻（issue #14）。
	insertStep := func(s *Step) {
		wctx, cancel := detachedWriteCtx(ctx)
		defer cancel()
		_ = u.repo.InsertStep(wctx, u.db, s)
	}

	// checkpoint 落一次 Step 边界的快照——CheckpointStore.Save 的唯一
	// 调用点（见 port.go 的注释：这一轮只有 Save 真正被使用,Load 留给
	// M4-C 的 Resume)。state_snapshot 这一轮传 nil：AgentState 的具体
	// 结构是 M4-C 恢复逻辑要用的东西,现在没有真实调用点会读它,不提前
	// 猜一个序列化格式。
	checkpoint := func() {
		wctx, cancel := detachedWriteCtx(ctx)
		defer cancel()
		_ = u.cp.Save(wctx, u.db, runID, seq, output.String(), nil)
	}

	closeLLMStep := func(status StepStatus, errMsg string) {
		if !llmStepOpen {
			return
		}
		seq++
		insertStep(&Step{
			ID: uuid.New(), RunID: runID, Seq: seq, Type: StepTypeLLM, Status: status,
			LatencyMS: int(time.Since(llmStepStarted).Milliseconds()), Error: errMsg, CreatedAt: time.Now(),
		})
		llmStepOpen = false
		checkpoint()
	}

	for event := range events {
		switch event.kind {
		case adkEventToken:
			openLLMStep()
			// 有正文说明这是新一轮生成（上一条 llm 行已经在 tool_call 或
			// done 上收掉了）。
			roundStepClosed = false
			output.WriteString(event.text)
			*eventID++
			if err := u.emitEvent(sink, *eventID, "token", map[string]string{"text": event.text}); err != nil {
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
			pending[event.toolCallID] = &pendingCall{seq: seq, toolName: event.toolName, toolArgs: event.toolArgs, startedAt: time.Now()}
			*eventID++
			if err := u.emitEvent(sink, *eventID, "tool_call", map[string]any{
				"id": event.toolCallID, "name": event.toolName, "args": json.RawMessage(event.toolArgs),
			}); err != nil {
				return output.String(), err
			}

		case adkEventToolResult:
			p, ok := pending[event.toolCallID]
			var stepSeq, latencyMS int
			var toolArgs json.RawMessage
			if ok {
				stepSeq, latencyMS, toolArgs = p.seq, int(time.Since(p.startedAt).Milliseconds()), p.toolArgs
				delete(pending, event.toolCallID)
			} else {
				// Eino 没给出这次调用的 toolCallID（比如某些非流式路径）
				// 时退化成"追加一条新 seq"——宁可序号多一个,不能因为
				// 关联不上就整条丢弃工具的执行结果。
				seq++
				stepSeq = seq
			}
			insertStep(&Step{
				ID: uuid.New(), RunID: runID, Seq: stepSeq, Type: StepTypeTool, Status: StepCompleted,
				ToolName: event.toolName, ToolArgs: toolArgs, ToolResult: event.toolResult,
				LatencyMS: latencyMS, CreatedAt: time.Now(),
			})
			checkpoint()
			*eventID++
			if err := u.emitEvent(sink, *eventID, "tool_result", map[string]any{
				"id": event.toolCallID, "result": json.RawMessage(event.toolResult),
			}); err != nil {
				return output.String(), err
			}
			// 工具结果交回模型，下一轮生成从这里开始——这一轮再出现
			// tool_call 就该是一条新的 llm 行了。
			roundStepClosed = false

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
			*eventID++
			if err := u.emitEvent(sink, *eventID, "done", struct{}{}); err != nil {
				return output.String(), err
			}
			return output.String(), nil
		}
	}
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

// failRun 把 Run 从 running 推进终态。
//
// 【ctx 只用来判断"客户端还在不在"，写库另用一个脱离请求的 ctx】调用它的
// 时刻正是这次运行失败/被中断的同一刻，而 SSE 端点的请求 ctx 在客户端断开
// 时已被取消——照样拿它做 CAS 的话 pgx 连连接都拿不到，UPDATE 一条都不会
// 执行，run 行永久停在 'running'（issue #14）。返回错误而不是丢弃：写失败
// 这件事至少要被调用方带出去记成日志。
//
// 【断开记成 interrupted 而不是 failed】ctx 已取消说明是客户端中途走了，
// 不是服务端故障。RunInterrupted 这一态 model.go 早就定义了，一直没有写入
// 者（contracts/openapi.yaml 的 AgentRunStatus 也认它），历史列表里能一眼
// 看出这次运行是被打断的。
func (u *Usecase) failRun(ctx context.Context, runID uuid.UUID) error {
	to := RunFailed
	if ctx.Err() != nil {
		to = RunInterrupted
	}

	wctx, cancel := detachedWriteCtx(ctx)
	defer cancel()
	if err := u.repo.UpdateRunStatus(wctx, u.db, runID, RunRunning, to); err != nil {
		return fmt.Errorf("mark run %s %s: %w", runID, to, err)
	}
	return nil
}

func (u *Usecase) emitEvent(sink conversation.EventSink, id int64, eventType string, payload any) error {
	body, err := json.Marshal(struct {
		Type string `json:"type"`
		Data any    `json:"data"`
	}{Type: eventType, Data: payload})
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	if err := sink.Emit(conversation.Event{ID: id, Type: eventType, Payload: body}); err != nil {
		return fmt.Errorf("emit event to client: %w", err)
	}
	return sink.Flush()
}
