package retrieval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// ────────────────────────────────────────────────────────────────
// 假实现
// ────────────────────────────────────────────────────────────────

// fakeQuerier 按顺序吐出预先摆好的行集：第 n 次 Query 拿 queued[n]。
// 队列空了就返回空结果，让"多查了一次"由测试自己断言（见 queries）。
type fakeQuerier struct {
	queued  [][]chunkRow
	queries []string
}

func (q *fakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (q *fakeQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.queries = append(q.queries, sql)
	if len(q.queued) == 0 {
		return &fakeRows{}, nil
	}
	rows := q.queued[0]
	q.queued = q.queued[1:]
	return &fakeRows{rows: rows, cur: -1}, nil
}

func (q *fakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	panic("PgRepo.Search 不该走 QueryRow")
}

// chunkRow 就是 Search 的两条查询共同选出的五列。
type chunkRow struct {
	id       uuid.UUID
	docID    uuid.UUID
	content  string
	filename string
	score    float64
}

// fakeRows 是 pgx.Rows 的最小实现，只支持那五列的 Scan——够驱动 scanChunks
// 的行循环。别的读法（Values/RawValues/字段描述）没人用，返回空值即可。
type fakeRows struct {
	rows []chunkRow
	cur  int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) TypeMap() *pgtype.Map                         { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, errors.New("fakeRows 不支持 Values") }

func (r *fakeRows) Next() bool {
	r.cur++
	return r.cur < len(r.rows)
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.cur < 0 || r.cur >= len(r.rows) {
		return errors.New("fakeRows: Scan 在 Next 返回 false 之后被调用")
	}
	row := r.rows[r.cur]

	// 列的顺序就是两条 SELECT 的顺序：id, document_id, content, filename, score。
	if len(dest) != 5 {
		return fmt.Errorf("fakeRows: 期望 5 个扫描目标，收到 %d 个", len(dest))
	}
	id, ok := dest[0].(*uuid.UUID)
	if !ok {
		return fmt.Errorf("fakeRows: 第 1 列不是 *uuid.UUID")
	}
	*id = row.id
	docID, ok := dest[1].(*uuid.UUID)
	if !ok {
		return fmt.Errorf("fakeRows: 第 2 列不是 *uuid.UUID")
	}
	*docID = row.docID
	content, ok := dest[2].(*string)
	if !ok {
		return fmt.Errorf("fakeRows: 第 3 列不是 *string")
	}
	*content = row.content
	filename, ok := dest[3].(*string)
	if !ok {
		return fmt.Errorf("fakeRows: 第 4 列不是 *string")
	}
	*filename = row.filename
	score, ok := dest[4].(*float64)
	if !ok {
		return fmt.Errorf("fakeRows: 第 5 列不是 *float64")
	}
	*score = row.score
	return nil
}

// annRows 摆 n 行，用来模拟某一遍查询会返回什么。
func annRows(t *testing.T, n int) []chunkRow {
	t.Helper()
	rows := make([]chunkRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, chunkRow{
			id:       uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i)),
			docID:    uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0001-%012d", i)),
			content:  fmt.Sprintf("分块 %d", i),
			filename: fmt.Sprintf("doc-%d.md", i),
			score:    1 - float64(i)/100,
		})
	}
	return rows
}

// ────────────────────────────────────────────────────────────────
// Search：近似索引短缺时的兜底
// ────────────────────────────────────────────────────────────────

// 全局 HNSW 索引 + 上层过滤会让小知识库拿到不足 topK 行。这里用假 Querier
// 摆出"第一遍只回来 2 行、第二遍回来 5 行"的形状，断言 Search 最终给出
// 完整的 5 行。修复前 PgRepo 只跑那一遍近似查询，测试会在 Len(5) 上失败。
func TestSearch_FallsBackToExactScanWhenANNShort(t *testing.T) {
	q := &fakeQuerier{queued: [][]chunkRow{annRows(t, 2), annRows(t, 5)}}

	got, err := NewPgRepo().Search(context.Background(), q, uuid.New(), []float32{0.1, 0.2}, "bge-m3", 5)

	require.NoError(t, err)
	require.Len(t, got, 5, "近似索引短缺时必须由精确扫描补齐，不能静默少给")
	require.Equal(t, "doc-0.md", got[0].Filename, "兜底那遍的行也要正常读出 filename")
	require.Len(t, q.queries, 2)
	require.Contains(t, q.queries[1], "MATERIALIZED",
		"兜底那遍必须是先过滤后排序的形状——不加 MATERIALIZED 会被内联回原形状")
}

// 近似那遍就够 topK 时不该多跑一遍（大知识库/过滤选择率不高的常见情况）。
func TestSearch_KeepsANNResultWhenEnough(t *testing.T) {
	q := &fakeQuerier{queued: [][]chunkRow{annRows(t, 5)}}

	got, err := NewPgRepo().Search(context.Background(), q, uuid.New(), []float32{0.1, 0.2}, "bge-m3", 5)

	require.NoError(t, err)
	require.Len(t, got, 5)
	require.Len(t, q.queries, 1, "够了就不该再查一遍")
}

// 知识库本身的分块数就少于 topK（这里 3 行、topK=5）：兜底也得把 3 行原样
// 返回，而不是补到 5 行或报错。
func TestSearch_ExactFallbackAlsoShortWhenKBIsSmall(t *testing.T) {
	q := &fakeQuerier{queued: [][]chunkRow{annRows(t, 3), annRows(t, 3)}}

	got, err := NewPgRepo().Search(context.Background(), q, uuid.New(), []float32{0.1, 0.2}, "bge-m3", 5)

	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Len(t, q.queries, 2)
}

// 近似那遍返回 0 行（issue 里最坏的那种：SSE 里一个 citation 都没有）时，
// 兜底必须顶上。
func TestSearch_FallsBackWhenANNReturnsNothing(t *testing.T) {
	q := &fakeQuerier{queued: [][]chunkRow{nil, annRows(t, 5)}}

	got, err := NewPgRepo().Search(context.Background(), q, uuid.New(), []float32{0.1, 0.2}, "bge-m3", 5)

	require.NoError(t, err)
	require.Len(t, got, 5)
	require.Len(t, q.queries, 2)
}
