package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// Token 用量记账（issue #47）
//
// token_usage 表从 0003 就建好了，但在这一条之前没有任何写入者。这一组钉的
// 是"每次调用恰一行"以及"记账失败不打断主流程"这两条契约。
// ════════════════════════════════════════════════════════════════

type fakeUsageRepo struct {
	rows []*Usage
	err  error
}

func (f *fakeUsageRepo) InsertUsage(ctx context.Context, q platform.Querier, u *Usage) error {
	if f.err != nil {
		return f.err
	}
	// 【必须看 ctx】真实实现走 q.Exec(ctx, ...)，ctx 已取消时 pgx 连连接都
	// 拿不到。假实现复刻这条，否则"取消之后还能不能记账"这条测试就是空的。
	if err := ctx.Err(); err != nil {
		return err
	}
	f.rows = append(f.rows, u)
	return nil
}

func (f *fakeUsageRepo) UsageSummary(ctx context.Context, q platform.Querier, since, until *time.Time) ([]*UsageByModel, error) {
	return nil, nil
}

func newTestRecorder(repo UsageRepo) usageRecorder {
	return usageRecorder{
		repo: repo, db: nil,
		providerID: uuid.New(), modelID: uuid.New(), kind: KindChat,
	}
}

// 【核心契约】一次调用恰好一行，字段来自传入值。
func TestUsageRecorder_RecordsExactlyOneRow(t *testing.T) {
	repo := &fakeUsageRepo{}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}

	rec.record(context.Background(), 120, 45)

	require.Len(t, repo.rows, 1)
	row := repo.rows[0]
	assert.Equal(t, rec.providerID, row.ProviderID)
	assert.Equal(t, rec.modelID, row.ModelID)
	assert.Equal(t, KindChat, row.Kind)
	assert.Equal(t, 120, row.PromptTokens)
	assert.Equal(t, 45, row.CompletionTokens)
	assert.False(t, row.CreatedAt.IsZero(), "created_at 由 Go 生成，不能是零值")
	assert.Nil(t, row.MessageID, "没有注入消息 id 时留 NULL")
}

// 【记账是观测，不是业务正确性】写失败只记日志，绝不能 panic 或上抛。
func TestUsageRecorder_WriteFailureIsSwallowed(t *testing.T) {
	repo := &fakeUsageRepo{err: errors.New("database is down")}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}

	assert.NotPanics(t, func() {
		rec.record(context.Background(), 10, 5)
	})
	assert.Empty(t, repo.rows)
}

// 客户端断开时请求 ctx 已经被取消——记账仍然要能写下去，否则
// "生成到一半用户关掉页面"的那些调用就永远不计了。
func TestUsageRecorder_WritesAfterContextCancellation(t *testing.T) {
	repo := &fakeUsageRepo{}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec.record(ctx, 33, 7)

	require.Len(t, repo.rows, 1, "WithoutCancel 之后仍然要写得进去")
	assert.Equal(t, 33, repo.rows[0].PromptTokens)
}

// 注入的消息 id 要落到 message_id 上（只有聊天主路径会注入）。
func TestUsageRecorder_RecordsInjectedMessageID(t *testing.T) {
	repo := &fakeUsageRepo{}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}
	msgID := uuid.New()

	rec.record(WithUsageMessage(context.Background(), msgID), 1, 2)

	require.Len(t, repo.rows, 1)
	require.NotNil(t, repo.rows[0].MessageID)
	assert.Equal(t, msgID, *repo.rows[0].MessageID)
}

// 没有 UsageRepo（装配时没传）时记账是个静默的 no-op，不能崩。
func TestUsageRecorder_DisabledIsNoOp(t *testing.T) {
	rec := usageRecorder{}

	assert.False(t, rec.enabled())
	assert.NotPanics(t, func() { rec.record(context.Background(), 1, 1) })
}

// ResponseMeta 里没有 usage 时什么都不记（非流式路径）。
func TestUsageRecorder_SkipsMetaWithoutUsage(t *testing.T) {
	repo := &fakeUsageRepo{}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}

	rec.recordFromMeta(context.Background(), nil)
	rec.recordFromMeta(context.Background(), &schema.ResponseMeta{})

	assert.Empty(t, repo.rows)
}

func TestUsageRecorder_ReadsUsageFromResponseMeta(t *testing.T) {
	repo := &fakeUsageRepo{}
	rec := newTestRecorder(repo)
	rec.db = stubQuerier{}

	rec.recordFromMeta(context.Background(), &schema.ResponseMeta{
		Usage: &schema.TokenUsage{PromptTokens: 9, CompletionTokens: 4},
	})

	require.Len(t, repo.rows, 1)
	assert.Equal(t, 9, repo.rows[0].PromptTokens)
	assert.Equal(t, 4, repo.rows[0].CompletionTokens)
}

// stubQuerier 只用来满足 platform.Querier 的类型要求——记账测试不真的执行
// SQL（假 repo 自己记下调用就够）。三个方法都必须实现，因为 Querier 是接口。
type stubQuerier struct{}

func (stubQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("stubQuerier.Exec: 记账测试不该真的执行 SQL")
}

func (stubQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("stubQuerier.Query: 记账测试不该真的执行 SQL")
}

func (stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}
