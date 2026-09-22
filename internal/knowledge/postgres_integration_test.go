// 连真实 PostgreSQL 的集成测试（issue #45 的分页 SQL）。
//
// 【为什么分页必须有真库测试】分页的失败方式全都无声：行值比较写错方向、
// ORDER BY 与索引列顺序不一致导致退化成「索引扫 + Sort」、DESC 序下切错那一端
// ——单测里的假 repo 复刻的是**我以为的**语义，只有真库能证明 SQL 本身对。
//
// 【门控】设了 CONGORAG_TEST_DB_URL 才跑。make test-integration 会把它指向
// 一个每次重建的独立测试库。
package knowledge

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/testdb"
)

// requireTestDB 返回一个 schema 已就绪的测试库（issue #70）。
//
// 具体从哪来由 internal/testdb 决定：设了 CONGORAG_TEST_DB_URL 就用它
// （并校验 schema 在不在），没设就自己起一个容器。这个函数只剩一行委托——
// 在此之前四个包各抄了一份门控逻辑，而"测试库该长什么样"因此有四个副本。
func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testdb.Require(t)
}

// seedDocsForPagination 造 n 份文档，**故意让前两条的 created_at 完全相同**
// ——同一毫秒创建两行是真实会发生的（时间戳由 Go 的 time.Now() 生成），
// 而只按时间排序的实现在这种情况下会稳定地多出或漏掉一行。
func seedDocsForPagination(t *testing.T, pool *pgxpool.Pool, n int) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	kbID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO knowledge_bases (id, name) VALUES ($1, $2)`, kbID, "pagination-probe")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, kbID)
	})

	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		// 前两条同一时刻——这是有意制造的并列。
		at := base.Add(time.Duration(i) * time.Second)
		if i < 2 {
			at = base
		}
		_, err := pool.Exec(ctx,
			`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, 'ready', $5, $5)`,
			uuid.New(), kbID, "doc.md", "pagination-probe-"+uuid.NewString(), at)
		require.NoError(t, err)
	}
	return kbID
}

// 【这是分页最重要的性质】把每一页接起来，必须恰好等于全量：不重、不漏、
// 严格按 (created_at DESC, id DESC)。任何一处 SQL 写错都会在这里红。
func TestIntegration_ListByKnowledgeBase_PagesConcatenateToTheFullSet(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgDocRepo()
	const total = 7
	kbID := seedDocsForPagination(t, pool, total)

	var got []*Document
	var cur *platform.ListCursor
	for i := 0; i < 20; i++ { // 防死循环的上限
		page, hasMore, err := repo.ListByKnowledgeBase(ctx, pool, kbID, cur, 3)
		require.NoError(t, err, "第 %d 页", i)
		require.LessOrEqual(t, len(page), 3)
		got = append(got, page...)
		if !hasMore {
			break
		}
		require.NotEmpty(t, page, "hasMore 为真时必须给了至少一行")
		last := page[len(page)-1]
		cur = &platform.ListCursor{
			SortKey:  last.CreatedAt.Format(time.RFC3339Nano),
			Tiebreak: last.ID.String(),
		}
	}

	require.Len(t, got, total, "两页拼起来必须恰好是全量，不重不漏")

	seen := map[uuid.UUID]bool{}
	for i, d := range got {
		assert.False(t, seen[d.ID], "第 %d 条重复：%s", i, d.ID)
		seen[d.ID] = true
		if i > 0 {
			prev := got[i-1]
			assert.True(t, prev.CreatedAt.After(d.CreatedAt) ||
				(prev.CreatedAt.Equal(d.CreatedAt) && prev.ID.String() > d.ID.String()),
				"必须严格按 (created_at DESC, id DESC)，第 %d 条与第 %d 条顺序不对", i-1, i)
		}
	}
}

// 恰好整除时不该多给一个空页——那会让用户点一次「加载更多」什么都没发生。
func TestIntegration_ListByKnowledgeBase_ExactMultipleHasNoEmptyPage(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgDocRepo()
	kbID := seedDocsForPagination(t, pool, 4)

	page, hasMore, err := repo.ListByKnowledgeBase(ctx, pool, kbID, nil, 4)

	require.NoError(t, err)
	assert.Len(t, page, 4)
	assert.False(t, hasMore, "刚好取满时不该说还有下一页")
}

// 【这条钉的是 issue #99：分块数的算法不能把 keyset 分页的索引优势吃掉】
//
// 0008 建的 documents_kb_created_id_idx 只有一个用途——让
// `WHERE knowledge_base_id = $1 ORDER BY created_at DESC, id DESC LIMIT n`
// 沿索引取够 n 行就停。把分块数写成 LEFT JOIN + GROUP BY 的聚合会让它退化：
// planner 必须先把该知识库的**每一份**文档聚合完、再排序，才轮得到 LIMIT，
// 于是每翻一页的代价是 O(库内文档数 × 分块查找)。
//
// 【为什么要造这么多行】几十行的表上，两种写法都被 planner 判成"反正都一样
// 便宜"，断言不出任何东西——这条缺陷的全部特征就是"小库上看不出来"。
// 3000 行 + ANALYZE 才让"扫全库"和"沿索引取 21 行"的代价差变得显著。
func TestIntegration_ListByKnowledgeBase_StopsAtTheIndexWhenTheBaseIsLarge(t *testing.T) {
	pool := requireTestDB(t)

	// 0 份文档起步：这里只借它"造一个知识库 + 跑完自动清理"那部分。
	kbID := seedDocsForPagination(t, pool, 0)
	seedManyDocuments(t, pool, kbID, 3000)

	plan := explainPlan(t, pool, explainWithLiterals(t, listDocumentsSQL, kbID, 21))

	assert.Contains(t, plan, "documents_kb_created_id_idx",
		"列表查询必须走分页索引——不走就说明 LIMIT 又失去了提前终止的能力")
	assert.NotContains(t, plan, "Sort",
		"计划里不该出现 Sort：它意味着先处理完整个知识库再取前 21 行")
}

// 【这条钉的是关联子查询算出来的数对不对】上面那条测的是形状，形状对了
// 数还得对："没有分块的文档是 0"和"有 N 个分块就是 N"这两种情况都由它管。
func TestIntegration_ListByKnowledgeBase_CountsChunksPerDocument(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgDocRepo()

	kbID := seedDocsForPagination(t, pool, 2)
	var withChunks, withNone uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM documents WHERE knowledge_base_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		kbID).Scan(&withChunks))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM documents WHERE knowledge_base_id = $1 ORDER BY created_at ASC, id ASC LIMIT 1`,
		kbID).Scan(&withNone))

	// 三个分块，其中一条 embedding 是空的——列表上的"分块数"数的是行数，
	// 不是"已经向量化的行数"，两种都算进去。
	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx,
			`INSERT INTO document_chunks (id, document_id, content) VALUES ($1, $2, $3)`,
			uuid.New(), withChunks, "chunk")
		require.NoError(t, err)
	}
	// 另一份文档故意一条分块都不给，验的是"没有分块 = 0"而不是 NULL。

	page, _, err := repo.ListByKnowledgeBase(ctx, pool, kbID, nil, 10)
	require.NoError(t, err)
	require.Len(t, page, 2)

	counts := map[uuid.UUID]int64{}
	for _, d := range page {
		counts[d.ID] = d.ChunkCount
	}
	assert.Equal(t, int64(3), counts[withChunks])
	assert.Equal(t, int64(0), counts[withNone])
}

// seedManyDocuments 用一条 INSERT ... SELECT 造 n 份文档。
//
// 【为什么不用 seedDocsForPagination 的循环】几千次单行 INSERT 会让这条
// 测试自己的耗时盖过它要证明的东西；create_at 逐秒递减，顺序仍与
// (created_at DESC, id DESC) 一致。
func seedManyDocuments(t *testing.T, pool *pgxpool.Pool, kbID uuid.UUID, n int) {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx,
		`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status, created_at, updated_at)
		 SELECT gen_random_uuid(), $1, 'doc-' || i || '.md', 'plan-probe-' || gen_random_uuid(), 'ready',
		        now() - (i || ' seconds')::interval, now()
		 FROM generate_series(1, $2) AS i`, kbID, n)
	require.NoError(t, err)

	// 每个文档配一条分块：没有分块的话"先聚合再排序"那条路看起来也没多贵，
	// 而真实的知识库是有内容的。
	_, err = pool.Exec(ctx,
		`INSERT INTO document_chunks (id, document_id, content)
		 SELECT gen_random_uuid(), id, 'chunk' FROM documents WHERE knowledge_base_id = $1`, kbID)
	require.NoError(t, err)

	// 统计信息必须是最新的：planner 靠它决定"沿索引取 21 行"划不划算，
	// 没 ANALYZE 时行数估计停留在插入之前的空表上。
	for _, table := range []string{"documents", "document_chunks"} {
		_, err = pool.Exec(ctx, `ANALYZE `+table)
		require.NoError(t, err)
	}
}

// explainWithLiterals 把列表 SQL 里的占位符换成具体值。
//
// 【为什么不能直接 EXPLAIN 带参数的语句】参数化之后 planner 拿不到
// `knowledge_base_id = ?` 和 `LIMIT ?` 的值（缓存过的语句可能走 generic
// plan），估出来的形状和生产实际执行的那次不是一回事——生产上第一条
// 执行走的是带具体值的 custom plan。这条测试要断言的是**生产那条 SQL**
// 的计划，所以替换是在 listDocumentsSQL 这个常量上做的，契约改了、
// 占位符换了名字，下面的断言会立刻失败（换完还有 $ 残留）。
func explainWithLiterals(t *testing.T, sql string, kbID uuid.UUID, limit int) string {
	t.Helper()

	sql = strings.Replace(sql, "$1", "'"+kbID.String()+"'", 1)
	sql = strings.Replace(sql, "$2", strconv.Itoa(limit), 1)
	require.NotContains(t, sql, "$", "占位符的写法变了，这条测试替换的就不再是生产那条 SQL")
	return sql
}

// explainPlan 跑 EXPLAIN (COSTS OFF) 并按行拼回计划文本。
func explainPlan(t *testing.T, pool *pgxpool.Pool, sql string) string {
	t.Helper()

	rows, err := pool.Query(context.Background(), "EXPLAIN (COSTS OFF) "+sql)
	require.NoError(t, err)
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

// 别的知识库的文档不能混进来。
func TestIntegration_ListByKnowledgeBase_IsScopedToItsKnowledgeBase(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgDocRepo()
	kbA := seedDocsForPagination(t, pool, 3)
	// 另一个知识库也要真的有内容，否则"没混进来"这件事证明不了什么。
	_ = seedDocsForPagination(t, pool, 2)

	pageA, _, err := repo.ListByKnowledgeBase(ctx, pool, kbA, nil, 50)
	require.NoError(t, err)

	require.Len(t, pageA, 3)
	for _, d := range pageA {
		assert.Equal(t, kbA, d.KnowledgeBaseID, "别的知识库的文档不该出现")
	}
}
