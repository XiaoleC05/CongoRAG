package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ platform.PeriodicScheduler = (*fakeScheduler)(nil)

// fakeScheduler 不真的定时跑，只记住注册的函数——测试可以直接调用它来
// 模拟"这一轮周期任务被触发了"，不需要真的等 6 小时。
type fakeScheduler struct {
	registered map[string]func(ctx context.Context) error
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{registered: map[string]func(ctx context.Context) error{}}
}

func (f *fakeScheduler) RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error) {
	f.registered[name] = fn
}

// 剪枝任务必须真的被注册到调度器上——没有这一条，"这两张表有了回收路径"
// 就只是代码里写着一个没人调用的函数（issue #98）。
func TestStartRetention_RegistersPruneTask(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sched := newFakeScheduler()

	u.StartRetention(context.Background(), sched)

	_, ok := sched.registered["agent-retention-prune"]
	assert.True(t, ok, "剪枝必须有周期任务挂着，否则 run_events / tool_effect_log 又变回只增不减")
}

// 保留窗口必须 ≥ 幂等键的窗口：补发读的就是 run_events，先删事件再让键失效
// 的话，24 小时内的同键重试只会拿到一帧 run_started 就结束——客户端分不清
// "这一轮本来就没有内容"和"被删了"（issue #98）。
func TestRunEventsRetention_CoversIdempotencyReplayWindow(t *testing.T) {
	assert.GreaterOrEqual(t, runEventsRetention, platform.IdempotencyKeyTTL,
		"保留窗口短于幂等键窗口，重试就会撞上已经被删掉的事件")
}

// 剪枝删的是"窗口之外"的行：窗口内的必须一行不少。
func TestPruneExpiredRunEvents_DeletesOnlyEventsOlderThanRetention(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	ctx := context.Background()

	runID := uuid.New()
	for i := 0; i < 3; i++ {
		id, err := repo.NextRunEventID(ctx, nil, runID)
		require.NoError(t, err)
		require.NoError(t, repo.AppendRunEvent(ctx, nil, runID,
			RunEvent{ID: id, Type: "token", Payload: []byte(`{"type":"token","data":{"text":"x"}}`)}))
	}

	// 全部事件都还在窗口内：剪枝必须一条都不删。
	require.NoError(t, u.pruneExpiredRunEvents(ctx))
	repo.mu.Lock()
	require.Len(t, repo.runEvents, 3, "窗口内的事件不能被删")
	repo.mu.Unlock()

	// 把它们整体推到一个窗口之前：必须被删干净。
	repo.backdateRunEvents(runEventsRetention + time.Minute)
	require.NoError(t, u.pruneExpiredRunEvents(ctx))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.runEvents, "窗口之外的事件必须被回收")
}

// 截止时刻必须是「现在 - 保留窗口」，由 Go 侧算好再传进 SQL——写成别的值
// （比如 now()）会把整张表删空，而且没有任何东西会报错。
func TestPruneExpiredRunEvents_CutoffIsNowMinusRetention(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	start := time.Now()

	require.NoError(t, u.pruneExpiredRunEvents(context.Background()))

	repo.mu.Lock()
	cutoff := repo.pruneRunEventsBefore
	repo.mu.Unlock()
	assert.False(t, cutoff.Before(start.Add(-runEventsRetention)), "截止时刻不能早于 now - runEventsRetention")
	assert.False(t, cutoff.After(time.Now().Add(-runEventsRetention)), "截止时刻不能晚于 now - runEventsRetention")
}

// 剪枝失败必须上报（由周期任务重试），不能吞掉——静默失败会让这张表在
// 用户机器上继续无限增长，而所有判据都显示"任务在跑"。
func TestPruneExpiredRunEvents_ReportsFailure(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	repo.pruneRunEventsErr = assert.AnError

	require.Error(t, u.pruneExpiredRunEvents(context.Background()))
}

// ════════════════════════════════════════════════════════════════
// tool_effect_log：判据是"run 已终态"，不是"账本行有多旧"
// ════════════════════════════════════════════════════════════════

// seedLedger 造一条 run + 一个工具步骤 + 一行效果账本，返回步骤。
//
// 直接摆数据而不是跑一次真实运行：剪枝的判据与"工具是怎么被调用的"无关，
// 只与步骤、账本、run 状态三者的关系有关。
func seedLedger(t *testing.T, repo *fakeRepo, status RunStatus, updatedAt time.Time) *Step {
	t.Helper()
	ctx := context.Background()

	ag := &Agent{ID: uuid.New(), Name: "助手", ToolNames: []string{}}
	require.NoError(t, repo.CreateAgent(ctx, nil, ag))

	run := &Run{
		ID: uuid.New(), AgentID: ag.ID, Status: status, Input: "算一下",
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	require.NoError(t, repo.InsertRun(ctx, nil, run))

	step := &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 2, Type: StepTypeTool, Status: StepRunning,
		ToolName: "calculator", ToolArgs: json.RawMessage(`{"a":1,"b":2,"operator":"+"}`),
	}
	require.NoError(t, repo.InsertStep(ctx, nil, step))
	require.NoError(t, repo.RecordToolEffect(ctx, nil, step.ID, EffectKey(step.ToolName, step.ToolArgs)))
	return step
}

func effectRecorded(t *testing.T, repo *fakeRepo, stepID uuid.UUID) bool {
	t.Helper()
	applied, err := repo.ToolEffectApplied(context.Background(), nil, stepID)
	require.NoError(t, err)
	return applied
}

// 【本文件最要紧的一条】被中断的 run，账本无论多旧都不能删。
//
// 恢复入口只接受 interrupted 的 run（ADR-007），而它可能在任何时候被点——
// 一条三天前被中断的 run 今天照样能恢复。账本一旦被按时间删掉，
// gateToolReplay 读到的是"这一步没执行过"，于是工具真的被执行第二次，
// 全程无错误、无日志。这正是 0010 迁移里"一个会静默重复执行的 resume，
// 比没有 resume 更糟"要防的事。
func TestPruneToolEffectLog_KeepsLedgerOfInterruptedRuns(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	old := time.Now().Add(-30 * 24 * time.Hour)
	step := seedLedger(t, repo, RunInterrupted, old)

	require.NoError(t, u.pruneToolEffectLog(context.Background()))

	assert.True(t, effectRecorded(t, repo, step.ID),
		"interrupted 的 run 随时可能被恢复，删掉它的账本等于让恢复重放一个已经生效过的工具")
}

// 终态的 run 不会被恢复（runTransitions 里那三态没有任何出边），
// 所以它们的账本在留够排查时间之后必须回收——否则这张表只增不减。
func TestPruneToolEffectLog_DropsLedgerOfTerminalRuns(t *testing.T) {
	for _, status := range []RunStatus{RunCompleted, RunFailed, RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			u, repo := newTestUsecaseForConsumeEvents()
			old := time.Now().Add(-30 * 24 * time.Hour)
			step := seedLedger(t, repo, status, old)

			require.NoError(t, u.pruneToolEffectLog(context.Background()))

			assert.False(t, effectRecorded(t, repo, step.ID),
				"终态 run 的账本再也不会被读到，必须回收")
		})
	}
}

// 终态了、但还没过保留期的 run：账本要留着——刚结束的这一次运行
// 正是排查"某一步到底跑没跑"时最需要看到的一份现场。
func TestPruneToolEffectLog_KeepsLedgerOfRecentlyTerminalRuns(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	step := seedLedger(t, repo, RunCompleted, time.Now())

	require.NoError(t, u.pruneToolEffectLog(context.Background()))

	assert.True(t, effectRecorded(t, repo, step.ID),
		"终态之后还要留一个保留期，不能一结束就删")
}

// 保留期之外的截止时刻同样是「现在 - 窗口」。
func TestPruneToolEffectLog_CutoffIsNowMinusRetention(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	start := time.Now()

	require.NoError(t, u.pruneToolEffectLog(context.Background()))

	repo.mu.Lock()
	cutoff := repo.pruneToolEffectsBefore
	repo.mu.Unlock()
	assert.False(t, cutoff.Before(start.Add(-toolEffectLogRetention)))
	assert.False(t, cutoff.After(time.Now().Add(-toolEffectLogRetention)))
}

func TestPruneToolEffectLog_ReportsFailure(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	repo.pruneToolEffectsErr = assert.AnError

	require.Error(t, u.pruneToolEffectLog(context.Background()))
}

// 两张表的判据互不相关，前一张失败不该让后一张这一轮也不做——否则一次
// 短暂的库抖动会让另一张表的回收推迟一个完整的 tick。
func TestPruneRetention_BothRunEvenIfOneFails(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	repo.pruneRunEventsErr = assert.AnError

	old := time.Now().Add(-30 * 24 * time.Hour)
	step := seedLedger(t, repo, RunCompleted, old)

	err := u.pruneRetention(context.Background())
	require.Error(t, err, "任务整体要报失败，由周期任务的失败路径记下并重试")

	repo.mu.Lock()
	ranSecond := !repo.pruneToolEffectsBefore.IsZero()
	repo.mu.Unlock()
	assert.True(t, ranSecond, "第一张表失败不该把第二张表这一轮也跳过")
	assert.False(t, effectRecorded(t, repo, step.ID), "第二张表该删的还是要删")
}

// 两张表都成功时任务返回 nil——errors.Join(nil, nil) 是 nil，不是空错误。
func TestPruneRetention_SucceedsWhenBothSucceed(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	require.NoError(t, u.pruneRetention(context.Background()))
}

// ════════════════════════════════════════════════════════════════
// issue #112：error 帧不把 5xx 原文发给客户端（agent 侧）
// ════════════════════════════════════════════════════════════════

// run 还没落库就失败的那条路径（emitUnpersisted）：内部错误不能带原文。
//
// 这一帧是客户端在 toast 第二行看到的那句话，而它的来源正是 prepareRun /
// claimRun 里的库操作——原文里会有 SQLSTATE、表名、主机名。
func TestEmitUnpersisted_InternalError_DoesNotLeakDetail(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()

	u.emitUnpersisted(sink, errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"))

	require.Len(t, sink.events, 1, "恰好一条 error 帧")
	assert.Equal(t, "error", sink.events[0].Type)
	assert.Equal(t, int64(0), sink.events[0].ID,
		"没有持久化事件的帧用 0，sseSink 才会省掉 id: 那一行；改成别的号会把客户端游标推到不存在的位置")

	var envelope struct {
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(sink.events[0].Payload, &envelope))
	assert.Equal(t, "internal_error", envelope.Data.Type)
	assert.Equal(t, platform.InternalErrorDetail, envelope.Data.Detail)
	assert.NotContains(t, envelope.Data.Detail, "5432", "连接串不能出现在给客户端的帧里")
}

// 4xx 那一路必须照旧透传最内层那句：判据是"5xx 不返回原文"，不是
// "所有错误都换成统一文案"——客户端自己能纠正的输入要看得见原因。
func TestEmitUnpersisted_4xx_StillCarriesTheReason(t *testing.T) {
	u, _ := newTestUsecaseForConsumeEvents()
	sink := newFakeSink()

	// %w 直接落在 sentinel 上（prepareRun 对空输入就是这么包的）：
	// Classify 认得出 → 400 → detail 透传最内层那句。
	u.emitUnpersisted(sink, fmt.Errorf("run input must not be empty: %w", platform.ErrInvalid))

	var envelope struct {
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.Len(t, sink.events, 1)
	require.NoError(t, json.Unmarshal(sink.events[0].Payload, &envelope))
	assert.Equal(t, "invalid_argument", envelope.Data.Type)
	assert.Contains(t, envelope.Data.Detail, "must not be empty",
		"客户端自己能纠正的输入要看得见原因，不能被换成统一文案")
}

// 收尾那条 error 事件（emitRunError）：它会被持久化进 run_events，
// 之后每次重订阅与幂等重放都会把它原样发给客户端——泄漏面比在线帧更大。
func TestEmitRunError_InternalError_DoesNotLeakDetail(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	run := insertRunningRun(t, repo)

	u.emitRunError(context.Background(), run.ID,
		errors.New(`pq: relation "run_events" does not exist`))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.runEvents, 1, "error 事件要落进 run_events，重订阅才看得到收尾帧")

	var envelope struct {
		Type string `json:"type"`
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(repo.runEvents[0].Payload, &envelope))
	assert.Equal(t, "error", envelope.Type)
	assert.Equal(t, "internal_error", envelope.Data.Type)
	assert.Equal(t, platform.InternalErrorDetail, envelope.Data.Detail)
	assert.NotContains(t, envelope.Data.Detail, "run_events", "表名不能进这条会被长期保存的事件")
}

// 4xx 的原因要保留：resume 拒绝那几档（冲突、版本不兼容、副作用已生效）
// 全靠 detail 说清下一步，换成统一文案等于把可操作的提示抹掉。
func TestEmitRunError_4xx_KeepsTheReason(t *testing.T) {
	u, repo := newTestUsecaseForConsumeEvents()
	run := insertRunningRun(t, repo)

	u.emitRunError(context.Background(), run.ID,
		fmt.Errorf("step 42 already applied the effect of %q: %w", "calculator", platform.ErrToolEffectApplied))

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.runEvents, 1)

	var envelope struct {
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(repo.runEvents[0].Payload, &envelope))
	assert.Equal(t, "tool_effect_already_applied", envelope.Data.Type,
		"重订阅/重放时 type 必须和在线那条帧一致，前端才给得出同一条下一步提示")
	assert.Contains(t, envelope.Data.Detail, "already applied the effect",
		"这条拒绝的理由必须留在库里：它就是「为什么不能自动重放」那句话")
}
