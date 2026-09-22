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
