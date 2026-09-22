// 连真实 PostgreSQL 的集成测试。
//
// 【为什么这个文件必须存在】RecentMessages 的取数行为全部写在一句 SQL 里：
// 内层 DESC 取最近 N 条、外层重排成 ASC、排他上界挡掉本轮自己刚写入的
// 消息。单元测试用的是 fakeRepo——假实现把这段逻辑照抄了一遍，SQL 本身
// 却从来没有在真实数据库上执行过。抄错了、写成"最旧的 N 条"、ORDER BY
// 没生效，单元测试全绿也说明不了任何事。这里让那条 SQL 真的跑一次。
//
// 【门控变量】和 internal/llm/postgres_integration_test.go 同一套约定：
// 设了 CONGORAG_TEST_DB_URL 才跑，没设就 t.Skip——本地 `go test ./...`
// 不连库，CI 的 integration job 跑完两套迁移之后会连真实的库跑这一条。
package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/testdb"
)

// requireTestDB 跳过测试,除非 CONGORAG_TEST_DB_URL 设置了。
// requireTestDB 返回一个 schema 已就绪的测试库（issue #70）。
//
// 具体从哪来由 internal/testdb 决定：设了 CONGORAG_TEST_DB_URL 就用它
// （并校验 schema 在不在），没设就自己起一个容器。这个函数只剩一行委托——
// 在此之前四个包各抄了一份门控逻辑，而"测试库该长什么样"因此有四个副本。
func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testdb.Require(t)
}

// 一次两轮对话在库里的真实形状，逐条断言 RecentMessages 的返回。
func TestPgRepo_RecentMessages_OrderAndBounds(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgRepo()

	now := time.Now()
	conv := &Conversation{ID: uuid.New(), Title: "集成测试会话", CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.CreateConversation(ctx, pool, conv))
	t.Cleanup(func() {
		// messages 对 conversations 是 ON DELETE CASCADE，删掉会话就干净了。
		_, err := pool.Exec(context.Background(), `DELETE FROM conversations WHERE id = $1`, conv.ID)
		require.NoError(t, err)
	})

	// 第一轮已完成，第二轮是本轮：seq3 是刚写入的用户消息，seq4 是它的
	// assistant 占位行（status=streaming、content 为空）。
	seeded := []*Message{
		{Role: domain.RoleUser, Content: "第一问", Status: MsgCompleted, SequenceNo: 1},
		{Role: domain.RoleAssistant, Content: "第一答", Status: MsgCompleted, SequenceNo: 2},
		{Role: domain.RoleUser, Content: "第二问", Status: MsgCompleted, SequenceNo: 3},
		{Role: domain.RoleAssistant, Content: "", Status: MsgStreaming, SequenceNo: 4},
	}
	for _, m := range seeded {
		m.ID, m.ConversationID, m.CreatedAt = uuid.New(), conv.ID, now
		require.NoError(t, repo.AppendMessage(ctx, pool, m))
	}

	t.Run("正序返回_上界排他", func(t *testing.T) {
		msgs, err := repo.RecentMessages(ctx, pool, conv.ID, 0, 3, 50)

		require.NoError(t, err)
		require.Len(t, msgs, 2, "seq3 是本轮用户消息、seq4 是它的占位行，都该被上界挡住")
		assert.Equal(t, []int64{1, 2}, []int64{msgs[0].SequenceNo, msgs[1].SequenceNo},
			"必须是旧 → 新，不是最新在前")
		assert.Equal(t, domain.RoleUser, msgs[0].Role)
		assert.Equal(t, "第一问", msgs[0].Content)
		assert.Equal(t, domain.RoleAssistant, msgs[1].Role, "角色是数据库里的事实，必须原样带回来")
	})

	t.Run("上界为0表示无上界", func(t *testing.T) {
		msgs, err := repo.RecentMessages(ctx, pool, conv.ID, 0, 0, 50)

		require.NoError(t, err)
		require.Len(t, msgs, 4, "传 0 时不做上界过滤，本轮的占位行也在里面")
		assert.Equal(t, int64(4), msgs[3].SequenceNo)
	})

	t.Run("limit取最近N条而不是最旧N条", func(t *testing.T) {
		msgs, err := repo.RecentMessages(ctx, pool, conv.ID, 0, 3, 1)

		require.NoError(t, err)
		require.Len(t, msgs, 1)
		assert.Equal(t, int64(2), msgs[0].SequenceNo,
			"limit=1 要的是上界之前最新的一条（seq2），不是最旧的一条（seq1）")
		assert.Equal(t, "第一答", msgs[0].Content)
	})

	t.Run("已经摘要覆盖过的部分不再返回", func(t *testing.T) {
		msgs, err := repo.RecentMessages(ctx, pool, conv.ID, 1, 3, 50)

		require.NoError(t, err)
		require.Len(t, msgs, 1)
		assert.Equal(t, "第一答", msgs[0].Content)
	})
}

// issue #118 / #98 的新 SQL 在真实库上跑一次。
//
// 【为什么这几条必须连真库】候选集的三条谓词（status='completed'、
// 水位线之差、门槛）和剪枝的 created_at 比较全部写在一句 SQL 里，而
// 单元测试用的 fakeRepo 把同样的逻辑用 Go 抄了一遍——SQL 写错了
// （JOIN 少一句、COALESCE 放错位置、谓词取反）在一堆绿色的单测里
// 看不出来。这个文件存在的理由就是这句话，见文件头。
//
// 【断言只看自己造的会话】候选集查询扫的是整张 conversations 表，同一次
// 测试运行里别的测试也建了会话。所以这里按 id 判定"在不在结果里"，
// 不断言结果集的规模。
func TestPgRepo_MaintenanceCandidates_AndEventPrune(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	repo := NewPgRepo()
	now := time.Now()

	mkConv := func(title string) *Conversation {
		c := &Conversation{ID: uuid.New(), Title: title, CreatedAt: now, UpdatedAt: now}
		require.NoError(t, repo.CreateConversation(ctx, pool, c))
		t.Cleanup(func() {
			_, err := pool.Exec(context.Background(), `DELETE FROM conversations WHERE id = $1`, c.ID)
			require.NoError(t, err)
		})
		return c
	}

	// 有摘要的会话：3 条已定稿 + 1 条还在生成中的占位行（seq4）。
	// 占位行的正文还没定稿，不该把水位线顶上去——这条谓词就是
	// 0003 迁移注释里那个「占位行把门控顶过去」的洞。
	withSummary := mkConv("候选集：有摘要")
	for _, m := range []*Message{
		{Role: domain.RoleUser, Content: "第一问", Status: MsgCompleted, SequenceNo: 1},
		{Role: domain.RoleAssistant, Content: "第一答", Status: MsgCompleted, SequenceNo: 2},
		{Role: domain.RoleUser, Content: "第二问", Status: MsgCompleted, SequenceNo: 3},
		{Role: domain.RoleAssistant, Content: "", Status: MsgStreaming, SequenceNo: 4},
	} {
		m.ID, m.ConversationID, m.CreatedAt = uuid.New(), withSummary.ID, now
		require.NoError(t, repo.AppendMessage(ctx, pool, m))
	}
	require.NoError(t, repo.UpsertSummary(ctx, pool, &Summary{
		ConversationID: withSummary.ID, Summary: "旧摘要正文",
		CoveredUntilSequenceNo: 2, UpdatedAt: now,
	}))

	// 没摘要的会话：2 条已定稿。
	noSummary := mkConv("候选集：没摘要")
	for _, seq := range []int64{1, 2} {
		m := &Message{ID: uuid.New(), ConversationID: noSummary.ID, Role: domain.RoleUser,
			Content: "msg", Status: MsgCompleted, SequenceNo: seq, CreatedAt: now}
		require.NoError(t, repo.AppendMessage(ctx, pool, m))
	}

	// 只有占位行的会话：一条已定稿的都没有，任何门槛都不该选中它。
	onlyStreaming := mkConv("候选集：只有占位行")
	require.NoError(t, repo.AppendMessage(ctx, pool, &Message{
		ID: uuid.New(), ConversationID: onlyStreaming.ID, Role: domain.RoleAssistant,
		Content: "", Status: MsgStreaming, SequenceNo: 1, CreatedAt: now,
	}))

	find := func(cands []*SummaryCandidate, id uuid.UUID) *SummaryCandidate {
		for _, c := range cands {
			if c.ConversationID == id {
				return c
			}
		}
		return nil
	}

	t.Run("门槛之差取自真实列", func(t *testing.T) {
		cands, err := repo.SummaryMaintenanceCandidates(ctx, pool, 2)
		require.NoError(t, err)

		// 3 - 2 = 1 < 2：差一条，不该入选。
		assert.Nil(t, find(cands, withSummary.ID), "水位线之差没到门槛就不该是候选")

		cands, err = repo.SummaryMaintenanceCandidates(ctx, pool, 1)
		require.NoError(t, err)
		got := find(cands, withSummary.ID)
		require.NotNil(t, got, "3 - 2 = 1 ≥ 门槛，必须入选")
		assert.Equal(t, int64(3), got.LatestSequenceNo,
			"最新序号只能数已定稿的行——seq4 是生成中的占位行")
		assert.Equal(t, int64(2), got.CoveredUntil)
		assert.Equal(t, "旧摘要正文", got.PriorSummary, "摘要正文必须随候选一起带回来")
	})

	t.Run("没有摘要的会话按0起算", func(t *testing.T) {
		cands, err := repo.SummaryMaintenanceCandidates(ctx, pool, 2)
		require.NoError(t, err)

		got := find(cands, noSummary.ID)
		require.NotNil(t, got, "conversation_summaries 里没有行时按 covered=0 算")
		assert.Equal(t, int64(2), got.LatestSequenceNo)
		assert.Equal(t, int64(0), got.CoveredUntil)
		assert.Equal(t, "", got.PriorSummary)
	})

	t.Run("一条定稿消息都没有的会话永远不是候选", func(t *testing.T) {
		cands, err := repo.SummaryMaintenanceCandidates(ctx, pool, 1)
		require.NoError(t, err)
		assert.Nil(t, find(cands, onlyStreaming.ID), "占位行不能把门槛顶过去")
	})

	t.Run("偏好抽取的门槛判据相同", func(t *testing.T) {
		ids, err := repo.PreferenceExtractionCandidates(ctx, pool, 4)
		require.NoError(t, err)
		assert.NotContains(t, ids, withSummary.ID, "3 < 4，不够门槛")
		assert.NotContains(t, ids, noSummary.ID)
		assert.NotContains(t, ids, onlyStreaming.ID, "占位行不能算进消息数")

		ids, err = repo.PreferenceExtractionCandidates(ctx, pool, 3)
		require.NoError(t, err)
		assert.Contains(t, ids, withSummary.ID, "3 ≥ 3 必须入选")
		assert.NotContains(t, ids, noSummary.ID, "2 < 3 不够门槛")

		ids, err = repo.PreferenceExtractionCandidates(ctx, pool, 2)
		require.NoError(t, err)
		assert.Contains(t, ids, withSummary.ID)
		assert.Contains(t, ids, noSummary.ID, "2 ≥ 2 必须入选")
		assert.NotContains(t, ids, onlyStreaming.ID)
	})

	t.Run("剪枝只删窗口之外的事件", func(t *testing.T) {
		oldID, err := repo.NextEventID(ctx, pool, withSummary.ID)
		require.NoError(t, err)
		require.NoError(t, repo.AppendEvent(ctx, pool, withSummary.ID,
			Event{ID: oldID, Type: "token", Payload: []byte(`{"type":"token","data":{"text":"旧"}}`)}))
		newID, err := repo.NextEventID(ctx, pool, withSummary.ID)
		require.NoError(t, err)
		require.NoError(t, repo.AppendEvent(ctx, pool, withSummary.ID,
			Event{ID: newID, Type: "token", Payload: []byte(`{"type":"token","data":{"text":"新"}}`)}))
		// 另一个会话的事件：不该被这次剪枝带走。
		otherID, err := repo.NextEventID(ctx, pool, noSummary.ID)
		require.NoError(t, err)
		require.NoError(t, repo.AppendEvent(ctx, pool, noSummary.ID,
			Event{ID: otherID, Type: "token", Payload: []byte(`{"type":"token","data":{"text":"别的会话"}}`)}))
		// 把两条事件都推到窗口之外：真表的 created_at 由数据库默认值写，
		// 只能用一条 UPDATE 把它挪走（没有 Go 侧的入口，这是刻意的——
		// 生产代码里没人该改这一列）。
		_, err = pool.Exec(ctx, `UPDATE conversation_events SET created_at = $2 WHERE conversation_id = $1`,
			withSummary.ID, now.Add(-48*time.Hour))
		require.NoError(t, err)

		deleted, err := repo.PruneConversationEvents(ctx, pool, now.Add(-24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted, "只该删掉那两条 48 小时前的事件")

		remaining, err := repo.EventsAfter(ctx, pool, withSummary.ID, 0)
		require.NoError(t, err)
		assert.Empty(t, remaining, "窗口之外的事件必须被删干净")

		kept, err := repo.EventsAfter(ctx, pool, noSummary.ID, 0)
		require.NoError(t, err)
		assert.Len(t, kept, 1, "窗口内的、别的会话的事件一行都不能少")
	})
}
