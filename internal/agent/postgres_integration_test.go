// 连真实 PostgreSQL 的集成测试——v4.0 加的那批 SQL。
//
// 【为什么这批 SQL 特别需要真库】它们全部是**新的写路径**，而它们都用假
// repo 的单测覆盖着：假实现复刻的是"我以为的语义"。真库才拦得住下面这一类
// 问题（每一条都至少在本仓库的历史上出现过一次）：
//
//   - 迁移里建的唯一约束名和 `platform.WrapPgErr` 里那个常量对不上
//     （分流静默失效，23505 被当成普通业务冲突）；
//   - 事务边界写错（`tool_effect_log` 必须独立提交，跟着步骤回滚就没了）；
//   - `coalesce(max(seq), 0)` 之类的聚合在空表上返回 NULL，Scan 进 int 直接报错；
//   - 级联删除的方向搞反（删 run 之后它的事件还在）。
//
// 【门控】设了 CONGORAG_TEST_DB_URL、或者设 CONGORAG_TESTCONTAINERS=1，
// 由 internal/testdb 决定（见那个包的注释）。
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/testdb"
)

// seedRun 造一条 run（连带它的 Agent），返回 run id。
func seedRun(t *testing.T, pool *pgxpool.Pool, status RunStatus, stateVersion int) *Run {
	t.Helper()
	ctx := context.Background()
	repo := NewPgRepo()

	ag := &Agent{ID: uuid.New(), Name: "集成测试 Agent", ToolNames: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, repo.CreateAgent(ctx, pool, ag))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM agents WHERE id = $1`, ag.ID)
	})

	run := &Run{
		ID: uuid.New(), AgentID: ag.ID, Status: status, Input: "算一下",
		StateSchemaVersion: stateVersion, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.InsertRun(ctx, pool, run))
	return run
}

// 【MaxStepSeq 在空表上不能炸】`max(seq)` 对空集返回 NULL，直接 Scan 进
// int 会报类型错——而"这条 run 一步都还没跑"是最常见的状态。
func TestPgRepo_MaxStepSeq_EmptyRunIsZero(t *testing.T) {
	pool := testdb.Require(t)
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	got, err := repo.MaxStepSeq(context.Background(), pool, run.ID)
	require.NoError(t, err, "空表上 max(seq) 是 NULL，必须 coalesce 掉")
	assert.Equal(t, 0, got)
}

func TestPgRepo_MaxStepSeq_ReturnsLargest(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	for _, seq := range []int{1, 2, 7} {
		require.NoError(t, repo.InsertStep(ctx, pool, &Step{
			ID: uuid.New(), RunID: run.ID, Seq: seq, Type: StepTypeLLM,
			Status: StepCompleted, CreatedAt: time.Now(),
		}))
	}

	got, err := repo.MaxStepSeq(ctx, pool, run.ID)
	require.NoError(t, err)
	assert.Equal(t, 7, got)
}

// 【UNIQUE (run_id, seq) 真的在挡重复编号】它是恢复路径从 1 重编号那个缺陷
// 的唯一防线——而那道防线本身是"静默生效"的（失败只被记成日志）。
func TestPgRepo_InsertStep_DuplicateSeqIsConflict(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	step := &Step{ID: uuid.New(), RunID: run.ID, Seq: 1, Type: StepTypeLLM, Status: StepCompleted, CreatedAt: time.Now()}
	require.NoError(t, repo.InsertStep(ctx, pool, step))

	err := repo.InsertStep(ctx, pool, &Step{
		ID: uuid.New(), RunID: run.ID, Seq: 1, Type: StepTypeLLM, Status: StepCompleted, CreatedAt: time.Now(),
	})
	require.Error(t, err, "同一个 run 里的 seq 必须唯一")
	assert.ErrorIs(t, err, platform.ErrDuplicateKey)
}

// 【tool_effect_log 的判读方向，在真库上跑一遍】
//
//	第一次记 = 正常路径
//	第二次记 = 23505，且必须被分流成 ErrToolEffectApplied（不是 ErrDuplicateKey）
//
// 这条同时钉住了"显式命名的约束名和 pgerr.go 里那个常量对得上"——
// 对不上的话分流静默失效，而恢复路径的整个判据就没了。
func TestPgRepo_ToolEffectLog_DuplicateIsDetected(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	step := &Step{ID: uuid.New(), RunID: run.ID, Seq: 1, Type: StepTypeTool, Status: StepRunning,
		ToolName: "calculator", CreatedAt: time.Now()}
	require.NoError(t, repo.InsertStep(ctx, pool, step))

	key := EffectKey("calculator", json.RawMessage(`{"a":1,"b":2,"operator":"+"}`))
	require.NoError(t, repo.RecordToolEffect(ctx, pool, step.ID, key))

	applied, err := repo.ToolEffectApplied(ctx, pool, step.ID, key)
	require.NoError(t, err)
	assert.True(t, applied)

	err = repo.RecordToolEffect(ctx, pool, step.ID, key)
	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrToolEffectApplied,
		"约束名对不上时这里会变成 ErrDuplicateKey——恢复路径的判据就没了")
	assert.NotErrorIs(t, err, platform.ErrDuplicateKey)
}

// 【级联删除的方向】删掉 run，它的 step 与效果账本都要跟着走。
// 方向搞反（或者少一条 ON DELETE CASCADE）不会报错，只会让库永远涨。
func TestPgRepo_DeleteRunCascadesToStepsAndEffects(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	step := &Step{ID: uuid.New(), RunID: run.ID, Seq: 1, Type: StepTypeTool, Status: StepCompleted,
		ToolName: "calculator", CreatedAt: time.Now()}
	require.NoError(t, repo.InsertStep(ctx, pool, step))
	require.NoError(t, repo.RecordToolEffect(ctx, pool, step.ID, "k"))

	_, err := pool.Exec(ctx, `DELETE FROM agent_runs WHERE id = $1`, run.ID)
	require.NoError(t, err)

	var steps, effects int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_run_steps WHERE run_id = $1`, run.ID).Scan(&steps))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tool_effect_log WHERE step_id = $1`, step.ID).Scan(&effects))
	assert.Zero(t, steps, "step 要跟着 run 走")
	assert.Zero(t, effects, "效果账本要跟着 step 走——留着它就是一份指向不存在步骤的账")
}

// ── 状态扫描与 checkpoint 回收 ──────────────────────────────────

// 【启动扫描的两条 UPDATE 必须在同一个事务里】否则会留下"run 已经不是
// running 了、但它的 step 还在 running"的中间态——而那个中间态正是恢复
// 要判读的现场，读到它会再收尾一遍。
func TestPgRepo_InterruptRunningRuns_MarksBothRunAndSteps(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	repo := NewPgRepo()

	// 一条该被扫走的，一条不该被动到的。
	victim := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	bystander := seedRun(t, pool, RunCompleted, CurrentStateSchemaVersion)

	require.NoError(t, repo.InsertStep(ctx, pool, &Step{
		ID: uuid.New(), RunID: victim.ID, Seq: 1, Type: StepTypeLLM, Status: StepRunning, CreatedAt: time.Now(),
	}))
	require.NoError(t, repo.InsertStep(ctx, pool, &Step{
		ID: uuid.New(), RunID: victim.ID, Seq: 2, Type: StepTypeLLM, Status: StepCompleted, CreatedAt: time.Now(),
	}))

	ids, err := repo.InterruptRunningRuns(ctx, pool)
	require.NoError(t, err)
	assert.Contains(t, ids, victim.ID)

	got, err := repo.GetRun(ctx, pool, victim.ID)
	require.NoError(t, err)
	assert.Equal(t, RunInterrupted, got.Status)

	steps, err := repo.StepsByRun(ctx, pool, victim.ID)
	require.NoError(t, err)
	require.Len(t, steps, 2)
	assert.Equal(t, StepInterrupted, steps[0].Status, "没跑完的那一步要跟着标 interrupted")
	assert.Equal(t, StepCompleted, steps[1].Status, "已经完成的那一步不能被动到")

	stillDone, err := repo.GetRun(ctx, pool, bystander.ID)
	require.NoError(t, err)
	assert.Equal(t, RunCompleted, stillDone.Status, "终态的 run 不该被这次扫描碰到")
}

// 【回收只动 state_snapshot 那一列】把整个 run 删掉也能"通过"一条只查
// snapshot 的断言，所以这里连着断言 input/status 还在。
func TestPgRepo_ClearTerminalRunCheckpoints_OnlyClearsSnapshot(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	repo := NewPgRepo()

	old := seedRun(t, pool, RunCompleted, CurrentStateSchemaVersion)
	_, err := pool.Exec(ctx,
		`UPDATE agent_runs SET state_snapshot = $2, updated_at = $3 WHERE id = $1`,
		old.ID, json.RawMessage(`{"big":"snapshot"}`), time.Now().Add(-48*time.Hour))
	require.NoError(t, err)

	// 一条很旧但**仍在恢复窗口里**的 run：不该被回收。
	waiting := seedRun(t, pool, RunInterrupted, CurrentStateSchemaVersion)
	_, err = pool.Exec(ctx,
		`UPDATE agent_runs SET state_snapshot = $2, updated_at = $3 WHERE id = $1`,
		waiting.ID, json.RawMessage(`{"big":"snapshot"}`), time.Now().Add(-48*time.Hour))
	require.NoError(t, err)

	n, err := repo.ClearTerminalRunCheckpoints(ctx, pool, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	cleared, err := repo.GetRun(ctx, pool, old.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.StateSnapshot)
	assert.Equal(t, RunCompleted, cleared.Status, "只清快照，不动状态")
	assert.Equal(t, "算一下", cleared.Input, "也不动 input——历史还有价值")

	kept, err := repo.GetRun(ctx, pool, waiting.ID)
	require.NoError(t, err)
	assert.NotNil(t, kept.StateSnapshot, "interrupted 的 run 还等着被恢复，不能清")
}

// ── run 维度的事件流 ────────────────────────────────────────────

// 发号从 1 开始、单调递增，`after_event_id` 是严格大于——断线续传的语义
// 全压在这三条上，而它们在假 repo 里是照抄的。
func TestPgRepo_RunEvents_NumberingAndCursor(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	// 发号与写入在同一个事务里（usecase 负责这点，这里直接连着调）。
	require.NoError(t, repo.AppendRunEvent(ctx, pool, run.ID, RunEvent{
		ID: 1, Type: "run_started", Payload: json.RawMessage(`{"type":"run_started"}`),
	}))
	require.NoError(t, repo.AppendRunEvent(ctx, pool, run.ID, RunEvent{
		ID: 2, Type: "token", Payload: json.RawMessage(`{"type":"token"}`),
	}))

	all, err := repo.RunEventsAfter(ctx, pool, run.ID, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2, "after_event_id=0 表示从头补发整个 run")

	tail, err := repo.RunEventsAfter(ctx, pool, run.ID, 1)
	require.NoError(t, err)
	require.Len(t, tail, 1)
	assert.Equal(t, int64(2), tail[0].ID)
	assert.Equal(t, "token", tail[0].Type, "Type 要从 payload 里还原出来")
}

// 计数器是"下一个要发的号"，从 1 开始；同一个 run 连调两次要拿到 1、2。
func TestPgRepo_NextRunEventID_StartsAtOne(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	run := seedRun(t, pool, RunRunning, CurrentStateSchemaVersion)
	repo := NewPgRepo()

	first, err := repo.NextRunEventID(ctx, pool, run.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), first)

	second, err := repo.NextRunEventID(ctx, pool, run.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), second)
}

// ── Agent 的编辑与幂等键 ────────────────────────────────────────

// UpdateAgent 必须真的改到行（`RowsAffected == 0` → not_found 那条路径
// 反过来也说明：改得到的时候不能报错）。
func TestPgRepo_UpdateAgent(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	repo := NewPgRepo()

	ag := &Agent{ID: uuid.New(), Name: "原名", ToolNames: []string{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, repo.CreateAgent(ctx, pool, ag))
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM agents WHERE id = $1`, ag.ID) })

	ag.Name = "新名"
	ag.Instruction = "你是助手"
	ag.ToolNames = []string{"calculator"}
	ag.UpdatedAt = time.Now()
	require.NoError(t, repo.UpdateAgent(ctx, pool, ag))

	got, err := repo.GetAgent(ctx, pool, ag.ID)
	require.NoError(t, err)
	assert.Equal(t, "新名", got.Name)
	assert.Equal(t, []string{"calculator"}, got.ToolNames)
	assert.Equal(t, "你是助手", got.Instruction)

	// 不存在的 id → not_found（RowsAffected == 0）。
	err = repo.UpdateAgent(ctx, pool, &Agent{ID: uuid.New(), Name: "x", ToolNames: []string{}})
	require.ErrorIs(t, err, platform.ErrNotFound)
}

// 幂等键的作用域是 (endpoint, key)，而那张表的主键约束名被
// pgerr.go 的常量直接引用——这条在真库上确认它仍然命中 ErrIdempotentHit。
func TestPgIdempotencyStore_ReserveAndHit(t *testing.T) {
	pool := testdb.Require(t)
	ctx := context.Background()
	store := NewPgIdempotencyStore()

	rec := &IdempotencyRecord{
		Endpoint: "POST /api/v1/agents/" + uuid.NewString() + "/runs",
		Key:      "integration-" + uuid.NewString(), ResourceType: "agent_run",
		ResourceID: uuid.New(), RequestFingerprint: "fp", CreatedAt: time.Now(),
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE endpoint = $1 AND idempotency_key = $2`, rec.Endpoint, rec.Key)
	})

	require.NoError(t, store.ReserveIdempotencyKey(ctx, pool, rec, time.Now().Add(-platform.IdempotencyKeyTTL)))

	err := store.ReserveIdempotencyKey(ctx, pool, rec, time.Now().Add(-platform.IdempotencyKeyTTL))
	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrIdempotentHit,
		"约束名对不上时这里会变成 ErrDuplicateKey，而幂等重放会被当成 409")

	back, err := store.LookupIdempotencyKey(ctx, pool, rec.Endpoint, rec.Key)
	require.NoError(t, err)
	assert.Equal(t, rec.ResourceID, back.ResourceID)
}
