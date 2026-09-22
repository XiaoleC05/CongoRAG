package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	// 分页（issue #45）：记录 ListRunsByAgent 收到的参数，并给出预置的一页。
	lastRunsCursor  *platform.ListCursor
	lastRunsLimit   int
	listRunsErr     error
	listRunsHasMore bool

	// run 维度事件流（issue #54）与工具效果账本（issue #63）。
	nextEventID int64
	runEvents   []RunEvent
	effects     map[string]struct{}
	// effectCtxErrs 记下每次 RecordToolEffect 拿到的 ctx 状态——钉住"账本
	// 写入也不能跟着请求 ctx 一起死"（issue #107），与 stepCtxErrs 同形。
	effectCtxErrs []error
	// recordEffectErr 非空时 RecordToolEffect 返回它（复刻一次写库失败）。
	recordEffectErr error

	// runEventTimes 与 runEvents 一一对应：真表的 created_at 是数据库默认值，
	// 假实现必须自己留一份，剪枝测试才能把某几条推到窗口之外（真库那边靠
	// 一条 UPDATE 做同样的事，见 postgres_integration_test.go）。
	runEventTimes []time.Time

	// 剪枝的观察点（issue #98）。记录收到的截止时刻，钉住"现在 - 窗口"
	// 这个算法；两个 err 用来复刻一次写库失败。
	pruneRunEventsBefore   time.Time
	pruneRunEventsErr      error
	pruneToolEffectsBefore time.Time
	pruneToolEffectsErr    error

	// onCAS 在 UpdateRunStatus 里被调用（持锁时），用来观察"CAS 那一刻"
	// 的外部状态。见 UpdateRunStatus 的注释。
	onCAS func()
}

// statusUpdate 记一次 UpdateRunStatus 调用，含当时 ctx 是否已被取消。
type statusUpdate struct {
	id     uuid.UUID
	from   RunStatus
	to     RunStatus
	ctxErr error
}

func newFakeRepo() *fakeRepo { return &fakeRepo{effects: map[string]struct{}{}} }

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

// UpdateAgent 就地改内存里的那份，找不到返回 not_found——真实实现用的是
// RowsAffected == 0，静默成功会让"改一个不存在的 Agent"这条路径测不到。
func (f *fakeRepo) UpdateAgent(ctx context.Context, q platform.Querier, a *Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.agents {
		if existing.ID == a.ID {
			*existing = *a
			return nil
		}
	}
	return fmt.Errorf("agent %s: %w", a.ID, platform.ErrNotFound)
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

// GetRun 按 id 在内存里找——恢复、取消、幂等重放三条路径都要读它。
func (f *fakeRepo) GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("run %s: %w", id, platform.ErrNotFound)
}

// ListRunsByAgent 记录收到的游标与 limit（供分页测试断言透传），返回预置的一页。
//
// 【为什么不沿用 panic("not used")】它现在有一条真实的调用路径了
// （issue #45 给这个端点加了分页），测试要能同时观察到"参数有没有被原样
// 传下来"和"结果有没有被正确装进信封"。
func (f *fakeRepo) ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID, cur *platform.ListCursor, limit int) ([]*Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRunsCursor = cur
	f.lastRunsLimit = limit
	if f.listRunsErr != nil {
		return nil, false, f.listRunsErr
	}
	return f.runs, f.listRunsHasMore, nil
}

// UpdateRunStatus 复刻真实实现的 CAS：`WHERE status = from` 没匹配到就返回
// ErrConflict，匹配到才改。
//
// 【为什么必须复刻 CAS 而不是"总是成功"】"不覆盖已有的终态"这条要求
// 全靠它——假实现总是成功的话，"取消来晚了一步，运行已经完成"那条分支
// 永远走不到，而那条分支正是用户会看到"点了停止却说已经完成"的地方。
func (f *fakeRepo) UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error {
	// 【onCAS 让测试能观察"CAS 发生的那一刻"的进程内状态】恢复路径有一条
	// 顺序要求：注册在途句柄必须**早于**把 run 标成 running。晚一步就会留下
	// 一个窗口，用户正好在窗口里点取消时，`Cancel` 查不到句柄、走孤儿分支
	// 返回 200，而那条恢复照跑不误。这个回调就是钉住那条顺序的手段。
	onCAS := f.onCAS

	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, statusUpdate{id: id, from: from, to: to, ctxErr: ctx.Err()})

	if onCAS != nil {
		onCAS()
	}

	for _, r := range f.runs {
		if r.ID == id {
			if r.Status != from {
				return fmt.Errorf("run %s: expected status %s but found %s: %w", id, from, r.Status, platform.ErrConflict)
			}
			r.Status = to
			r.UpdatedAt = time.Now()
			return nil
		}
	}
	// 行不存在时真实 SQL 的 RowsAffected 也是 0 → 同样报冲突。
	return fmt.Errorf("run %s: expected status %s but no row matched: %w", id, from, platform.ErrConflict)
}
func (f *fakeRepo) InsertStep(ctx context.Context, q platform.Querier, s *Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, s)
	f.stepCtxErrs = append(f.stepCtxErrs, ctx.Err())
	return nil
}
func (f *fakeRepo) StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*Step, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Step, 0, len(f.steps))
	for _, s := range f.steps {
		if s.RunID == runID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeRepo) ListToolCatalog(ctx context.Context, q platform.Querier) ([]ToolCatalogEntry, error) {
	panic("not used")
}

// UpdateStep 改的是**同一个指针**，所以断言 repo.steps[i].Status 看到的是
// 终态——和真实实现"一行先插入、随后更新"的语义一致。
//
// 【不认识就报 not_found】真实实现用的是 RowsAffected == 0，把一个不存在的
// step id 传进来是个编程错误，静默成功会让测试里那条路径永远测不到。
func (f *fakeRepo) UpdateStep(ctx context.Context, q platform.Querier, s *Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.steps {
		if existing.ID == s.ID {
			*existing = *s
			return nil
		}
	}
	return fmt.Errorf("step %s: %w", s.ID, platform.ErrNotFound)
}

// ── run 维度事件流（issue #54）────────────────────────────────

// MaxStepSeq 从内存里算——Step 的编号要靠它接着已有的往下排
// （从 1 重来会撞 UNIQUE (run_id, seq)，而那个失败是静默的）。
func (f *fakeRepo) MaxStepSeq(ctx context.Context, q platform.Querier, runID uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	maxSeq := 0
	for _, s := range f.steps {
		if s.RunID == runID && s.Seq > maxSeq {
			maxSeq = s.Seq
		}
	}
	return maxSeq, nil
}

func (f *fakeRepo) NextRunEventID(ctx context.Context, q platform.Querier, runID uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextEventID++
	return f.nextEventID, nil
}

func (f *fakeRepo) AppendRunEvent(ctx context.Context, q platform.Querier, runID uuid.UUID, ev RunEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runEvents = append(f.runEvents, ev)
	f.runEventTimes = append(f.runEventTimes, time.Now())
	return nil
}

// backdateRunEvents 把已经记下的 run 事件整体推到 d 这么早之前。
//
// 【为什么需要它】真表的 created_at 由数据库默认值写，唯一能改它的手段是
// 直接 UPDATE（生产代码里没有这个入口，见集成测试）；假实现直接改这份副本
// 即可——剪枝的判据就是"比截止时刻早"，把时间挪过去和等到那时候等价。
func (f *fakeRepo) backdateRunEvents(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	past := time.Now().Add(-d)
	for i := range f.runEventTimes {
		f.runEventTimes[i] = past
	}
}

// ── 保留窗口剪枝（issue #98）──────────────────────────────────

// PruneRunEvents 复刻真 SQL 的判据：只按 created_at 一条线删，不看 run 状态。
func (f *fakeRepo) PruneRunEvents(ctx context.Context, q platform.Querier, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneRunEventsBefore = before
	if f.pruneRunEventsErr != nil {
		return 0, f.pruneRunEventsErr
	}

	var keptEvents []RunEvent
	var keptTimes []time.Time
	for i, ev := range f.runEvents {
		if f.runEventTimes[i].Before(before) {
			continue
		}
		keptEvents = append(keptEvents, ev)
		keptTimes = append(keptTimes, f.runEventTimes[i])
	}
	n := int64(len(f.runEvents) - len(keptEvents))
	f.runEvents, f.runEventTimes = keptEvents, keptTimes
	return n, nil
}

// PruneToolEffectLog 复刻真 SQL 的三跳判定：账本 → 步骤 → run，只有
// **已终态且终态够久**的 run 的账本才被回收。
//
// 【为什么"终态"这一半不能省】假实现若只按时间删，retention_test.go 里
// "interrupted 的账本一行都不能动"那条测试就永远是绿的——而它正是这张表
// 存在的全部意义（删错了 = 恢复时重放一个已经生效过的副作用）。
func (f *fakeRepo) PruneToolEffectLog(ctx context.Context, q platform.Querier, olderThan time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruneToolEffectsBefore = olderThan
	if f.pruneToolEffectsErr != nil {
		return 0, f.pruneToolEffectsErr
	}

	// run 已终态、且终态之后又过了 olderThan 这么久。
	recyclable := map[uuid.UUID]bool{}
	for _, r := range f.runs {
		recyclable[r.ID] = r.Status.IsTerminal() && r.UpdatedAt.Before(olderThan)
	}
	// 账本键是 "stepID\x00effectKey"（见 RecordToolEffect），取前半段找步骤。
	stepRun := map[uuid.UUID]uuid.UUID{}
	for _, s := range f.steps {
		stepRun[s.ID] = s.RunID
	}

	var n int64
	for key := range f.effects {
		stepIDStr, _, _ := strings.Cut(key, "\x00")
		stepID, err := uuid.Parse(stepIDStr)
		if err != nil {
			continue
		}
		if recyclable[stepRun[stepID]] {
			delete(f.effects, key)
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) RunEventsAfter(ctx context.Context, q platform.Querier, runID uuid.UUID, afterEventID int64) ([]RunEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RunEvent, 0, len(f.runEvents))
	for _, ev := range f.runEvents {
		if ev.ID > afterEventID {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ── 工具效果账本（issue #63）──────────────────────────────────

// RecordToolEffect 复刻真实实现的关键性质：同一个 (step_id, effect_key)
// 只能记一次，第二次返回 platform.ErrToolEffectApplied。
//
// 【这条假实现必须复刻它】issue #63 的验收标准就是"不写 resume 时能稳定
// 观察到这个冲突"——假实现如果总是成功，那条测试就永远是绿的，
// 而它测的东西（唯一约束真的在挡重复执行）恰好是整条恢复机制的判据。
func (f *fakeRepo) RecordToolEffect(ctx context.Context, q platform.Querier, stepID uuid.UUID, effectKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.effectCtxErrs = append(f.effectCtxErrs, ctx.Err())
	if f.recordEffectErr != nil {
		return f.recordEffectErr
	}
	key := stepID.String() + "\x00" + effectKey
	if _, ok := f.effects[key]; ok {
		return fmt.Errorf("record tool effect for step %s: %w", stepID, platform.ErrToolEffectApplied)
	}
	f.effects[key] = struct{}{}
	return nil
}

func (f *fakeRepo) ToolEffectApplied(ctx context.Context, q platform.Querier, stepID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 只按 step 找——判据是"这一步记过账没有"，见 port.go 的注释。
	prefix := stepID.String() + "\x00"
	for k := range f.effects {
		if strings.HasPrefix(k, prefix) {
			return true, nil
		}
	}
	return false, nil
}

// ── 崩溃扫描与 checkpoint 回收 ────────────────────────────────

func (f *fakeRepo) InterruptRunningRuns(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []uuid.UUID
	for _, r := range f.runs {
		if r.Status == RunRunning {
			r.Status = RunInterrupted
			ids = append(ids, r.ID)
		}
	}
	for _, s := range f.steps {
		if s.Status == StepRunning {
			s.Status = StepInterrupted
		}
	}
	return ids, nil
}

func (f *fakeRepo) ClearTerminalRunCheckpoints(ctx context.Context, q platform.Querier, olderThan time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, r := range f.runs {
		if r.StateSnapshot != nil && r.Status.IsTerminal() && r.UpdatedAt.Before(olderThan) {
			r.StateSnapshot = nil
			n++
		}
	}
	return n, nil
}

// fakeTx 是最小的 platform.TxManager：直接把同一个 Querier 传下去。
//
// 【它不模拟回滚】测试里没有一条断言依赖"事务失败时回滚"——那件事由真实
// 的 Postgres 保证（CI 的 integration job 跑得到）。这里只要让
// "发号 + 写事件"两步能跑起来即可。
type fakeTx struct{}

func (fakeTx) InTx(ctx context.Context, fn func(q platform.Querier) error) error {
	return fn(nil)
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
	// 【必须走 NewUsecase 而不是裸结构体】Usecase 现在持有在途运行表，
	// 裸结构体的那个 map 是 nil，registerRunning 会直接 panic 在
	// "assignment to entry in nil map"上——而那条错误信息完全看不出
	// 问题出在构造方式上。
	return NewUsecase(repo, cp, nil, nil, newFakeIdempotencyStore(), fakeTx{}, nil, nil), repo, cp
}

// runConsumeEvents 是测试里调用 consumeEvents 的统一入口。
//
// 【为什么要包一层】事件号现在问数据库要（issue #54），在途句柄也要传进去
// （取消靠它）。每次调用都写这些参数会让 13 个用例各多两行噪音，
// 而它们要断言的东西和这两样都无关。
func runConsumeEvents(u *Usecase, ctx context.Context, runID uuid.UUID, events <-chan adkEvent, sink conversation.EventSink) (string, error) {
	return u.consumeEvents(ctx, runID, "model-1", events, sink, &runningRun{done: make(chan struct{})})
}

// fakeIdempotencyStore 是一个内存版的幂等键表，复刻那条关键性质：
// 同一个 (endpoint, key) 第二次预留返回 ErrIdempotentHit。
type fakeIdempotencyStore struct {
	mu      sync.Mutex
	records map[string]*IdempotencyRecord
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{records: map[string]*IdempotencyRecord{}}
}

func (s *fakeIdempotencyStore) ReserveIdempotencyKey(ctx context.Context, q platform.Querier, rec *IdempotencyRecord, expiredBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rec.Endpoint + "\x00" + rec.Key
	if _, ok := s.records[key]; ok {
		return fmt.Errorf("reserve: %w", platform.ErrIdempotentHit)
	}
	stored := *rec
	s.records[key] = &stored
	return nil
}

func (s *fakeIdempotencyStore) DeleteIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, endpoint+"\x00"+key)
	return nil
}

func (s *fakeIdempotencyStore) LookupIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) (*IdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[endpoint+"\x00"+key]
	if !ok {
		return nil, fmt.Errorf("idempotency key %s: %w", key, platform.ErrNotFound)
	}
	copied := *rec
	return &copied, nil
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

	output, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)

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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

			_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)

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

	output, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
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

	output, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)

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

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)

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

	_, err := runConsumeEvents(u, reqCtx, uuid.New(), events, sink)
	require.Error(t, err)

	require.Len(t, repo.steps, 1, "未完成的那一轮要标失败落库")
	require.Len(t, repo.stepCtxErrs, 1)
	assert.NoError(t, repo.stepCtxErrs[0], "落 Step 用的 ctx 必须脱离请求生命周期")
	assert.Equal(t, StepFailed, repo.steps[0].Status)
	assert.NoError(t, cp.lastCtxErr, "checkpoint 同样要脱离请求生命周期")
}

// ════════════════════════════════════════════════════════════════
// issue #106：退出路径不能留下"永远 running"的工具步骤
// ════════════════════════════════════════════════════════════════

// toolStepOf 从落库的步骤里挑出工具那一行（事件流不同，它的下标会变）。
func toolStepOf(t *testing.T, repo *fakeRepo) *Step {
	t.Helper()
	for _, s := range repo.steps {
		if s.Type == StepTypeTool {
			return s
		}
	}
	t.Fatal("没有落库的工具步骤——这一步本该在工具执行之前就写进去")
	return nil
}

// 【issue #106 的验收判据】取消/断线时正在执行的那一步工具此前会永远停在
// running：closeOpenStepsOnExit 只关 llmStep，而 pending 里那些已经以
// running 落库的工具行在函数返回时没有任何处理。
//
// 两个后果里第二个更重：轨迹页上那个"运行中"是假的，而 ADR-007 崩溃表的
// 第一判据就是"一行 running 的 step"——残留让"有 running 的 step"不再等于
// "进程崩过"，而整条恢复链都建立在这个判据上。
func TestConsumeEvents_ToolStepStillRunningOnExit_IsInterrupted(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 4)
	// 模型请求调用工具、这一行已经落库（status=running），但结果还没回来
	// 流就断了（用户点取消 / 客户端断开都会走到这条路径）。
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`)}
	close(events)

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
	require.Error(t, err, "没有 done 事件就是非正常结束")

	for _, s := range repo.steps {
		assert.NotEqual(t, StepRunning, s.Status,
			"任何一条退出路径都不能留下永远 running 的 step（ADR-007 的崩溃判据）")
	}
	toolStep := toolStepOf(t, repo)
	assert.Equal(t, StepInterrupted, toolStep.Status)
	assert.NotEmpty(t, toolStep.Error, "要写清为什么它没跑完，轨迹页才解释得清")
}

// 错误事件同样是一条退出路径：工具节点自己失败时（工具返回基础设施错误，
// ToolsNode 把它当致命错误，不写 tool message）tool_result 永远不会到达，
// 那一行留在 running 就是同一个现场。
func TestConsumeEvents_ErrorWithToolInFlight_IsInterrupted(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 4)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventError, err: errors.New("tool node exploded")}
	close(events)

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
	require.Error(t, err)

	assert.Equal(t, StepInterrupted, toolStepOf(t, repo).Status,
		"这一行等不到结果了，退出时必须收成 interrupted")
}

// ════════════════════════════════════════════════════════════════
// issue #107：工具效果账本不能被请求 ctx 拖死，失败也不能咽
// ════════════════════════════════════════════════════════════════

// 【issue #107 的验收判据】请求 ctx 已取消时账本仍然要写得进去。
//
// 断线是这个端点最常见的触发场景，那时 pgx 拿着已取消的 ctx 连 Acquire 都
// 过不去——账本在最需要它的那一刻静默缺一行，而代码紧接着就把这一步标成
// completed。这与 insertStep / updateStep / checkpoint / emitRunEvent 的
// 处理是同一条规则，此前只有账本这一处例外。
func TestConsumeEvents_ToolEffectRecordedWithDetachedContext(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()
	events := make(chan adkEvent, 4)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`), toolResult: json.RawMessage(`{"result":3}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel() // net/http 在客户端断开时取消请求 ctx

	_, err := runConsumeEvents(u, reqCtx, uuid.New(), events, sink)
	require.NoError(t, err)

	require.Len(t, repo.effectCtxErrs, 1)
	assert.NoError(t, repo.effectCtxErrs[0], "账本写入必须脱离请求生命周期")
	assert.Len(t, repo.effects, 1, "账本要真的记下去")
}

// 记不上账 = "这一步的副作用发生过没有"无法判定。以前这里只记一行日志就
// 往下走、紧接着把步骤标成 completed——若进程随后被杀掉，现场是
// "step running + 无账本"，恢复路径据此判成"工具没跑过" → 重放 → 工具真的
// 被第二次执行，全程无错误无日志（项目文档 §9.5 说的正是这件事）。
func TestConsumeEvents_ToolEffectWriteFailure_FailsTheRun(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	repo.recordEffectErr = errors.New("connection reset by peer")
	sink := newFakeSink()
	events := make(chan adkEvent, 4)
	events <- adkEvent{kind: adkEventToolCall, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`)}
	events <- adkEvent{kind: adkEventToolResult, toolCallID: "c1", toolName: "calculator",
		toolArgs: json.RawMessage(`{}`), toolResult: json.RawMessage(`{"result":3}`)}
	events <- adkEvent{kind: adkEventDone}
	close(events)

	_, err := runConsumeEvents(u, context.Background(), uuid.New(), events, sink)
	require.Error(t, err, "账本未知的状态下不能继续往下走")
	assert.Contains(t, err.Error(), "connection reset by peer")

	toolStep := toolStepOf(t, repo)
	assert.NotEqual(t, StepRunning, toolStep.Status,
		"不能把它留在 running——那既污染崩溃判据，又会让它落进恢复的重放列表")
	assert.NotEqual(t, StepCompleted, toolStep.Status,
		"也不能说它成功了：账本缺一行，这次执行就没有证据")
}

// ════════════════════════════════════════════════════════════════
// issue #109：Agent 路径的输入上限
// ════════════════════════════════════════════════════════════════

// 【issue #109 的验收判据之一】prepareRun 必须拒绝超长输入，而且拒绝要发生在
// 分配 run id 之前、错误类型是 invalid_argument。
//
// 【为什么错误类型是这条测试的重点】超长输入放过去的话，报错会发生在模型那
// 一侧、被归成 upstream_llm_error，前端按 type 提示用户"检查 API Key 和
// 配额"——排查方向从第一句话起就是错的（issue #34 修掉的就是这类误报）。
func TestStart_InputTooLong_IsRejectedBeforeAnyRunRow(t *testing.T) {
	u, repo, agentID, sink := newStartFixture(t)

	_, err := u.Start(context.Background(), agentID, strings.Repeat("字", maxInputLen+1), "", sink)
	require.ErrorIs(t, err, platform.ErrInvalid)
	assert.Equal(t, "invalid_argument", platform.SSEErrorType(err))
	assert.Empty(t, repo.runs, "被拒的那一轮不该在 agent_runs 里留下一条 running 行")

	require.Len(t, sink.events, 1)
	assert.Equal(t, "error", sink.events[0].Type)
}

// 边界值本身要放行——上限是"至多 maxInputLen 个字符"，不是"少于"。
// （这里只断言它没有在上限这一道被拒：模型是假的，运行会在后面失败。）
func TestStart_InputAtLimit_PassesTheLengthGate(t *testing.T) {
	u, repo, agentID, sink := newStartFixture(t)

	_, err := u.Start(context.Background(), agentID, strings.Repeat("字", maxInputLen), "", sink)
	assert.NotErrorIs(t, err, platform.ErrInvalid,
		"正好等于上限的输入不该被这一道挡下：%v", err)
	assert.NotEmpty(t, repo.runs, "长度过关之后要真的建出 run 行")
}

// 失败状态必须真的写下去：用请求 ctx 做 CAS 的话，客户端一断开 UPDATE
// 就不会执行，run 行永久停在 'running'（issue #14）。同时 ctx 已取消说明
// 是客户端走了，记 'interrupted' 而不是服务端故障 'failed'。
func TestFinishRun_CancelledRequestContext_StillWritesInterrupted(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	run := insertRunningRun(t, repo)
	_, err := u.finishRun(reqCtx, run, &runningRun{}, errors.New("运行期失败"))
	require.Error(t, err, "原始失败要带出去，不能因为写终态成功就吞掉")

	require.Len(t, repo.updates, 1)
	assert.Equal(t, run.ID, repo.updates[0].id)
	assert.Equal(t, RunRunning, repo.updates[0].from)
	assert.Equal(t, RunInterrupted, repo.updates[0].to)
	assert.NoError(t, repo.updates[0].ctxErr, "写库用的 ctx 必须脱离请求生命周期")
}

// ctx 还活着（真正的运行期失败）时仍然记 failed，不要一律记成 interrupted。
func TestFinishRun_LiveRequestContext_MarksFailed(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()

	_, err := u.finishRun(context.Background(), insertRunningRun(t, repo),
		&runningRun{}, errors.New("模型返回了 500"))
	require.Error(t, err)

	require.Len(t, repo.updates, 1)
	assert.Equal(t, RunFailed, repo.updates[0].to)
	assert.NoError(t, repo.updates[0].ctxErr)
}

// 【issue #55 的核心断言】用户点「停止」必须是 cancelled，不是 failed。
//
// 两者的触发条件都是"请求 ctx 被取消"（取消端点 cancel 的就是那条 ctx），
// 所以唯一的区分依据是 runningRun.requested——用户点的是他想要的结束，
// 客户端断开是他没说不要、只是连接没了。记反了用户会看到"运行失败"
// 而红色错误提示，而他刚刚做的是一个成功的操作。
func TestFinishRun_UserRequestedCancel_MarksCancelled(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	st := &runningRun{done: make(chan struct{})}
	st.requested.Store(true)

	_, err := u.finishRun(ctx, insertRunningRun(t, repo), st, context.Canceled)
	require.Error(t, err, "取消也是一次非正常结束，原始错误照样带出去记日志")

	require.Len(t, repo.updates, 1)
	assert.Equal(t, RunCancelled, repo.updates[0].to,
		"用户点的取消是 cancelled；记成 interrupted 前端会显示成「被打断」而不是「已停止」")
}

// 没有任何 cause 时是正常完成——取消标志没置过、ctx 也没被取消。
func TestFinishRun_NoCause_MarksCompleted(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()

	run, err := u.finishRun(context.Background(), insertRunningRun(t, repo), &runningRun{}, nil)
	require.NoError(t, err)

	require.Len(t, repo.updates, 1)
	assert.Equal(t, RunCompleted, repo.updates[0].to)
	assert.Equal(t, RunCompleted, run.Status)
}

// 【"不覆盖已有的终态"】收尾试图把一条已经终态的 run 改成 failed 时必须
// 放弃，并把数据库里的真实状态读回来返回——而不是把一次已经成功的运行
// 报成失败。真实的强制点是 UpdateRunStatus 的 CAS（WHERE status = from），
// 假实现复刻了它，所以这条测的确实是那条分支。
func TestFinishRun_AlreadyTerminal_DoesNotOverwrite(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	run := insertRunningRun(t, repo)
	// 另一条路径（用户取消）先把它收了尾。
	require.NoError(t, repo.UpdateRunStatus(context.Background(), nil, run.ID, RunRunning, RunCancelled))

	got, err := u.finishRun(context.Background(), run, &runningRun{}, errors.New("太晚了"))
	require.Error(t, err, "原始失败仍然要带出去记日志")

	assert.Equal(t, RunCancelled, got.Status,
		"必须返回数据库里的真实终态，而不是内存里那个过期的 running")
}

// insertRunningRun 往假 repo 里放一条 running 的 run。
//
// 【为什么必须真的插进去】UpdateRunStatus 复刻了 CAS——行不存在也会报冲突
// （真实 SQL 的 RowsAffected == 0 就是这条路径）。凭空造一个 &Run{} 再传
// 进去的话，测的就不是收尾逻辑，而是"假实现会不会拒绝"。
func insertRunningRun(t *testing.T, repo *fakeRepo) *Run {
	t.Helper()
	run := &Run{
		ID: uuid.New(), AgentID: uuid.New(), Status: RunRunning, Input: "算一下",
		StateSchemaVersion: CurrentStateSchemaVersion, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.InsertRun(context.Background(), nil, run))
	return run
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

	_, err := u.Start(context.Background(), uuid.New(), "   ", "", sink)
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
	u := newTestUsecase(repo, &fakeCheckpointStore{}, NewToolRegistry(), nil)

	a, err := u.CreateAgent(context.Background(), "计算助手", "", "", nil)
	require.NoError(t, err)

	assert.NotNil(t, a.ToolNames, "nil slice 绑定成 SQL NULL，撞上 tool_names NOT NULL")
	assert.Empty(t, a.ToolNames)
	require.Len(t, repo.agents, 1)
	assert.NotNil(t, repo.agents[0].ToolNames, "落库的那份也要是非 nil 空数组")
}
