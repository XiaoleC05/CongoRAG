package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// fakeRepo 只实现 consumeEvents 真正会调用的那几个方法（InsertStep）——
// 其余方法这个文件的测试从不触发,留空实现即可,панic 提示比默默返回
// 零值更容易在测试写错时被发现。
type fakeRepo struct {
	mu    sync.Mutex
	steps []*Step
}

func newFakeRepo() *fakeRepo { return &fakeRepo{} }

func (f *fakeRepo) CreateAgent(ctx context.Context, q platform.Querier, a *Agent) error {
	panic("not used")
}
func (f *fakeRepo) GetAgent(ctx context.Context, q platform.Querier, id uuid.UUID) (*Agent, error) {
	panic("not used")
}
func (f *fakeRepo) ListAgents(ctx context.Context, q platform.Querier) ([]*Agent, error) {
	panic("not used")
}
func (f *fakeRepo) InsertRun(ctx context.Context, q platform.Querier, r *Run) error {
	panic("not used")
}
func (f *fakeRepo) GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error) {
	panic("not used")
}
func (f *fakeRepo) ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID) ([]*Run, error) {
	panic("not used")
}
func (f *fakeRepo) UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error {
	panic("not used")
}
func (f *fakeRepo) InsertStep(ctx context.Context, q platform.Querier, s *Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, s)
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
}

func (f *fakeCheckpointStore) Save(ctx context.Context, q platform.Querier, runID uuid.UUID, step int, output string, snap json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls++
	f.lastStep, f.lastOutput = step, output
	return nil
}

func (f *fakeCheckpointStore) Load(ctx context.Context, q platform.Querier, runID uuid.UUID) (*Run, error) {
	panic("not used")
}

func newTestUsecaseForConsumeEvents() (*Usecase, *fakeRepo) {
	repo := newFakeRepo()
	u := &Usecase{repo: repo, cp: &fakeCheckpointStore{}}
	return u, repo
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

	require.Len(t, repo.steps, 1)
	step := repo.steps[0]
	assert.Equal(t, StepTypeTool, step.Type)
	assert.Equal(t, "calculator", step.ToolName)
	assert.JSONEq(t, `{"a":128,"b":3,"operator":"*"}`, string(step.ToolArgs))
	assert.JSONEq(t, `{"result":384}`, string(step.ToolResult))
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

	require.Len(t, repo.steps, 2)
	assert.Equal(t, StepTypeTool, repo.steps[0].Type)
	assert.Equal(t, StepTypeLLM, repo.steps[1].Type)
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
