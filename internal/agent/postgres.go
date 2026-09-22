// Repo 的 PostgreSQL 实现。所有 SQL 都收敛在这个文件里。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Repo = (*PgRepo)(nil)

// PgRepo 是无状态的，连接由调用方通过 q 参数传入——和其余业务包的
// PgRepo 同一个模式。
type PgRepo struct{}

func NewPgRepo() *PgRepo {
	return &PgRepo{}
}

func (r *PgRepo) CreateAgent(ctx context.Context, q platform.Querier, a *Agent) error {
	_, err := q.Exec(ctx,
		`INSERT INTO agents (id, name, description, instruction, tool_names, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		a.ID, a.Name, a.Description, a.Instruction, a.ToolNames, a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create agent %s: %w", a.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) GetAgent(ctx context.Context, q platform.Querier, id uuid.UUID) (*Agent, error) {
	a := &Agent{}
	err := q.QueryRow(ctx,
		`SELECT id, name, description, instruction, tool_names, created_at, updated_at
		 FROM agents WHERE id = $1`, id,
	).Scan(&a.ID, &a.Name, &a.Description, &a.Instruction, &a.ToolNames, &a.CreatedAt, &a.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("agent %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", id, platform.WrapPgErr(err))
	}
	return a, nil
}

// UpdateAgent 见 port.go 的契约。
//
// 【没有把 created_at 也 SET 一遍】它不是配置；跟着改会让"这个 Agent 是
// 什么时候建的"变成一个会漂的值（而列表按 created_at 排序）。
func (r *PgRepo) UpdateAgent(ctx context.Context, q platform.Querier, a *Agent) error {
	tag, err := q.Exec(ctx,
		`UPDATE agents
		    SET name = $2, description = $3, instruction = $4, tool_names = $5, updated_at = $6
		  WHERE id = $1`,
		a.ID, a.Name, a.Description, a.Instruction, a.ToolNames, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update agent %s: %w", a.ID, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("agent %s: %w", a.ID, platform.ErrNotFound)
	}
	return nil
}

func (r *PgRepo) ListAgents(ctx context.Context, q platform.Querier) ([]*Agent, error) {
	rows, err := q.Query(ctx,
		`SELECT id, name, description, instruction, tool_names, created_at, updated_at
		 FROM agents ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Instruction, &a.ToolNames, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return out, nil
}

func (r *PgRepo) InsertRun(ctx context.Context, q platform.Querier, run *Run) error {
	_, err := q.Exec(ctx,
		`INSERT INTO agent_runs (id, agent_id, status, current_step, input, output,
		                          state_snapshot, state_schema_version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		run.ID, run.AgentID, string(run.Status), run.CurrentStep, run.Input, run.Output,
		nullableJSON(run.StateSnapshot), run.StateSchemaVersion, run.CreatedAt, run.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert run %s: %w", run.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*Run, error) {
	run, err := scanRun(q.QueryRow(ctx,
		`SELECT id, agent_id, status, current_step, input, output,
		        state_snapshot, state_schema_version, created_at, updated_at
		 FROM agent_runs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("run %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", id, platform.WrapPgErr(err))
	}
	return run, nil
}

// agentRunsSelectCols / 两条列表 SQL：见 knowledge 的 documentsSelectCols
// 同一套写法与理由（keyset 行值比较、方向与索引一致、多取一条判 hasMore）。
const agentRunsSelectCols = `SELECT id, agent_id, status, current_step, input, output,
	        state_snapshot, state_schema_version, created_at, updated_at
	 FROM agent_runs`

const listRunsWithCursorSQL = agentRunsSelectCols + `
	 WHERE agent_id = $1 AND (created_at, id) < ($2, $3)
	 ORDER BY created_at DESC, id DESC
	 LIMIT $4`

const listRunsSQL = agentRunsSelectCols + `
	 WHERE agent_id = $1
	 ORDER BY created_at DESC, id DESC
	 LIMIT $2`

func (r *PgRepo) ListRunsByAgent(ctx context.Context, q platform.Querier, agentID uuid.UUID, cur *platform.ListCursor, limit int) ([]*Run, bool, error) {
	sql := listRunsSQL
	args := []any{agentID}

	if cur != nil {
		ts, err := time.Parse(time.RFC3339Nano, cur.SortKey)
		if err != nil {
			return nil, false, fmt.Errorf("cursor sort key %q is not a timestamp: %w", cur.SortKey, platform.ErrInvalid)
		}
		id, err := uuid.Parse(cur.Tiebreak)
		if err != nil {
			return nil, false, fmt.Errorf("cursor tiebreak %q is not a uuid: %w", cur.Tiebreak, platform.ErrInvalid)
		}
		sql = listRunsWithCursorSQL
		args = append(args, ts, id)
	}
	args = append(args, limit+1)

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list runs of agent %s: %w", agentID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	out := make([]*Run, 0, limit)
	for rows.Next() {
		run, err := scanRunRow(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate runs: %w", err)
	}

	// DESC 序，多取的那条在尾部——先切再返回。
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// UpdateRunStatus 用 CAS（WHERE status = $2）——见 port.go 的注释,
// 和 knowledge.PgDocRepo.UpdateStatus 同样的模式,只是这一轮没有一个
// 独立的"合法迁移表"Go 层校验,只靠数据库这一层挡并发写入。
func (r *PgRepo) UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to RunStatus) error {
	tag, err := q.Exec(ctx,
		`UPDATE agent_runs SET status = $3, updated_at = $4 WHERE id = $1 AND status = $2`,
		id, string(from), string(to), time.Now())
	if err != nil {
		return fmt.Errorf("update run %s status: %w", id, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s: expected status %s but no row matched: %w", id, from, platform.ErrConflict)
	}
	return nil
}

func (r *PgRepo) InsertStep(ctx context.Context, q platform.Querier, s *Step) error {
	_, err := q.Exec(ctx,
		`INSERT INTO agent_run_steps (id, run_id, seq, type, status, tool_name, tool_args,
		                               tool_result, token_usage, latency_ms, error, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		s.ID, s.RunID, s.Seq, s.Type, string(s.Status), nullableString(s.ToolName),
		nullableJSON(s.ToolArgs), nullableJSON(s.ToolResult), tokenUsageJSON(s.TokenUsage),
		s.LatencyMS, nullableString(s.Error), s.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert step %d of run %s: %w", s.Seq, s.RunID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*Step, error) {
	rows, err := q.Query(ctx,
		`SELECT id, run_id, seq, type, status, tool_name, tool_args, tool_result,
		        token_usage, latency_ms, error, created_at
		 FROM agent_run_steps WHERE run_id = $1 ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("steps of run %s: %w", runID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []*Step
	for rows.Next() {
		s := &Step{}
		var status string
		var toolName, errMsg *string
		var toolArgs, toolResult, tokenUsageRaw []byte
		if err := rows.Scan(&s.ID, &s.RunID, &s.Seq, &s.Type, &status, &toolName, &toolArgs,
			&toolResult, &tokenUsageRaw, &s.LatencyMS, &errMsg, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		s.Status = StepStatus(status)
		if toolName != nil {
			s.ToolName = *toolName
		}
		if errMsg != nil {
			s.Error = *errMsg
		}
		s.ToolArgs = toolArgs
		s.ToolResult = toolResult
		s.TokenUsage = unmarshalTokenUsage(tokenUsageRaw)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate steps: %w", err)
	}
	return out, nil
}

// UpdateStep 补上一步的终态、结果与延迟。
//
// 【只改会变的那几列】不更新 seq / run_id / type / tool_name / tool_args /
// created_at——它们是一次写入就定死的（见 port.go 的注释）。
//
// 【不加 CAS 的 WHERE status = ...】和 UpdateRunStatus 不同：run 的终态
// 竞争很真实（取消与失败可能同时发生），而 step 只有一个写者——就是正在
// 执行它的那个 goroutine。行不存在说明调用方把 step id 用错了，那是个
// 编程错误，值得报出来而不是静默成功。
func (r *PgRepo) UpdateStep(ctx context.Context, q platform.Querier, s *Step) error {
	tag, err := q.Exec(ctx,
		`UPDATE agent_run_steps
		    SET status = $2, tool_result = $3, token_usage = $4, latency_ms = $5, error = $6
		  WHERE id = $1`,
		s.ID, string(s.Status), nullableJSON(s.ToolResult), tokenUsageJSON(s.TokenUsage),
		s.LatencyMS, nullableString(s.Error),
	)
	if err != nil {
		return fmt.Errorf("update step %d of run %s: %w", s.Seq, s.RunID, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("step %s: %w", s.ID, platform.ErrNotFound)
	}
	return nil
}

// ── run 维度事件流（issue #54）──────────────────────────────────

// NextRunEventID 实现与 conversation.PgRepo.NextEventID 逐字同形的 SQL，
// 只是换了一张计数器表。两张表并存的理由见 migrations/0009 的注释。
func (r *PgRepo) NextRunEventID(ctx context.Context, q platform.Querier, runID uuid.UUID) (int64, error) {
	var next int64
	err := q.QueryRow(ctx,
		`INSERT INTO run_counters (run_id, next_event_id)
		 VALUES ($1, 1)
		 ON CONFLICT (run_id) DO UPDATE
		   SET next_event_id = run_counters.next_event_id + 1
		 RETURNING next_event_id`,
		runID,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("allocate next event id for run %s: %w", runID, platform.WrapPgErr(err))
	}
	return next, nil
}

func (r *PgRepo) AppendRunEvent(ctx context.Context, q platform.Querier, runID uuid.UUID, ev RunEvent) error {
	_, err := q.Exec(ctx,
		`INSERT INTO run_events (run_id, event_id, payload) VALUES ($1, $2, $3)`,
		runID, ev.ID, ev.Payload,
	)
	if err != nil {
		return fmt.Errorf("append event %d of run %s: %w", ev.ID, runID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) RunEventsAfter(ctx context.Context, q platform.Querier, runID uuid.UUID, afterEventID int64) ([]RunEvent, error) {
	rows, err := q.Query(ctx,
		`SELECT event_id, payload FROM run_events
		 WHERE run_id = $1 AND event_id > $2
		 ORDER BY event_id ASC`,
		runID, afterEventID)
	if err != nil {
		return nil, fmt.Errorf("events after %d of run %s: %w", afterEventID, runID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []RunEvent
	for rows.Next() {
		var ev RunEvent
		var payload []byte
		if err := rows.Scan(&ev.ID, &payload); err != nil {
			return nil, fmt.Errorf("scan run event: %w", err)
		}
		ev.Payload = payload
		ev.Type = extractEventType(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run events: %w", err)
	}
	return out, nil
}

// extractEventType 从 payload 里取出 "type" 字段。
//
// 【为什么从 JSON 里解，而不是给表加一列 type】表结构照 conversation_events
// 来（两张表同形，读代码时不用在两个形状之间切换），而类型信息本来就编码在
// payload 里——Event.Type 与 payload.type 是同一个值的两个投影。
//
// 【认不出就返回空串，不报错】这个方法只在读取路径上被调用，而它服务的
// 那两个端点（补发、重放）的语义是"把存下来的东西原样发出去"。因为一个
// 解不出的 type 就整条报错，会让一条坏行把整个 run 的补发全部堵死。
//
// 【实现与 conversation 的同名函数是两份副本，不是漏改】两个包各自持有
// 一份，代价是两处要一起改；收益是不互相依赖对方的私有实现（本仓库一贯
// 的取舍，见 tokenUsageJSON 的注释）。
func extractEventType(payload []byte) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return ""
	}
	return probe.Type
}

// ── 工具效果账本（issue #63）────────────────────────────────────

func (r *PgRepo) RecordToolEffect(ctx context.Context, q platform.Querier, stepID uuid.UUID, effectKey string) error {
	_, err := q.Exec(ctx,
		`INSERT INTO tool_effect_log (step_id, effect_key) VALUES ($1, $2)`,
		stepID, effectKey)
	if err != nil {
		// 23505 在这里被分流成 ErrToolEffectApplied（不是 ErrDuplicateKey）：
		// 它的含义是"这个副作用已经发生过了"，恢复路径要据此判读，
		// 而不是把它当成一次业务冲突（见 sentinel.go 的注释）。
		return fmt.Errorf("record tool effect for step %s: %w", stepID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgRepo) ToolEffectApplied(ctx context.Context, q platform.Querier, stepID uuid.UUID, effectKey string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tool_effect_log WHERE step_id = $1 AND effect_key = $2)`,
		stepID, effectKey).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check tool effect for step %s: %w", stepID, platform.WrapPgErr(err))
	}
	return exists, nil
}

// ── 崩溃扫描与 checkpoint 回收 ──────────────────────────────────

// InterruptRunningRuns 见 port.go 的契约。
//
// 【两条 UPDATE 必须在同一个事务里】否则会出现"run 已经不是 running 了，
// 但它那个 running 的 step 还在"的中间态——正是 ADR-007 崩溃表要判读的
// 那个现场。恢复逻辑读到它会把一个已经由扫描收尾的 run 再收尾一遍。
//
// 【为什么最后单独 SELECT 一次受影响的 id】UPDATE ... RETURNING 只返回被
// 改的行，而这里要的是 run 级 id 列表（step 那条 UPDATE 会返回一堆 step id，
// 不是 run id）。多一次查询换来调用方能拿它记日志——这是启动扫描唯一的
// 可观测性来源。
func (r *PgRepo) InterruptRunningRuns(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	tag, err := q.Exec(ctx,
		`UPDATE agent_runs SET status = $1, updated_at = now() WHERE status = $2`,
		string(RunInterrupted), string(RunRunning))
	if err != nil {
		return nil, fmt.Errorf("interrupt running runs: %w", platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}

	if _, err := q.Exec(ctx,
		`UPDATE agent_run_steps s
		    SET status = $1
		   FROM agent_runs r
		  WHERE s.run_id = r.id AND s.status = $2 AND r.status = $3`,
		string(StepInterrupted), string(StepRunning), string(RunInterrupted)); err != nil {
		return nil, fmt.Errorf("interrupt running steps: %w", platform.WrapPgErr(err))
	}

	rows, err := q.Query(ctx,
		`SELECT id FROM agent_runs WHERE status = $1 ORDER BY created_at ASC`,
		string(RunInterrupted))
	if err != nil {
		return nil, fmt.Errorf("list interrupted runs: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan interrupted run id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interrupted runs: %w", err)
	}
	return ids, nil
}

// ClearTerminalRunCheckpoints 见 port.go 的契约。
//
// 【只动 state_snapshot 这一列】output / input / steps 都留着：轨迹页、
// 运行历史、用量都还要读它们。膨胀的只有快照。
func (r *PgRepo) ClearTerminalRunCheckpoints(ctx context.Context, q platform.Querier, olderThan time.Time) (int64, error) {
	tag, err := q.Exec(ctx,
		`UPDATE agent_runs
		    SET state_snapshot = NULL
		  WHERE state_snapshot IS NOT NULL
		    AND updated_at < $1
		    AND status IN ($2, $3, $4)`,
		olderThan,
		string(RunCompleted), string(RunFailed), string(RunCancelled))
	if err != nil {
		return 0, fmt.Errorf("clear terminal run checkpoints: %w", platform.WrapPgErr(err))
	}
	return tag.RowsAffected(), nil
}

// ── 幂等键（issue #56 / ADR-008）────────────────────────────────

var _ IdempotencyStore = (*PgIdempotencyStore)(nil)

// PgIdempotencyStore 是 agent 包声明的 IdempotencyStore 的实现。
//
// 【和 conversation.PgRepo 的同类方法共用同一张表】两个包的实现都过
// platform.WrapPgErr，所以"幂等命中不是 409"只有一个实现点；
// 各自一份 SQL 副本是本仓库对"不互相依赖私有实现"的一贯取舍。
type PgIdempotencyStore struct{}

func NewPgIdempotencyStore() *PgIdempotencyStore {
	return &PgIdempotencyStore{}
}

func (s *PgIdempotencyStore) ReserveIdempotencyKey(ctx context.Context, q platform.Querier, rec *IdempotencyRecord, expiredBefore time.Time) error {
	// 清理与插入放在同一次调用里，与 conversation 那一侧的理由一致：
	// 分开做会留下一个两边都没覆盖到的窗口。
	if _, err := q.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < $1`, expiredBefore); err != nil {
		return fmt.Errorf("prune expired idempotency keys: %w", platform.WrapPgErr(err))
	}

	_, err := q.Exec(ctx,
		`INSERT INTO idempotency_keys
		   (endpoint, idempotency_key, resource_type, resource_id,
		    first_event_id, request_fingerprint, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rec.Endpoint, rec.Key, rec.ResourceType, rec.ResourceID,
		rec.FirstEventID, rec.RequestFingerprint, rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("reserve idempotency key for run: %w", platform.WrapPgErr(err))
	}
	return nil
}

func (s *PgIdempotencyStore) LookupIdempotencyKey(ctx context.Context, q platform.Querier, endpoint, key string) (*IdempotencyRecord, error) {
	rec := &IdempotencyRecord{}
	err := q.QueryRow(ctx,
		`SELECT endpoint, idempotency_key, resource_type, resource_id,
		        first_event_id, request_fingerprint, created_at
		 FROM idempotency_keys
		 WHERE endpoint = $1 AND idempotency_key = $2`,
		endpoint, key,
	).Scan(&rec.Endpoint, &rec.Key, &rec.ResourceType, &rec.ResourceID,
		&rec.FirstEventID, &rec.RequestFingerprint, &rec.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("idempotency key %s: %w", key, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("lookup idempotency key %s: %w", key, platform.WrapPgErr(err))
	}
	return rec, nil
}

// MaxStepSeq 见 port.go 的契约。
//
// 【用 coalesce 而不是在 Go 里判 NULL】没有步骤时 max(seq) 是 NULL，直接
// Scan 进 int 会报类型错——那是一条只要跑过一次空 run 就会撞上的路径。
func (r *PgRepo) MaxStepSeq(ctx context.Context, q platform.Querier, runID uuid.UUID) (int, error) {
	var maxSeq int
	err := q.QueryRow(ctx,
		`SELECT coalesce(max(seq), 0) FROM agent_run_steps WHERE run_id = $1`, runID).Scan(&maxSeq)
	if err != nil {
		return 0, fmt.Errorf("max step seq of run %s: %w", runID, platform.WrapPgErr(err))
	}
	return maxSeq, nil
}

func (r *PgRepo) ListToolCatalog(ctx context.Context, q platform.Querier) ([]ToolCatalogEntry, error) {
	rows, err := q.Query(ctx, `SELECT name, description, side_effect_level FROM tools ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list tool catalog: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []ToolCatalogEntry
	for rows.Next() {
		var e ToolCatalogEntry
		var level string
		if err := rows.Scan(&e.Name, &e.Description, &level); err != nil {
			return nil, fmt.Errorf("scan tool catalog entry: %w", err)
		}
		e.SideEffectLevel = SideEffectLevel(level)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tool catalog: %w", err)
	}
	return out, nil
}

var _ CheckpointStore = (*PgCheckpointStore)(nil)

// PgCheckpointStore 是 CheckpointStore 唯一的实现——单独一个类型,
// 不是 PgRepo 的方法,呼应 port.go 的注释：CheckpointStore 和 Repo
// 是两个独立的抽象（前者服务 M4-C 的 Resume,后者服务一般的 CRUD),
// 即使这一轮它们的底层 SQL 都落在 agent_runs 这张表上。
type PgCheckpointStore struct{}

func NewPgCheckpointStore() *PgCheckpointStore {
	return &PgCheckpointStore{}
}

func (c *PgCheckpointStore) Save(ctx context.Context, q platform.Querier, runID uuid.UUID, step int, output string, snap json.RawMessage) error {
	tag, err := q.Exec(ctx,
		`UPDATE agent_runs SET current_step = $2, output = $3, state_snapshot = $4, updated_at = $5 WHERE id = $1`,
		runID, step, output, nullableJSON(snap), time.Now())
	if err != nil {
		return fmt.Errorf("save checkpoint for run %s: %w", runID, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s: %w", runID, platform.ErrNotFound)
	}
	return nil
}

func (c *PgCheckpointStore) Load(ctx context.Context, q platform.Querier, runID uuid.UUID) (*Run, error) {
	run, err := scanRun(q.QueryRow(ctx,
		`SELECT id, agent_id, status, current_step, input, output,
		        state_snapshot, state_schema_version, created_at, updated_at
		 FROM agent_runs WHERE id = $1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("run %s: %w", runID, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load checkpoint for run %s: %w", runID, platform.WrapPgErr(err))
	}
	return run, nil
}

func scanRun(row pgx.Row) (*Run, error) {
	run := &Run{}
	var status string
	var stateSnapshot []byte
	err := row.Scan(&run.ID, &run.AgentID, &status, &run.CurrentStep, &run.Input, &run.Output,
		&stateSnapshot, &run.StateSchemaVersion, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return nil, err
	}
	run.Status = RunStatus(status)
	run.StateSnapshot = stateSnapshot
	return run, nil
}

func scanRunRow(rows pgx.Rows) (*Run, error) {
	run := &Run{}
	var status string
	var stateSnapshot []byte
	err := rows.Scan(&run.ID, &run.AgentID, &status, &run.CurrentStep, &run.Input, &run.Output,
		&stateSnapshot, &run.StateSchemaVersion, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return nil, err
	}
	run.Status = RunStatus(status)
	run.StateSnapshot = stateSnapshot
	return run, nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableJSON(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// tokenUsageJSON/unmarshalTokenUsage：和 conversation 包 postgres.go 里
// 同名函数一样的取舍（序列化失败就返回 nil/丢弃这一列,不让整行操作
// 失败）,各自一份独立副本——两个包不允许互相依赖对方的私有实现细节。
func tokenUsageJSON(tu *domain.TokenUsage) []byte {
	if tu == nil {
		return nil
	}
	b, err := json.Marshal(tu)
	if err != nil {
		return nil
	}
	return b
}

func unmarshalTokenUsage(raw []byte) *domain.TokenUsage {
	if len(raw) == 0 {
		return nil
	}
	var tu domain.TokenUsage
	if err := json.Unmarshal(raw, &tu); err != nil {
		return nil
	}
	return &tu
}
