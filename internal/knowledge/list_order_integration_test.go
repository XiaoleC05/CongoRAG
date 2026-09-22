// 真库验证 PgRepo.List 的排序语义（issue #103）。
//
// 【为什么这条必须连真库】契约（contracts/openapi.yaml 的知识库列表）
// 承诺"按创建时间倒序"，而这个承诺完整地落在 postgres.go 里那一句
// `ORDER BY created_at DESC` 上。单元测试用的是 fakeRepo，它的 List
// 直接返回 `Create` 按插入顺序 append 的切片——**恰好是相反的升序**，
// 所以"新知识库排在最前"这件事在单测里从来没有被验证过。
//
// 【它挡的是什么】把 `ORDER BY created_at DESC` 整句删掉、或者写成 ASC，
// 全套单测仍然全绿；用户看到的知识库顺序却变成 PostgreSQL 的任意顺序
// （小表上通常恰好是物理插入顺序，于是"看起来是对的"）。
//
// 【门控】和同包的 postgres_integration_test.go 共用 requireTestDB
// （internal/testdb：设了 CONGORAG_TEST_DB_URL 用它，没设就自己起容器）。
package knowledge

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 【时间戳为什么定在 2035 年】这个库上可能还有别的测试、别的开发者留下的
// 知识库。用一个远离"现在"的固定时刻造探针行，既不会和它们的 created_at
// 撞在一起（撞了的话"哪条更新"这件事就要靠 tie-break，而这条测试不该
// 依赖它），也让失败信息里的时间一眼能认出是这条测试造的数据。
func TestIntegration_List_ReturnsNewestFirst(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgRepo()

	base := time.Date(2035, 4, 1, 9, 0, 0, 0, time.UTC)
	older := &KB{ID: uuid.New(), Name: "probe-list-order-older", CreatedAt: base, UpdatedAt: base}
	newer := &KB{ID: uuid.New(), Name: "probe-list-order-newer", CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour)}

	// 【为什么先插 older 再插 newer】插入顺序（= 物理顺序，小表上就是顺序扫
	// 吐出来的顺序）因此正好与期望顺序**相反**。要是 List 退化成了"没有
	// ORDER BY 的顺序扫描"，返回的会是 older、newer，下面两条断言都会红；
	// 反过来先插 newer 的话，物理顺序恰好等于期望顺序，缺 ORDER BY 也能全绿。
	for _, kb := range []*KB{older, newer} {
		require.NoError(t, repo.Insert(ctx, pool, kb))
		kbID := kb.ID
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, kbID)
		})
	}

	got, err := repo.List(ctx, pool)
	require.NoError(t, err)

	// ── (1) 全局不变式：整张列表必须按 created_at 非递增 ──
	// 这一条不依赖任何"只有我的探针行"的假设，所以在共享库上也成立；
	// 它挡的是"ORDER BY 只对某一段生效"这类写错（比如排序被写进了子查询）。
	for i := 1; i < len(got); i++ {
		require.False(t, got[i].CreatedAt.After(got[i-1].CreatedAt),
			"第 %d 条（%s @ %s）比第 %d 条（%s @ %s）更新——列表必须按 created_at 倒序",
			i, got[i].Name, got[i].CreatedAt, i-1, got[i-1].Name, got[i-1].CreatedAt)
	}

	// ── (2) 探针两条的相对位置：created_at 更晚的必须排在前面 ──
	idxNewer, idxOlder := -1, -1
	for i, kb := range got {
		switch kb.ID {
		case newer.ID:
			idxNewer = i
		case older.ID:
			idxOlder = i
		}
	}
	require.GreaterOrEqual(t, idxNewer, 0, "刚插入的新知识库必须出现在列表里")
	require.GreaterOrEqual(t, idxOlder, 0, "刚插入的旧知识库必须出现在列表里")
	assert.Less(t, idxNewer, idxOlder,
		"created_at 更晚的知识库必须排在前面（真实现是 ORDER BY created_at DESC）")
}
