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
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("CONGORAG_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("CONGORAG_TEST_DB_URL 未设置,跳过需要真实 Postgres 的集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(context.Background()), "连不上测试数据库")
	return pool
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
