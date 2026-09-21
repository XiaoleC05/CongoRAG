package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// fakeRepo 只实现这个文件的测试真正会调用的那几个方法（CreateAgent 的
// 落库、InsertStep、UpdateRunStatus）——其余方法留 panic 实现,比默默返回
// 零值更容易在测试写错时被发现。
type fakeRepo struct {
	mu    sync.Mutex
	steps []*Step
	// stepCtxErrs 和 steps 一一对应，记下每次 insertStep 拿到的 ctx 的
	// 状态——钉住"断开之后补写的 Step 不跟着请求 ctx 一起死"（issue #14）。
	stepCtxErrs []error
	agents      []*Agent
	runs        []*Run
	updates     []statusUpdate
}

// statusUpdate 记一次 UpdateRunStatus 调用，含当时 ctx 是否已被取消。
type statusUpdate struct {
	id     uuid.UUID
	from   RunStatus
	to     RunStatus
	ctxErr error
}

func newFakeRepo() *fakeRepo { return &fakeRepo{} }

func (f *fakeRepo) CreateAgent(ctx context.Context, q platform.Querier, a *Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = append(f.agents, a)
	return nil
}
func (f *fakeRepo) GetAgent(ctx context.Context, q platform.Querier, id uuid.UUID) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.agents {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("agent %s: %w", id, platform.ErrNotFound)
}
func (f *fakeRepo) ListAgents(ctx context.Context, q platform.Querier) ([]*Agent, error) {
	panic("not used")
}
func (f *fakeRepo) InsertRun(ctx context.Context, q platform.Querier, r *Run) error {
	// 【为什么记录而不是 panic】门控那条测试要断言的正是「InsertRun 没被
	// 调用」——记录成切片之后失败信息是"期望 0 条，实际 1 条"，而 panic
	// 只会给出一句"not used"，看不出是哪个不变式破了。
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, r)
	return nil
}
func (f *fakeRepo) GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error) {
	panic("not used")
}
func (f *fakeRepo) ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID) ([]*Run, error) {
	panic("not used")
}
func (f *fakeRepo) UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, statusUpdate{id: id, from: from, to: to, ctxErr: ctx.Err()})
	return nil
}
func (f *fakeRepo) InsertStep(ctx context.Context, q platform.Querier, s *Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, s)
	f.stepCtxErrs = append(f.stepCtxErrs, ctx.Err())
	return nil
}
func (f *fakeRepo) StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*Step, error) {
	panic("not used")
}
func (f *fakeRepo) ListToolCatalog(ctx context.Context, q platform.Querier) ([]ToolCatalogEntry, error) {
	panic("not used")
}

// fakeSink 记录每一次 Emit 调用,不真的写 HTTP 响应。
type fakeSink struct {
	mu     sync.Mutex
	events []conversation.Event
	done   chan struct{}
}

func newFakeSink() *fakeSink { return &fakeSink{done: make(chan struct{})} }

func (s *fakeSink) Emit(ev conversation.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}
func (s *fakeSink) Flush() error          { return nil }
func (s *fakeSink) Done() <-chan struct{} { return s.done }

// fakeCheckpointStore 只记住最后一次 Save 的参数,不真的连数据库。
type fakeCheckpointStore struct {
	mu         sync.Mutex
	saveCalls  int
	lastStep   int
	lastOutput string
	lastCtxErr error
}

func (f *fakeCheckpointStore) Save(ctx context.Context, q platform.Querier, runID uuid.UUID, step int, output string, snap json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls++
	f.lastStep, f.lastOutput, f.lastCtxErr = step, output, ctx.Err()
	return nil
}

func (f *fakeCheckpointStore) Load(ctx context.Context, q platform.Querier, runID uuid.UUID) (*Run, error) {
	panic("not used")
}

func newTestUsecaseForConsumeEvents() (*Usecase, *fakeRepo) {
	u, repo, _ := newTestUsecaseWithCheckpoint()
	return u, repo
}

// newTestUsecaseWithCheckpoint 多返回一个 cp——断言 checkpoint 拿到的 ctx
// 时要用（newTestUsecaseForConsumeEvents 把它藏起来了）。
func newTestUsecaseWithCheckpoint() (*Usecase, *fakeRepo, *fakeCheckpointStore) {
	repo := newFakeRepo()
	cp := &fakeCheckpointStore{}
	return &Usecase{repo: repo, cp: cp}, repo, cp
}

// ════════════════════════════════════════════════════════════════
// consumeEvents：Eino 事件流 → SSE + agent_run_steps 的映射逻辑
// ════════════════════════════════════════════════════════════════

func TestConsumeEvents_TokensAccumulateIntoOutput(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "3 个 128 的和是 "}
	events <- adkEvent{kind: adkEventToken, text: "384，乘以 2 是 768。"}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	output, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)

	require.NoError(t, err)
	assert.Equal(t, "3 个 128 的和是 384，乘以 2 是 768。", output)
}

// token → done 之间没有工具调用时,只应该落一个 llm 类型的 Step
// （覆盖从第一个 token 到 done 之间的这一整段生成),不是每个 token
// 单独一行。
func TestConsumeEvents_TokensOnly_ProducesOneLLMStep(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "a"}
	events <- adkEvent{kind: adkEventToken, text: "b"}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)

	require.Len(t, repo.steps, 1)
	assert.Equal(t, StepTypeLLM, repo.steps[0].Type)
	assert.Equal(t, StepCompleted, repo.steps[0].Status)
}

// tool_call → tool_result 这一对必须落成同一个 Step（用 toolCallID
// 关联),不是两行——ToolArgs 来自 tool_call 事件,ToolResult 来自
// tool_result 事件,合并进一行才对应"一次工具调用"这个业务概念。
//
// 这次调用之前那一行 llm 是模型请求工具的轮次（issue #33）：只有 tool 一行
// 的话，轨迹里看起来"模型什么都没做"就把工具跑了一遍。
func TestConsumeEvents_ToolCallAndResult_MergeIntoOneStep(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-1", toolName: "calculator", toolArgs: json.RawMessage(`{"a":128,"b":3,"operator":"*"}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-1", toolName: "calculator", toolResult: json.RawMessage(`{"result":384}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)

	require.Len(t, repo.steps, 2)
	assert.Equal(t, StepTypeLLM, repo.steps[0].Type, "请求工具的那一轮模型生成也要有一行")
	step := repo.steps[1]
	assert.Equal(t, StepTypeTool, step.Type)
	assert.Equal(t, "calculator", step.ToolName)
	assert.JSONEq(t, `{"a":128,"b":3,"operator":"*"}`, string(step.ToolArgs))
	assert.JSONEq(t, `{"result":384}`, string(step.ToolResult))
}

// 【这条是 #33 的回归测试：一次并行工具调用只算一轮】模型一次可以并行请求
// 多个工具，eino_adk 会为每一个调用各发一个 tool_call 事件。每个事件都
// open+close 一次的话，一次并行调用就写出两条 llm 行——轨迹页上"模型跑了几轮"
// 和每轮的 LatencyMS 都跟着错。llm 行对应的是"一轮生成"，不是"一次工具调用"。
func TestConsumeEvents_ParallelToolCalls_ProduceSingleLLMStep(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-1", toolName: "calculator", toolArgs: json.RawMessage(`{"a":1,"b":2,"operator":"+"}`)}
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-2", toolName: "calculator", toolArgs: json.RawMessage(`{"a":3,"b":4,"operator":"+"}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-1", toolName: "calculator", toolResult: json.RawMessage(`{"result":3}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-2", toolName: "calculator", toolResult: json.RawMessage(`{"result":7}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)

	var llmSteps, toolSteps int
	for _, s := range repo.steps {
		switch s.Type {
		case StepTypeLLM:
			llmSteps++
		case StepTypeTool:
			toolSteps++
		}
	}
	assert.Equal(t, 1, llmSteps, "同一轮里的两个并行工具调用只该有一条 llm 行")
	assert.Equal(t, 2, toolSteps, "两个工具调用各自一条 tool 行")
}

// 工具结果交回模型之后是新的一轮：那一轮再请求工具时必须重新开一条 llm 行，
// 不能被上面那个"同一轮只记一次"的判据吃掉。
func TestConsumeEvents_SecondRoundAfterToolResult_GetsOwnLLMStep(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-1", toolName: "calculator", toolArgs: json.RawMessage(`{"a":1,"b":2,"operator":"+"}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-1", toolName: "calculator", toolResult: json.RawMessage(`{"result":3}`)}
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-2", toolName: "calculator", toolArgs: json.RawMessage(`{"a":3,"b":4,"operator":"+"}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-2", toolName: "calculator", toolResult: json.RawMessage(`{"result":7}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)

	// 两轮生成（都不带正文、都直接请求工具）→ llm, tool, llm, tool
	require.Len(t, repo.steps, 4)
	assert.Equal(t,
		[]string{StepTypeLLM, StepTypeTool, StepTypeLLM, StepTypeTool},
		[]string{repo.steps[0].Type, repo.steps[1].Type, repo.steps[2].Type, repo.steps[3].Type})
}

// 【这条是 #34 的回归测试，但只有第一个用例真的在钉它】
//
// 值得把这个案子的结论写下来，因为验证阶段对它的描述是夸大的：那份报告
// 说"工具自己的失败（ErrNotFound 之类）也被报成 upstream_llm_error"。
// 实际上不会——errors.Is 对 errors.Join 出来的错误逐个成员判断，而
// platform.SSEErrorType 的顺序是 ErrInvalid → ErrNotFound → ErrUpstream，
// 所以带 ErrNotFound 的工具错误本来就分类成 not_found。那两个用例因此
// **改前改后都通过**，它们的价值是"防止将来有人把归类改成无条件包一层"
// 或者去动 SSEErrorType 的顺序，不是证明今天修了什么。
//
// 真正变了的是 ctx 取消那一档：context.Canceled 不在 SSEErrorType 认的
// 三个 sentinel 里，被 Join 进 ErrUpstream 之后整个错误变成
// upstream_llm_error——客户端断开引发的收尾被说成"上游模型服务出错"。
func TestConsumeEvents_ErrorEvent_TypeMapping(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantType  string
		whyWanted string
	}{
		{
			name:      "ctx 取消不该被说成上游模型故障",
			err:       fmt.Errorf("stream aborted: %w", context.Canceled),
			wantType:  "internal_error",
			whyWanted: "请求 ctx 取消是客户端断开引发的收尾，不是 provider 的问题",
		},
		{
			name:      "工具层的 not_found 不能被压成上游故障",
			err:       fmt.Errorf("get knowledge base: %w", platform.ErrNotFound),
			wantType:  "not_found",
			whyWanted: "带 sentinel 的错误要盖过上游那一档，客户端才知道是 id 不对",
		},
		{
			name:      "参数级失败保持 invalid_argument",
			err:       fmt.Errorf("tool args: %w", platform.ErrInvalid),
			wantType:  "invalid_argument",
			whyWanted: "同一个错误类型在 REST 和 SSE 两条流上必须一致",
		},
		{
			name:      "本来就没有 sentinel 的裸错误才归到上游",
			err:       errors.New("dial tcp: connection refused"),
			wantType:  "upstream_llm_error",
			whyWanted: "ADK 事件流上的裸错误基本都来自 provider",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newTestUsecaseForConsumeEvents()
			sink := newFakeSink()
			events := make(chan adkEvent, 4)
			events <- adkEvent{kind: adkEventError, err: tc.err}
			close(events)

			var eventID int64
			_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)

			require.Error(t, err)
			assert.Equal(t, tc.wantType, platform.SSEErrorType(err),
				tc.whyWanted+"; SSE 的 error.type 和 REST 的 Problem.type 是同一套枚举")
		})
	}
}

// 开发文档 §7.1 的例子本身：一次 llm 生成决定调用 calculator,工具
// 执行完,模型再生成一次最终回答——三个 Step,顺序必须是 llm → tool → llm。
func TestConsumeEvents_FullReactCycle_StepOrderIsCorrect(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	// 第一轮:模型直接请求调用工具,没有先输出任何 token。
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "call-1", toolName: "calculator", toolArgs: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "call-1", toolName: "calculator", toolResult: json.RawMessage(`{"result":768}`)}
	// 第二轮:模型看到工具结果,生成最终文字回答。
	events <- adkEvent{kind: adkEventToken, text: "结果是 768。"}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	output, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)
	assert.Equal(t, "结果是 768。", output)

	require.Len(t, repo.steps, 3, "纯工具调用轮次也必须落一行 llm（issue #33）")
	assert.Equal(t, StepTypeLLM, repo.steps[0].Type, "第一轮：模型请求调用工具")
	assert.Equal(t, StepTypeTool, repo.steps[1].Type)
	assert.Equal(t, StepTypeLLM, repo.steps[2].Type, "第二轮：模型生成最终回答")
	for i, s := range repo.steps {
		assert.Equal(t, i+1, s.Seq, "seq 按落库顺序连续，不跳号")
	}
}

func TestConsumeEvents_EventIDsAreMonotonicallyIncreasing(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "a"}
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "c1", toolName: "calculator", toolArgs: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "c1", toolResult: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)
	require.NoError(t, err)

	require.Len(t, sink.events, 4)
	for i, ev := range sink.events {
		assert.Equal(t, int64(i+1), ev.ID)
	}
}

func TestConsumeEvents_ErrorEvent_StopsAndReturnsError(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "开始生成"}
	events <- adkEvent{kind: adkEventError, err: errors.New("upstream boom")}
	close(events)

	var eventID int64
	output, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream boom")
	assert.Equal(t, "开始生成", output, "已经生成的部分文本应该保留,不因为后续失败而丢弃")

	require.Len(t, repo.steps, 1, "未完成的 llm step 应该被标记失败并落库,不是悄悄丢弃")
	assert.Equal(t, StepFailed, repo.steps[0].Status)
}

// 没有 done 事件、channel 就直接关闭（比如上游异常退出)时必须报错,
// 不能假装成功——docs/sse-protocol.md 的 done 事件是"生成正常结束"的
// 唯一信号,缺了它不该被解释成成功。
func TestConsumeEvents_ChannelClosedWithoutDone_ReturnsError(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "x"}
	close(events)

	var eventID int64
	_, err := u.consumeEvents(context.Background(), uuid.New(), events, sink, &eventID)

	require.Error(t, err)
}

// 客户端断开时请求 ctx 已被取消，断开那一刻要补写的 Step 和 checkpoint
// 不能跟着它一起死：pgx 拿着已取消的 ctx 连连接都拿不到，轨迹会恰好断在
// 最需要留痕的那一刻（issue #14）。
func TestConsumeEvents_WritesStepAndCheckpointWithDetachedContext(t *testing.T) {
	u, repo, cp := newTestUsecaseWithCheckpoint()
	sink := newFakeSink()
	events := make(chan adkEvent, 10)
	events <- adkEvent{kind: adkEventToken, text: "生成到一半"}
	events <- adkEvent{kind: adkEventError, err: errors.New("客户端断开")}
	close(events)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel() // net/http 在客户端断开时取消请求 ctx

	var eventID int64
	_, err := u.consumeEvents(reqCtx, uuid.New(), events, sink, &eventID)
	require.Error(t, err)

	require.Len(t, repo.steps, 1, "未完成的那一轮要标失败落库")
	require.Len(t, repo.stepCtxErrs, 1)
	assert.NoError(t, repo.stepCtxErrs[0], "落 Step 用的 ctx 必须脱离请求生命周期")
	assert.Equal(t, StepFailed, repo.steps[0].Status)
	assert.NoError(t, cp.lastCtxErr, "checkpoint 同样要脱离请求生命周期")
}

// 失败状态必须真的写下去：用请求 ctx 做 CAS 的话，客户端一断开 UPDATE
// 就不会执行，run 行永久停在 'running'（issue #14）。同时 ctx 已取消说明
// 是客户端走了，记 'interrupted' 而不是服务端故障 'failed'。
func TestFailRun_CancelledRequestContext_StillWritesInterrupted(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	runID := uuid.New()
	require.NoError(t, u.failRun(reqCtx, runID))

	require.Len(t, repo.updates, 1)
	assert.Equal(t, runID, repo.updates[0].id)
	assert.Equal(t, RunRunning, repo.updates[0].from)
	assert.Equal(t, RunInterrupted, repo.updates[0].to)
	assert.NoError(t, repo.updates[0].ctxErr, "写库用的 ctx 必须脱离请求生命周期")
}

// ctx 还活着（真正的运行期失败）时仍然记 failed，不要一律记成 interrupted。
func TestFailRun_LiveRequestContext_MarksFailed(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()

	require.NoError(t, u.failRun(context.Background(), uuid.New()))

	require.Len(t, repo.updates, 1)
	assert.Equal(t, RunFailed, repo.updates[0].to)
	assert.NoError(t, repo.updates[0].ctxErr)
}

// ════════════════════════════════════════════════════════════════
// Start：SSE error 帧的 type 与 CreateAgent 的入参归一化
// ════════════════════════════════════════════════════════════════

// detachedWriteCtx 是断开后所有收尾写入共用的 ctx：必须摘掉取消信号（否则
// 那些写入全部落空）、保留请求值，并且有上界——脱开而不是无界。
func TestDetachedWriteCtx_IgnoresParentCancellation(t *testing.T) {
	type ctxKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "req-1"))
	cancel()

	ctx, cancelWrite := detachedWriteCtx(parent)
	defer cancelWrite()

	assert.NoError(t, ctx.Err(), "父 ctx 已取消，写入 ctx 必须仍然可用")
	assert.Equal(t, "req-1", ctx.Value(ctxKey{}), "请求值要保留，只是摘掉取消信号")
	deadline, ok := ctx.Deadline()
	require.True(t, ok, "必须有上界，不能无界地活")
	assert.WithinDuration(t, time.Now().Add(finalizeTimeout), deadline, time.Second)
}

// 空输入的 error 帧 type 必须是 invalid_argument——前端按这套枚举选文案，
// 硬编码 internal_error 会把客户端自己能纠正的输入说成"服务内部错误"
// （issue #34）。空输入在查 agent 之前就被挡下，不碰 repo。
func TestStart_InvalidInput_EmitsMappedErrorType(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()

	_, err := u.Start(context.Background(), uuid.New(), "   ", sink)
	require.ErrorIs(t, err, platform.ErrInvalid)

	require.Len(t, sink.events, 1)
	assert.Equal(t, "error", sink.events[0].Type)

	var envelope struct {
		Type string `json:"type"`
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(sink.events[0].Payload, &envelope))
	assert.Equal(t, "invalid_argument", envelope.Data.Type)
	assert.Contains(t, envelope.Data.Detail, "must not be empty")
}

// toolNames 在契约里是可选项：nil slice 会被 pgx 编码成 SQL NULL，而
// agents.tool_names 是 NOT NULL，INSERT 会被 23502 顶回来变成 500
// （issue #18）。业务层必须归一成空数组。
func TestCreateAgent_NilToolNames_NormalisedToEmptySlice(t *testing.T) {
	repo := newFakeRepo()
	u := &Usecase{repo: repo, tools: NewToolRegistry()}

	a, err := u.CreateAgent(context.Background(), "计算助手", "", "", nil)
	require.NoError(t, err)

	assert.NotNil(t, a.ToolNames, "nil slice 绑定成 SQL NULL，撞上 tool_names NOT NULL")
	assert.Empty(t, a.ToolNames)
	require.Len(t, repo.agents, 1)
	assert.NotNil(t, repo.agents[0].ToolNames, "落库的那份也要是非 nil 空数组")
}
