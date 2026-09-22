// 真库验证 PgMemoryRepo.ListNeedingEmbedding 的"谁需要重算向量"判据
// （issue #103，issue #39 的修复链）。
//
// 【为什么这条必须连真库】换 embedding 模型之后，记忆的重建完全依赖这一句
// `WHERE embedding IS NULL OR embedding_model IS DISTINCT FROM $1`：
//
//	· 写成 `<>` 的话，"向量在、模型标记为 NULL"的行比较结果是 NULL 而不是
//	  true，会被漏掉——它们永远补不上向量，而 SearchByRelevance 只认"向量
//	  非空且模型是当前模型"，于是这些记忆在检索里静默消失（注意：两列都为
//	  NULL 的行不受影响，第一个分支就接住了它，所以那种行区分不出两种写法）；
//	· 判据写反（比如 `=`）的话，返回的恰好是**已经算过**的那批。
//
// 两种错法在单测里都看不见：usecase_test.go 里的 fakeMemoryRepo 收了
// activeModel 参数却一个字都没用，直接把预先摆好的切片原样吐回来
// （见该文件的 ListNeedingEmbedding）。判据真正的形状只有真库能证明。
//
// 【门控】和同包的 postgres_integration_test.go 共用 requireTestDB。
package conversation

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoriesEmbeddingDim 读 memories.embedding 此刻真实的维度；列上没有维度
// 约束（0001 迁移刚建完时的 halfvec）时返回 0。
//
// 【为什么必须动态读，而不是写死一个维度】integration 里有一条会真的把
// memories.embedding ALTER 成 halfvec(N)（internal/llm 的 Bootstrap 集成
// 测试）。往 halfvec(768) 的列里插一个 3 维向量不是"精度损失"，是直接
// 报 22000（expected 768 dimensions, not 3）——写死维度的话，这条测试
// 的结果会取决于"跑它之前有没有跑过那条 Bootstrap 测试"，那是最难查的
// 那种红。
func memoriesEmbeddingDim(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var formatted string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT format_type(a.atttypid, a.atttypmod)
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'memories' AND a.attname = 'embedding'
		   AND a.attnum > 0 AND NOT a.attisdropped`).Scan(&formatted),
		"读 memories.embedding 的列类型失败")

	// format_type 给出 "halfvec" 或 "halfvec(1024)"，维度在括号里。
	m := regexp.MustCompile(`\((\d+)\)`).FindStringSubmatch(formatted)
	if m == nil {
		return 0 // 列上没有维度约束，任意维度都能插
	}
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err, "解析 %q 里的维度失败", formatted)
	return n
}

// memProbeVectorLiteral 造一个符合该列维度的 halfvec 文本字面量。
//
// 【为什么用文本字面量而不是 pgvector-go 的编解码器】pgxvec.RegisterTypes
// 只在 api / worker 两个二进制的启动路径上注册（apps/*/internal/app），
// 测试进程里没有——和 internal/llm 的集成测试同样的取舍。
func memProbeVectorLiteral(dim int) string {
	if dim <= 0 {
		dim = 3
	}
	parts := make([]string, dim)
	for i := range parts {
		parts[i] = "0.5"
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// 【这条测试造的五种形状，前四种按"换模型之后真实库里会长什么样"来排】
//
//  1. embedding IS NULL、embedding_model = 旧模型名 —— 切换事务里
//     clearStaleEmbeddings 清掉向量的结果，是最常见的一种；
//  2. 两列都是 NULL —— 从来没算过向量的记忆（比如引导还没做完时写的）；
//  3. 向量还在、模型标记是旧模型 —— 进程在"清空"和"重算"之间被打断时留下的；
//  4. 向量与模型标记都是当前模型 —— **已经算好的那批，必须不返回**；
//  5. 向量非空、embedding_model 为 NULL —— 见下面那段说明。
//
// 【为什么必须造第 5 行】判据 `embedding IS NULL OR embedding_model IS
// DISTINCT FROM $1` 里，只有第 5 行能区分 `IS DISTINCT FROM` 和 `<>`：
// 前面几行里，1、2 被第一个分支必然接住（哪怕写成 `<>`），3 两列都不为
// NULL 时两种写法的结果一样，4 两种写法都排除。只有"向量在、模型标记是
// NULL"时 `NULL <> $1` 求值为 NULL（= 不匹配）而 `IS DISTINCT FROM`
// 为真——也就是说，少了这一行，把判据换成 `<>` 这条测试照样全绿。
//
// 【第 5 行现在写不出来吗】是：Insert 两列一起写、UpdateEmbedding 也是,
// 当前没有任何写入路径会留下"有向量但没模型标记"的行。它是判据的防御性
// 那一半，这里锁的是**判据的形状**而不是某个已知的写入路径——库是本地
// 单机、可以被手工改的，判据不该因为"现在还到不了"就退化成 `<>`。
func TestIntegration_ListNeedingEmbedding_MatchesActiveModelOnly(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgMemoryRepo()

	// 这两个名字是纯文本列上的探针值，不需要库里真的存在同名模型：
	// ListNeedingEmbedding 只做字符串比较，不 JOIN llm_models。
	const activeModel = "probe-embedding-model-active"
	const staleModel = "probe-embedding-model-stale"

	vec := memProbeVectorLiteral(memoriesEmbeddingDim(t, pool))

	// created_at 拉开一分钟一条：既是真实形状，也让下面的顺序断言有确定的答案。
	base := time.Date(2035, 5, 1, 8, 0, 0, 0, time.UTC)

	// vec 传 nil 表示这一行的 embedding 是 NULL。
	mk := func(vec, model *string, at time.Time) uuid.UUID {
		id := uuid.New()
		_, err := pool.Exec(ctx,
			`INSERT INTO memories (id, scope, content, embedding, embedding_model, created_at)
			 VALUES ($1, 'issue103_probe', $2, $3::halfvec, $4::text, $5::timestamptz)`,
			id, "probe memory "+id.String(), vec, model, at)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM memories WHERE id = $1`, id)
		})
		return id
	}
	str := func(s string) *string { return &s }

	// 【为什么按时间倒着插】插入顺序（= 物理顺序）因此正好与 ORDER BY
	// created_at 升序**相反**——把 ORDER BY 整句删掉时，下面那几条顺序断言
	// 才会真的红，而不是被物理顺序碰巧蒙对。
	current := mk(&vec, str(activeModel), base.Add(4*time.Minute)) // 4. 当前模型，不该返回
	noModel := mk(&vec, nil, base.Add(3*time.Minute))              // 5. 有向量、没模型标记
	staleVec := mk(&vec, str(staleModel), base.Add(2*time.Minute)) // 3. 旧模型的向量还留着
	never := mk(nil, nil, base.Add(time.Minute))                   // 2. 从来没算过
	cleared := mk(nil, str(staleModel), base)                      // 1. 切换后被清空向量

	// limit 给得比库里任何可能的前存量都大：这条测试断言的是**集合**，
	// 不想因为"别的记忆排在前面把探针行挤出窗口"而假红。
	got, err := repo.ListNeedingEmbedding(ctx, pool, activeModel, 1000)
	require.NoError(t, err)

	pos := map[uuid.UUID]int{}
	for i, m := range got {
		pos[m.ID] = i
	}

	// ── 该返回的四行：一行都不能少 ──
	require.Contains(t, pos, cleared, "向量被清空、模型标记是旧模型的记忆必须待重算")
	require.Contains(t, pos, never, "从来没算过向量的记忆必须待重算")
	require.Contains(t, pos, staleVec, "向量是旧模型算的、还没被清空的记忆必须待重算")
	// 这一条就是 `<>` 与 `IS DISTINCT FROM` 的分水岭：写成 `<>` 时
	// `NULL <> 'probe-embedding-model-active'` 求值为 NULL，这一行被漏掉。
	require.Contains(t, pos, noModel,
		"向量非空、模型标记为 NULL 的记忆必须待重算（写成 `<>` 的话它会被漏掉）")

	// ── 不该返回的那一行 ──
	// 判据写反（比如 `=` 或整句取反）时，返回的正好是它。
	assert.NotContains(t, pos, current, "向量已经是当前模型的记忆不该重算")

	// ── 顺序：按创建时间升序 ──
	// 只比较这几条探针行之间的相对位置——它们之间有别的行也不影响这个不变式，
	// 而"整表非递减"需要知道库里其它行的 created_at，那条留给别的测试。
	assert.Less(t, pos[cleared], pos[never],
		"created_at 更早的记忆必须排在前面（ORDER BY created_at 升序）")
	assert.Less(t, pos[never], pos[staleVec],
		"created_at 更早的记忆必须排在前面（ORDER BY created_at 升序）")
	assert.Less(t, pos[staleVec], pos[noModel],
		"created_at 更早的记忆必须排在前面（ORDER BY created_at 升序）")

	// 返回行的字段也要对：SQL 只 SELECT 了三列，漏一列或者顺序写反的话
	// 上面那些 ID 断言会一起错位，这里把它定死。
	gotCleared := got[pos[cleared]]
	assert.Equal(t, "issue103_probe", gotCleared.Scope)
	assert.Equal(t, "probe memory "+cleared.String(), gotCleared.Content)
}

// 上面那条测的是"判据"。这条测 limit 真的被传给 SQL 了——limit 写成常量、
// 或者 LIMIT $2 的位置写错（比如把 activeModel 传进了 limit），
// 在单测里同样看不见。
func TestIntegration_ListNeedingEmbedding_RespectsLimit(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgMemoryRepo()

	// 用一个绝对不会与其它测试重名、也绝对不会出现在任何真实模型名里的
	// 模型名：这样"库里此刻有多少行待重算"完全由这条测试自己决定。
	const activeModel = "probe-limit-model-does-not-exist"

	base := time.Date(2035, 5, 2, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		id := uuid.New()
		_, err := pool.Exec(ctx,
			`INSERT INTO memories (id, scope, content, embedding_model, created_at)
			 VALUES ($1, 'issue103_probe', $2, NULL, $3::timestamptz)`,
			id, fmt.Sprintf("limit probe %d", i), base.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM memories WHERE id = $1`, id)
		})
	}

	got, err := repo.ListNeedingEmbedding(ctx, pool, activeModel, 1)
	require.NoError(t, err)

	// 库里此刻至少有这 3 行（可能还有别的），limit=1 就必须恰好给一行：
	// LIMIT 被漏掉时会给出三行以上，被写成 0 时一行都没有，两种都在这里红。
	assert.Len(t, got, 1, "limit=1 时必须恰好返回一行")
}
