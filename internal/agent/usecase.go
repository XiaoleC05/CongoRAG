package agent

import (
	"context"
	"encoding/json"
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
		_ = u.emitEvent(sink, *eventID, "error", map[string]string{
			"type": "internal_error", "detail": err.Error(),
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

	chatModelID, err := u.registry.ActiveModelID(ctx, llm.KindChat)
	if err != nil {
		return nil, fmt.Errorf("resolve active chat model: %w", err)
	}

	now := time.Now()
	run := &Run{
		ID: uuid.New(), AgentID: agentID, Status: RunRunning, Input: input,
		StateSchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := u.repo.InsertRun(ctx, u.db, run); err != nil {
		return nil, fmt.Errorf("insert run: %w", err)
	}

	events, err := runAgent(ctx, u.registry, chatModelID, ag, tools, input)
	if err != nil {
		u.failRun(ctx, run.ID)
		return run, fmt.Errorf("start agent run: %w", err)
	}

	output, runErr := u.consumeEvents(ctx, run.ID, events, sink, eventID)
	run.Output = output

	if runErr != nil {
		u.failRun(ctx, run.ID)
		return run, runErr
	}

	if err := u.repo.UpdateRunStatus(ctx, u.db, run.ID, RunRunning, RunCompleted); err != nil {
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

	// checkpoint 落一次 Step 边界的快照——CheckpointStore.Save 的唯一
	// 调用点（见 port.go 的注释：这一轮只有 Save 真正被使用,Load 留给
	// M4-C 的 Resume)。state_snapshot 这一轮传 nil：AgentState 的具体
	// 结构是 M4-C 恢复逻辑要用的东西,现在没有真实调用点会读它,不提前
	// 猜一个序列化格式。
	checkpoint := func() {
		_ = u.cp.Save(ctx, u.db, runID, seq, output.String(), nil)
	}

	closeLLMStep := func(status StepStatus, errMsg string) {
		if !llmStepOpen {
			return
		}
		seq++
		_ = u.repo.InsertStep(ctx, u.db, &Step{
			ID: uuid.New(), RunID: runID, Seq: seq, Type: StepTypeLLM, Status: status,
			LatencyMS: int(time.Since(llmStepStarted).Milliseconds()), Error: errMsg, CreatedAt: time.Now(),
		})
		llmStepOpen = false
		checkpoint()
	}

	for event := range events {
		switch event.kind {
		case adkEventToken:
			if !llmStepOpen {
				llmStepStarted = time.Now()
				llmStepOpen = true
			}
			output.WriteString(event.text)
			*eventID++
			if err := u.emitEvent(sink, *eventID, "token", map[string]string{"text": event.text}); err != nil {
				return output.String(), err
			}

		case adkEventToolCall:
			closeLLMStep(StepCompleted, "")
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
			_ = u.repo.InsertStep(ctx, u.db, &Step{
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

		case adkEventError:
			closeLLMStep(StepFailed, event.err.Error())
			return output.String(), fmt.Errorf("agent run failed: %w", event.err)

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

func (u *Usecase) failRun(ctx context.Context, runID uuid.UUID) {
	_ = u.repo.UpdateRunStatus(ctx, u.db, runID, RunRunning, RunFailed)
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
