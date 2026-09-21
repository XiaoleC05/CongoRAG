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
