// 真库验证 PgUsageRepo.UsageSummary 的聚合语义（issue #103）。
//
// 【为什么这条必须连真库】用量页上的每一个数字都由 usage_postgres.go 那
// 一句 SQL 算出来：COUNT(*)、SUM、JOIN llm_models 取模型名、GROUP BY
// 两个 id 加 kind、按总量倒序、以及 `>= since AND < until` 的左闭右开。
//
// 而现有的单测验证的是**Go 的加法**：llm 侧的 fake 返回 nil，api 侧的
// fake 把预先造好的成品行吐出来，再由 server.go 在 Go 里二次求和。
// 于是把两个 model_id 用反、漏掉 COALESCE、把区间写成闭区间、
// 甚至删掉 GROUP BY，测试全都是绿的。
//
// 【门控】和同包的 postgres_integration_test.go 共用 requireTestDB。
//
// 【这一条覆盖不到的】`COALESCE(SUM(...), 0)` —— 两个 token 列都是
// NOT NULL，分组求和在"组里至少有一行"时不可能为 NULL，所以那个 COALESCE
// 在当前 schema 下是不可达的防御。这里不假装测了它。
package llm

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 【区间为什么定在 2035 年】别的测试（和真实使用）写入 token_usage 时
// created_at 用的是 time.Now()。把探针区间放在一个远离"现在"的固定时刻，
// 这条测试的聚合结果就只由它自己插入的那几行决定——否则"总有别的行混进来"
// 会让每一条断言都要先做过滤，而过滤写多了就测不出 GROUP BY 本身了。
func TestIntegration_UsageSummary_GroupsSumsOrdersAndBounds(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgUsageRepo()

	providerID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO llm_providers (id, base_url, api_key_encrypted) VALUES ($1, 'https://example.invalid', 'x')`,
		providerID)
	require.NoError(t, err)
	// provider 删掉会级联删掉两个 model 和全部 token_usage 行。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM llm_providers WHERE id = $1`, providerID)
	})

	chatModelUUID, embedModelUUID, outsideModelUUID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO llm_models (id, provider_id, model_id, kind) VALUES
		   ($1, $4, 'usage-probe-chat',        'chat'),
		   ($2, $4, 'usage-probe-embed',       'embedding'),
		   ($3, $4, 'usage-probe-outside',     'chat')`,
		chatModelUUID, embedModelUUID, outsideModelUUID, providerID)
	require.NoError(t, err)

	base := time.Date(2035, 3, 1, 12, 0, 0, 0, time.UTC)
	until := base.Add(time.Hour)

	seed := func(model uuid.UUID, kind Kind, at time.Time, prompt, completion int) {
		t.Helper()
		require.NoError(t, repo.InsertUsage(ctx, pool, &Usage{
			ProviderID: providerID, ModelID: model, Kind: kind,
			PromptTokens: prompt, CompletionTokens: completion, CreatedAt: at,
		}))
	}

	// ── chat 模型：两条落在区间内，两条踩在边界上 ──
	seed(chatModelUUID, KindChat, base, 20, 2)                            // 恰好是下界：必须算进来
	seed(chatModelUUID, KindChat, base.Add(30*time.Minute), 10, 1)        // 区间内
	seed(chatModelUUID, KindChat, base.Add(-time.Nanosecond), 5000, 5000) // 下界之前一纳秒：必须排除
	seed(chatModelUUID, KindChat, until, 5000, 5000)                      // 恰好是上界：左闭右开，必须排除
	// ── embedding 模型：两条，其中一条 token 为 0 ──
	// 0 的那行是为了把 COUNT(*) 和 SUM 分开：Calls 数的是行数，不是 token。
	seed(embedModelUUID, KindEmbedding, base.Add(10*time.Minute), 3, 1)
	seed(embedModelUUID, KindEmbedding, base.Add(20*time.Minute), 0, 0)
	// ── 第三个模型：只在区间外有调用 ──
	// 它锁的是"时间条件作用在聚合之前"这条语义：区间内一次调用都没有的模型
	// 整行不出现。把 WHERE 的时间条件挪进 JOIN 的话它会以 0 出现。
	seed(outsideModelUUID, KindChat, base.Add(-time.Hour), 7, 7)

	// ── (1) 左闭右开区间 ──
	since, untilPtr := base, until
	rows, err := repo.UsageSummary(ctx, pool, &since, &untilPtr)
	require.NoError(t, err)

	// 区间里只可能有这两个模型的行（2035 年的时间窗是这条测试独占的），
	// 所以这里能断言长度——少一个模型说明边界没切干净，多一个说明 GROUP BY 漏了。
	require.Len(t, rows, 2, "探针区间里恰好两个模型有调用")

	chatRow := usageRowForModel(rows, chatModelUUID)
	require.NotNil(t, chatRow, "chat 模型在区间内有调用，必须出现在结果里")
	assert.Equal(t, providerID, chatRow.ProviderID)
	assert.Equal(t, "usage-probe-chat", chatRow.ModelName, "模型名必须取 llm_models.model_id（用户敲的那个名字），不是 uuid")
	assert.Equal(t, KindChat, chatRow.Kind)
	assert.Equal(t, int64(2), chatRow.Calls, "下界那一行要算进来，上界和它之前的那两行要排掉")
	assert.Equal(t, int64(30), chatRow.PromptTokens)
	assert.Equal(t, int64(3), chatRow.CompletionTokens)

	embedRow := usageRowForModel(rows, embedModelUUID)
	require.NotNil(t, embedRow)
	assert.Equal(t, "usage-probe-embed", embedRow.ModelName)
	assert.Equal(t, KindEmbedding, embedRow.Kind)
	assert.Equal(t, int64(2), embedRow.Calls, "token 为 0 的那行也是一次调用，COUNT(*) 要数它")
	assert.Equal(t, int64(3), embedRow.PromptTokens)
	assert.Equal(t, int64(1), embedRow.CompletionTokens)

	// ── (2) 排序：按 prompt+completion 的总和倒序 ──
	// chat 是 33，embedding 是 4。
	assert.Equal(t, chatModelUUID, rows[0].ModelID, "总量大的模型排在前面")
	assert.Equal(t, embedModelUUID, rows[1].ModelID)

	// ── (3) 区间内没有调用的模型整行不出现 ──
	assert.Nil(t, usageRowForModel(rows, outsideModelUUID),
		"只在区间外有调用的模型不该出现（时间条件必须在聚合之前生效）")

	// ── (4) 两个边界都为 NULL 表示不限 ──
	// 这条走的是 `$1::timestamptz IS NULL OR ...` 的另一支：边界行和边界外
	// 的行全都算进来（其它测试/真实使用的行也在，所以只按 model 取自己那两行）。
	all, err := repo.UsageSummary(ctx, pool, nil, nil)
	require.NoError(t, err)
	chatAll := usageRowForModel(all, chatModelUUID)
	require.NotNil(t, chatAll)
	assert.Equal(t, int64(4), chatAll.Calls, "不限区间时四条都要算")
	assert.Equal(t, int64(10030), chatAll.PromptTokens)
	assert.Equal(t, int64(10003), chatAll.CompletionTokens)
	assert.NotNil(t, usageRowForModel(all, outsideModelUUID), "不限区间时区间外的模型也要出现")

	// ── (5) 只给下界（上界为 NULL）──
	// 裁掉下界之前那一行，其余保留——这是"上界为 NULL 表示不设上限"这一支。
	sinceOnly, err := repo.UsageSummary(ctx, pool, &since, nil)
	require.NoError(t, err)
	chatSinceOnly := usageRowForModel(sinceOnly, chatModelUUID)
	require.NotNil(t, chatSinceOnly)
	assert.Equal(t, int64(3), chatSinceOnly.Calls, "下界之前那一行要被排掉，恰好等于下界的那行要留下")
	assert.Equal(t, int64(5030), chatSinceOnly.PromptTokens)
	// 那个模型唯一的调用在下界之前，所以这次也不该出现——它和 (3) 一起
	// 说明"上界为 NULL"放开的是上界，下界照旧生效。
	assert.Nil(t, usageRowForModel(sinceOnly, outsideModelUUID),
		"只给下界时，下界之前的行仍然要被排掉")
}

// usageRowForModel 从结果里挑出某个模型的那一行；不在结果里时返回 nil。
//
// 【为什么需要它】(4)(5) 两次调用不限区间，结果里会混进别的测试、
// 甚至真实使用留下的行，所以那两条只能按模型 id 定位自己的行，
// 不能像 (1) 那样断言整个切片的长度。
func usageRowForModel(rows []*UsageByModel, modelID uuid.UUID) *UsageByModel {
	for _, r := range rows {
		if r.ModelID == modelID {
			return r
		}
	}
	return nil
}
