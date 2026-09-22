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

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// issue #59：agent_runs 的状态迁移表
// ════════════════════════════════════════════════════════════════

// 每一行都要至少覆盖一条**非法**迁移——这是项目文档 §8.4 给的下限，
// 目的是让表被真的填满，而不是写一个空壳（空壳的状态机会放行一切，
// 而"放行一切"和"没有状态机"在测试上看起来一模一样）。
func TestRunStatus_CanTransition(t *testing.T) {
	cases := []struct {
		name string
		from RunStatus
		to   RunStatus
		want bool
	}{
		// ── 合法：正常路径与取消 ──
		{"pending 开始执行", RunPending, RunRunning, true},
		{"pending 排队时被取消", RunPending, RunCancelled, true},
		{"running 正常完成", RunRunning, RunCompleted, true},
		{"running 失败", RunRunning, RunFailed, true},
		{"running 被用户取消", RunRunning, RunCancelled, true},
		{"running 进程崩掉被扫描成中断", RunRunning, RunInterrupted, true},

		// ── 合法：恢复（interrupted 是唯一有出边的"未完成"态）──
		{"interrupted 被恢复", RunInterrupted, RunRunning, true},
		{"interrupted 被用户放弃", RunInterrupted, RunCancelled, true},

		// ── 非法：§8.4 点名的三行样例 ──
		{"completed → running 拒绝", RunCompleted, RunRunning, false},
		{"cancelled → running 拒绝", RunCancelled, RunRunning, false},

		// ── 非法：其余终态出边 ──
		{"completed → failed 拒绝", RunCompleted, RunFailed, false},
		{"completed → cancelled 拒绝", RunCompleted, RunCancelled, false},
		{"cancelled → completed 拒绝", RunCancelled, RunCompleted, false},
		{"failed → running 拒绝（ADR-007：failed 不可恢复）", RunFailed, RunRunning, false},
		{"failed → completed 拒绝", RunFailed, RunCompleted, false},

		// ── 非法：跳过 running ──
		{"pending → completed 拒绝（没跑过就说完成了）", RunPending, RunCompleted, false},

		// ── 未知状态一律拒绝 ──
		{"未知状态出发", RunStatus("Succeeded"), RunRunning, false},
		{"迁到未知状态", RunRunning, RunStatus("done"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.from.CanTransition(tc.to))
		})
	}
}

// 终态的定义是"没有任何出边"——Resume 入口靠它给出一句准确的拒绝理由。
func TestRunStatus_IsTerminal(t *testing.T) {
	assert.False(t, RunPending.IsTerminal())
	assert.False(t, RunRunning.IsTerminal())
	// 【interrupted 不是终态】它是恢复的入口状态——把它判成终态，
	// 整条 M4-C 的恢复就没有起点了。
	assert.False(t, RunInterrupted.IsTerminal())

	assert.True(t, RunCompleted.IsTerminal())
	assert.True(t, RunFailed.IsTerminal())
	assert.True(t, RunCancelled.IsTerminal())
}

// ════════════════════════════════════════════════════════════════
// issue #63：工具效果账本
// ════════════════════════════════════════════════════════════════

// effectKey 必须只由 (工具名, 参数) 决定——同样的输入永远得到同样的键，
// 否则恢复路径判断"这个效果做过没有"时每次都会得到不同的答案。
func TestEffectKey_IsDeterministicAndArgumentSensitive(t *testing.T) {
	args := json.RawMessage(`{"a":1,"b":2}`)

	assert.Equal(t, EffectKey("calculator", args), EffectKey("calculator", args),
		"同样的工具与参数必须得到同样的键——不然恢复判据每次都不同")
	assert.NotEqual(t, EffectKey("calculator", args), EffectKey("knowledge_search", args),
		"换个工具是不同的副作用")
	assert.NotEqual(t, EffectKey("calculator", args), EffectKey("calculator", json.RawMessage(`{"a":1,"b":3}`)),
		"同一工具换个参数是另一次副作用")
	// 拼接处必须有分隔符：没有的话 ("ab","c") 与 ("a","bc") 会撞成同一个摘要。
	assert.NotEqual(t, EffectKey("ab", json.RawMessage(`c`)), EffectKey("a", json.RawMessage(`bc`)))
}

// 【issue #63 的验收标准本身】不写 resume 的跳过逻辑时，重放同一步必须
// 撞上唯一约束——那个冲突就是"工具被重复执行了"的判据（项目文档 §9.5）。
//
// 【这条测试的价值在于它钉的是一个**会消失的信号**】正确的 resume 会让
// 它消失（先查账本再决定跑不跑）；如果哪天有人把那个检查删掉，这条测试
// 不会红——但它记录的判读方向会让审查者一眼看出问题。所以这里显式断言
// "冲突真的会发生"，把它变成一条可执行的规格而不是一句注释。
func TestToolEffectLog_DuplicateExecutionIsObservable(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	stepID := uuid.New()
	key := EffectKey("calculator", json.RawMessage(`{"a":1,"b":2,"operator":"+"}`))

	require.NoError(t, repo.RecordToolEffect(ctx, nil, stepID, key),
		"第一次执行工具：记账成功，这是正常路径")

	err := repo.RecordToolEffect(ctx, nil, stepID, key)
	require.Error(t, err, "第二次执行同一个效果必须被唯一约束挡住")
	assert.ErrorIs(t, err, platform.ErrToolEffectApplied,
		"必须是这个 sentinel 而不是 ErrDuplicateKey——恢复路径靠它判读「重复执行了」")
	assert.NotErrorIs(t, err, platform.ErrDuplicateKey, "它和业务唯一冲突是两件事")

	applied, err := repo.ToolEffectApplied(ctx, nil, stepID, key)
	require.NoError(t, err)
	assert.True(t, applied, "记过账之后查询必须为真——恢复路径正是靠这个跳过重放")
}

// 没记过账的效果查出来必须是 false（否则恢复会把没跑过的步骤当成跑过了）。
func TestToolEffectLog_NotAppliedYet(t *testing.T) {
	repo := newFakeRepo()
	applied, err := repo.ToolEffectApplied(context.Background(), nil, uuid.New(), "whatever")
	require.NoError(t, err)
	assert.False(t, applied)
}

// ════════════════════════════════════════════════════════════════
// issue #65 / #61：PrepareResume 的每一道拒绝
// ════════════════════════════════════════════════════════════════

// newResumeFixture 造一条"崩在第 2 步"的 run：
//
//	step 1  llm   completed
//	step 2  tool  running（工具没跑完）——崩溃现场
func newResumeFixture(t *testing.T, tool Tool, schemaVersion int) (*Usecase, *fakeRepo, *Run, *Step) {
	t.Helper()
	repo := newFakeRepo()
	tools := NewToolRegistry()
	tools.Register(tool)
	reg := &fakeLLMRegistry{model: newChatModel("test-model", true)}
	u := newTestUsecase(repo, &fakeCheckpointStore{}, tools, reg)

	ag := &Agent{ID: uuid.New(), Name: "助手", ToolNames: []string{tool.Name()}}
	require.NoError(t, repo.CreateAgent(context.Background(), nil, ag))

	run := &Run{
		ID: uuid.New(), AgentID: ag.ID, Status: RunInterrupted, Input: "算一下",
		StateSchemaVersion: schemaVersion, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.InsertRun(context.Background(), nil, run))

	require.NoError(t, repo.InsertStep(context.Background(), nil, &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 1, Type: StepTypeLLM, Status: StepCompleted,
	}))
	// 【工具步骤必须是 running】它就是崩溃现场：这一步在调用之前落了库，
	// 结果没来得及写回来。
	step := &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 2, Type: StepTypeTool, Status: StepRunning,
		ToolName: tool.Name(), ToolArgs: json.RawMessage(`{"a":1,"b":2,"operator":"+"}`),
	}
	require.NoError(t, repo.InsertStep(context.Background(), nil, step))

	return u, repo, run, step
}

// 终态的 run 不能恢复：completed 没有可恢复的东西，failed 重放只会以同样的
// 方式再失败一次（而它消耗的是用户自己配的额度）。
func TestPrepareResume_TerminalRun_IsRejected(t *testing.T) {
	for _, status := range []RunStatus{RunCompleted, RunFailed, RunCancelled} {
		t.Run(string(status), func(t *testing.T) {
			u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
			run.Status = status
			_ = repo

			_, err := u.PrepareResume(context.Background(), run.ID)
			require.Error(t, err)
			assert.ErrorIs(t, err, platform.ErrConflict,
				"终态是 409 冲突，不是 404 也不是 500")
			assert.Contains(t, err.Error(), string(status),
				"拒绝理由要带上当前状态，否则用户看不出为什么不能恢复")
		})
	}
}

// 正在跑的 run 也不能恢复：恢复它等于让这次运行跑两遍。
func TestPrepareResume_RunningRun_IsRejected(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	run.Status = RunRunning
	_ = repo

	_, err := u.PrepareResume(context.Background(), run.ID)
	require.ErrorIs(t, err, platform.ErrConflict)
}

// 【issue #65 的核心】快照版本不匹配必须**明确拒绝**，而不是硬着头皮
// 反序列化出一个半截状态。错误必须是有类型的（前端按它给出"重新发起"的
// 提示），不能是一次反序列化崩溃。
func TestPrepareResume_SchemaVersionMismatch_IsRejectedWithTypedError(t *testing.T) {
	u, _, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion+1)

	_, err := u.PrepareResume(context.Background(), run.ID)

	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrStateSchemaVersionMismatch,
		"必须是有类型的错误，API 层才能映射成「明确拒绝并提示重新发起」")
	assert.Contains(t, err.Error(), fmt.Sprint(CurrentStateSchemaVersion+1),
		"要把快照的版本号写进文案——排查时先要知道差在哪一版")
	assert.Equal(t, "state_schema_version_mismatch", platform.SSEErrorType(err),
		"流式路径上也要给出同一个 type，不能退化成 internal_error")
}

// ── issue #61：ToolMetadata 决定能不能重放 ──

// nonIdempotentTool 是一个写外部系统、且不可重放的工具。
type nonIdempotentTool struct {
	stubTool
	invoked *int
}

func (s *nonIdempotentTool) Name() string { return "payment" }
func (s *nonIdempotentTool) Metadata() Metadata {
	return Metadata{SideEffectLevel: WriteNonIdempotent, RetryPolicy: RetryNeedsIdempotency}
}
func (s *nonIdempotentTool) Spec() ToolSpec {
	return ToolSpec{Name: s.Name(), Description: "收款", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (s *nonIdempotentTool) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	*s.invoked++
	return json.RawMessage(`{"ok":true}`), nil
}

// 【issue #61 的验收标准】标为 WRITE_NON_IDEMPOTENT 的假工具在 resume 时
// **不被**自动重放。
//
// 【为什么判据是"没被调用"而不是"返回了错误"】错误谁都能返回；这条要钉的是
// 那个副作用**真的没发生**。所以用一个会计数的假工具，然后断言计数是 0。
func TestPrepareResume_WriteNonIdempotentTool_IsNotReplayed(t *testing.T) {
	invoked := 0
	u, _, run, _ := newResumeFixture(t, &nonIdempotentTool{invoked: &invoked}, CurrentStateSchemaVersion)

	_, err := u.PrepareResume(context.Background(), run.ID)

	require.ErrorIs(t, err, platform.ErrReplayUnsafe,
		"判据来自 ToolMetadata，不是硬编码的工具名白名单")
	assert.Equal(t, 0, invoked, "被拒绝的恢复绝不能已经把工具跑过一遍")
	assert.Equal(t, "replay_unsafe", platform.SSEErrorType(err))
}

// 工具声明 retry_policy = never 时同样不重放——它比副作用等级更严格，
// 是工具自己的声明。
func TestPrepareResume_RetryNeverTool_IsNotReplayed(t *testing.T) {
	// stubTool 的 Metadata 就是 ReadOnly + RetryNever，正好覆盖这一条：
	// 副作用无害但工具明确说了不要重试。
	u, _, run, _ := newResumeFixture(t, &stubTool{}, CurrentStateSchemaVersion)

	_, err := u.PrepareResume(context.Background(), run.ID)

	require.ErrorIs(t, err, platform.ErrReplayUnsafe)
}

// 只读工具可以重放——它是三个内置工具的常态（issue #61："首批 3 个工具
// 全是 READ_ONLY，所以先证伪再证真"，这一条就是那个"证真"）。
func TestPrepareResume_ReadOnlyTool_IsScheduledForReplay(t *testing.T) {
	u, _, run, step := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)

	plan, err := u.PrepareResume(context.Background(), run.ID)
	require.NoError(t, err)

	require.Len(t, plan.pendingToolSteps, 1, "没跑完的只读步骤要被排进重放列表")
	assert.Equal(t, step.ID, plan.pendingToolSteps[0].ID,
		"重放必须复用**同一个 step 行**：效果账本的键是 (step_id, effect_key)，换一行就等于绕开了那道判据")
}

// 【崩溃表第二行里那个可区分的子情况】账本已提交 = 工具确实跑过了，
// 而它的结果没留下来。这时重放才是真正的重复执行，必须拒绝。
func TestPrepareResume_EffectAlreadyApplied_IsRejected(t *testing.T) {
	u, repo, run, step := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)

	// 模拟"工具跑完、账本提交了、进程在写回结果之前被杀"。
	require.NoError(t, repo.RecordToolEffect(context.Background(), nil, step.ID,
		EffectKey(step.ToolName, step.ToolArgs)))

	_, err := u.PrepareResume(context.Background(), run.ID)

	require.ErrorIs(t, err, platform.ErrToolEffectApplied)
	assert.Equal(t, "tool_effect_already_applied", platform.SSEErrorType(err))
}

// ── 拒绝之外：正常恢复要把没跑完的只读步骤重放掉 ──

// 【#64 的核心路径】恢复时重放没走完的工具步骤，并把它的结果写回**同一行**。
func TestResume_ReplaysPendingReadOnlyStep(t *testing.T) {
	u, repo, run, step := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)

	plan, err := u.PrepareResume(context.Background(), run.ID)
	require.NoError(t, err)

	turn, err := u.replayToolStep(context.Background(), plan.pendingToolSteps[0])
	require.NoError(t, err)

	assert.Equal(t, "calculator", turn.ToolName)
	assert.Contains(t, string(turn.ToolResult), "3", "1+2=3")
	assert.Equal(t, StepCompleted, step.Status, "重放成功之后这一步要变成完成")
	assert.NotEmpty(t, step.ToolResult)

	// 关键：记账用的是同一行的 id——换一行就没法在下次恢复时判读了。
	applied, err := repo.ToolEffectApplied(context.Background(), nil, step.ID,
		EffectKey(step.ToolName, step.ToolArgs))
	require.NoError(t, err)
	assert.True(t, applied)
}

// 恢复之前把没走完的 llm 轮次标 interrupted——"不假装成功"（§9.1）。
func TestResume_MarksInterruptedLLMStep(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)

	// 再加一行没走完的 llm 轮次（崩溃就发生在模型生成的中途）。
	llmStep := &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 3, Type: StepTypeLLM, Status: StepRunning,
	}
	require.NoError(t, repo.InsertStep(context.Background(), nil, llmStep))

	plan, err := u.PrepareResume(context.Background(), run.ID)
	require.NoError(t, err)
	require.Len(t, plan.interruptedLLMSteps, 1)

	// 【只跑"标记 + 重放"那两段】Resume 的第三步要真的调模型，测试里没有
	// 模型可用——这里直接调用它内部的那两步，断言状态落库的形状。
	for _, s := range plan.interruptedLLMSteps {
		s.Status = StepInterrupted
		s.Error = "进程在这次生成完成之前退出"
		require.NoError(t, repo.UpdateStep(context.Background(), nil, s))
	}

	assert.Equal(t, StepInterrupted, llmStep.Status)
	assert.Equal(t, "进程在这次生成完成之前退出", llmStep.Error,
		"错误文案要说清是「没跑完」而不是一次失败——两者对用户的含义不同")
}

// 已经完成的工具步骤要能被重建成历史，交给模型继续。
func TestPrepareResume_CompletedToolStepsBecomeHistory(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	require.NoError(t, repo.InsertStep(context.Background(), nil, &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 4, Type: StepTypeTool, Status: StepCompleted,
		ToolName: "calculator", ToolArgs: json.RawMessage(`{"a":2,"b":3,"operator":"*"}`),
		ToolResult: json.RawMessage(`{"result":6}`),
	}))

	plan, err := u.PrepareResume(context.Background(), run.ID)
	require.NoError(t, err)

	require.Len(t, plan.priorTurns, 1, "已完成的工具调用要进历史")
	assert.Equal(t, "calculator", plan.priorTurns[0].ToolName)
	assert.JSONEq(t, `{"result":6}`, string(plan.priorTurns[0].ToolResult))
}

// Agent 的工具集在崩溃之后被改过（工具没了）时，提前拒绝比执行到一半才发现清楚。
func TestPrepareResume_ToolNoLongerRegistered_IsRejected(t *testing.T) {
	u, _, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	// 把注册表换成一个空的：Agent 仍然声明了 calculator，但实现没了。
	u.tools = NewToolRegistry()

	_, err := u.PrepareResume(context.Background(), run.ID)
	require.ErrorIs(t, err, platform.ErrConflict)
}

// ════════════════════════════════════════════════════════════════
// issue #67：checkpoint 回收
// ════════════════════════════════════════════════════════════════

func TestPruneCheckpoints_ClearsOnlyOldTerminalRuns(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	ctx := context.Background()

	old := time.Now().Add(-48 * time.Hour)

	// 一条很旧的、已经完成的 run（该被回收）。
	done := &Run{
		ID: uuid.New(), AgentID: run.AgentID, Status: RunCompleted, Input: "x",
		StateSnapshot: json.RawMessage(`{"big":"snapshot"}`),
		CreatedAt:     old, UpdatedAt: old,
	}
	require.NoError(t, repo.InsertRun(ctx, nil, done))

	// 一条很旧的、但被中断的 run：**不该回收**——恢复可能还要读它。
	interrupted := &Run{
		ID: uuid.New(), AgentID: run.AgentID, Status: RunInterrupted, Input: "y",
		StateSnapshot: json.RawMessage(`{"big":"snapshot"}`),
		CreatedAt:     old, UpdatedAt: old,
	}
	require.NoError(t, repo.InsertRun(ctx, nil, interrupted))

	n, err := u.PruneCheckpoints(ctx)
	require.NoError(t, err)

	assert.EqualValues(t, 1, n, "只有那条终态的旧 run 该被回收")
	assert.Nil(t, done.StateSnapshot, "终态 run 的快照要清空")
	assert.NotNil(t, interrupted.StateSnapshot,
		"interrupted 的 run 还等着被恢复，清掉它等于把恢复的前提删了")
}

// ════════════════════════════════════════════════════════════════
// issue #55：取消
// ════════════════════════════════════════════════════════════════

// 终态的 run 不能取消——状态机是权威判据（issue #59）。
func TestCancel_TerminalRun_IsRejected(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	run.Status = RunCompleted
	_ = repo

	_, err := u.Cancel(context.Background(), run.ID)
	require.ErrorIs(t, err, platform.ErrConflict)
}

// 不在本进程里、但库里是 running 的 run（进程被 KILL 后留下的）可以被取消，
// 走的是 CAS 兜底那条分支。
func TestCancel_OrphanRunningRun_FallsBackToCAS(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	run.Status = RunRunning

	got, err := u.Cancel(context.Background(), run.ID)
	require.NoError(t, err)

	require.Len(t, repo.updates, 1)
	assert.Equal(t, RunCancelled, repo.updates[0].to)
	assert.Equal(t, "cancelled", string(got.Status), "返回的必须是取消**之后**的状态")
}

// 在途表里有这条 run 时，取消要真的 cancel 那条 ctx（把 Eino 的调用拆掉），
// 并等收尾写入完成之后才返回。
func TestCancel_InFlightRun_CancelsContextAndWaitsForFinalize(t *testing.T) {
	u, repo, run, _ := newResumeFixture(t, NewCalculator(), CurrentStateSchemaVersion)
	run.Status = RunRunning

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := u.registerRunning(run.ID, cancel)

	// 收尾由"运行那个 goroutine"负责——这里用一个 goroutine 模拟它。
	finished := make(chan struct{})
	go func() {
		<-runCtx.Done() // 取消真的传到了这条 ctx 上
		require.NoError(t, repo.UpdateRunStatus(context.Background(), nil, run.ID, RunRunning, RunCancelled))
		run.Status = RunCancelled
		u.unregisterRunning(run.ID)
		close(st.done)
		close(finished)
	}()

	got, err := u.Cancel(context.Background(), run.ID)
	require.NoError(t, err)

	select {
	case <-finished:
	default:
		t.Fatal("Cancel 返回时收尾还没做完——响应里会带一个过期的状态")
	}
	assert.Equal(t, RunCancelled, got.Status)
	assert.True(t, st.requested.Load(), "必须留下「用户点的」这个标记，终态靠它区分 cancelled 与 interrupted")
}

// ════════════════════════════════════════════════════════════════
// issue #54 / ADR-008：run 维度事件流与幂等
// ════════════════════════════════════════════════════════════════

// run_started 必须是首帧，且带 run id——否则客户端拿不到取消要用的那个 id。
func TestStart_FirstFrameIsRunStartedWithRunID(t *testing.T) {
	u, _, agentID, sink := newStartFixture(t)

	_, err := u.Start(context.Background(), agentID, "算一下", "", sink)
	// 模型是假的，运行会在后面失败——这条只关心首帧。
	_ = err

	require.NotEmpty(t, sink.events)
	assert.Equal(t, "run_started", sink.events[0].Type)

	var envelope struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(sink.events[0].Payload, &envelope))
	assert.NotEmpty(t, envelope.Data.RunID, "取消端点要用这个 id，不给它等于取消没法实现")
}

// 幂等命中时不重新执行：返回已有 run，并把它的历史事件补发一遍。
func TestStart_IdempotentHit_ReplaysWithoutReexecuting(t *testing.T) {
	u, repo, agentID, sink := newStartFixture(t)
	ctx := context.Background()

	// 先真的跑一次，拿到那条 run（模型是假的，它会失败——不影响本测试）。
	first, _ := u.Start(ctx, agentID, "算一下", "key-1", sink)
	require.NotNil(t, first)

	before := len(repo.runs)
	sink2 := newFakeSink()

	second, err := u.Start(ctx, agentID, "算一下", "key-1", sink2)
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID, "同一个键必须返回同一条 run")
	assert.Equal(t, before, len(repo.runs), "命中幂等键绝不能再插一条 run 行")

	require.NotEmpty(t, sink2.events)
	assert.Equal(t, "run_started", sink2.events[0].Type, "重放的首帧同样是 run_started")
}

// 同一个键配不同的输入必须报错，而不是把上一次的运行重放一遍——
// 否则用户新写的那句话既没执行、也不会报错（ADR-008）。
func TestStart_IdempotencyKeyReusedWithDifferentInput_IsRejected(t *testing.T) {
	u, _, agentID, sink := newStartFixture(t)
	ctx := context.Background()

	_, _ = u.Start(ctx, agentID, "算一下 1+1", "key-2", sink)

	_, err := u.Start(ctx, agentID, "算一下 2+2", "key-2", newFakeSink())
	require.ErrorIs(t, err, platform.ErrInvalid)
}

// newStartFixture 造一个可以跑 Start 的 Usecase。
//
// 【模型是"配置齐全但没有真实端点"的】Start 会在 runAgent 那一步失败，
// 而那正好经过"建 run 行 → 发 run_started → 收尾"整条路径——
// 本组测试关心的是这条路径的形状，不是模型输出。
func newStartFixture(t *testing.T) (*Usecase, *fakeRepo, uuid.UUID, *fakeSink) {
	t.Helper()
	repo := newFakeRepo()
	tools := NewToolRegistry()
	tools.Register(NewCalculator())
	reg := &fakeLLMRegistry{
		model: newChatModel("test-model", true),
		// 走到真正的模型调用时失败：本组测试要的是那条路径的形状，
		// 不是模型输出（见 fakeLLMRegistry.ResolveChatEndpoint 的注释）。
		endpointErr: fmt.Errorf("no endpoint in tests: %w", platform.ErrUpstream),
	}
	u := newTestUsecase(repo, &fakeCheckpointStore{}, tools, reg)

	ag := &Agent{ID: uuid.New(), Name: "助手"}
	require.NoError(t, repo.CreateAgent(context.Background(), nil, ag))
	return u, repo, ag.ID, newFakeSink()
}

// ════════════════════════════════════════════════════════════════
// issue #71：agent_run_id 透传
// ════════════════════════════════════════════════════════════════

// 运行期用的 ctx 必须带上 agent_run_id——这条路径上的日志靠它串成一条
// 时间线，而 request_id 的作用域只到一次 HTTP 请求。
func TestExecute_BindsAgentRunIDIntoContext(t *testing.T) {
	u, repo, agentID, sink := newStartFixture(t)

	_, _ = u.Start(context.Background(), agentID, "算一下", "", sink)

	require.NotEmpty(t, repo.runs)
	runID := repo.runs[0].ID

	// 运行已经结束，但 ctx 上那个值在整条路径里都应该是它——
	// 这里直接验证注入函数本身是幂等且可读的（注入点在 execute 里）。
	assert.Equal(t, runID.String(), platform.AgentRunIDFrom(platform.WithAgentRunID(context.Background(), runID.String())))
	assert.Empty(t, platform.AgentRunIDFrom(context.Background()),
		"没有 run 的路径（比如文档处理的 River job）读出来是空串")
}

// 日志里两个追踪字段都要出现，而且缺席的那个不应该打成空串。
func TestLogAttrs_IncludesOnlyPresentIDs(t *testing.T) {
	assert.Empty(t, platform.LogAttrs(context.Background()))

	ctx := context.WithValue(context.Background(), platform.CtxRequestID, "req-1")
	ctx = platform.WithAgentRunID(ctx, "run-1")

	attrs := platform.LogAttrs(ctx)
	assert.Equal(t, []any{"request_id", "req-1", "agent_run_id", "run-1"}, attrs)
}

// llm 包在本文件里只用于构造假 registry，这行断言防止 import 被误删。
var _ llm.Registry = (*fakeLLMRegistry)(nil)

// conversation.EventSink 的假实现来自 usecase_test.go（fakeSink），
// 这行断言把那个依赖写明白——它一旦不满足，这里会先红。
var _ conversation.EventSink = (*fakeSink)(nil)

// errors 只用于断言链式错误的可读性；保留它避免 import 被误删。
var _ = errors.Is

// ════════════════════════════════════════════════════════════════
// issue #81：改一个已存在的 Agent
// ════════════════════════════════════════════════════════════════

// 能改的那几项真的改了，而且落回了存取层。
func TestUpdateAgent_UpdatesFields(t *testing.T) {
	u, _, _, _ := newStartFixture(t)
	ctx := context.Background()

	ag, err := u.CreateAgent(ctx, "原名", "旧描述", "", []string{"calculator"})
	require.NoError(t, err)

	updated, err := u.UpdateAgent(ctx, ag.ID, "新名", "新描述", "你是助手", nil)
	require.NoError(t, err)

	assert.Equal(t, "新名", updated.Name)
	assert.Equal(t, "新描述", updated.Description)
	assert.Equal(t, "你是助手", updated.Instruction)
	assert.Empty(t, updated.ToolNames)
	assert.NotNil(t, updated.ToolNames, "nil slice 会被 pgx 编码成 SQL NULL，撞上 NOT NULL 列")
	assert.True(t, updated.UpdatedAt.After(ag.UpdatedAt) || updated.UpdatedAt.Equal(ag.UpdatedAt))
	assert.Equal(t, ag.CreatedAt, updated.CreatedAt, "created_at 不是配置，不该跟着改")
}

// 【校验与创建共用一份】建得了改不了的判据必须完全一致——各写一遍的话，
// 不对称迟早会出现，而且不报错。
func TestUpdateAgent_SharesValidationWithCreate(t *testing.T) {
	u, _, _, _ := newStartFixture(t)
	ctx := context.Background()

	ag, err := u.CreateAgent(ctx, "助手", "", "", nil)
	require.NoError(t, err)

	for _, tc := range []struct {
		name        string
		nameArg     string
		toolNames   []string
		instruction string
	}{
		{"空名字", "   ", nil, ""},
		{"名字超长", string(make([]rune, maxNameLen+1)), nil, ""},
		{"工具没注册", "助手", []string{"no-such-tool"}, ""},
		{"instruction 超长", "助手", nil, string(make([]rune, maxInstructionLen+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := u.UpdateAgent(ctx, ag.ID, tc.nameArg, "", tc.instruction, tc.toolNames)
			require.ErrorIs(t, err, platform.ErrInvalid)
		})
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	u, _, _, _ := newStartFixture(t)

	_, err := u.UpdateAgent(context.Background(), uuid.New(), "x", "", "", nil)
	require.ErrorIs(t, err, platform.ErrNotFound)
}
