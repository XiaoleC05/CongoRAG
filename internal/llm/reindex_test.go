package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 换 embedding 模型时的清空 + 重建（issue #39）
//
// 这条路径的全部价值在原子性上：清空旧向量、改列类型、把全部文档重新排队，
// 三步必须在同一个事务里。分步做的失败态恰好是 v2.0 #5 修掉的那个状态——
// 文档显示 ready、检索静默返回零条。
// ════════════════════════════════════════════════════════════════

// fakeReindexer 记录自己被调用了几次、拿到了哪个 querier、返回多少份文档。
type fakeReindexer struct {
	calls int
	gotQ  platform.Querier
	n     int
	err   error
}

func (f *fakeReindexer) RequeueAllDocuments(ctx context.Context, q platform.Querier) (int, error) {
	f.calls++
	f.gotQ = q
	if f.err != nil {
		return 0, f.err
	}
	return f.n, nil
}

// 允许重置 + 库里确实有别的模型留下的向量 → 清空、改列、重建一条龙，
// 且重建拿到的是**同一个事务**的 querier。
func TestBootstrap_AllowEmbeddingResetRequeuesInTheSameTransaction(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		columnTypes:   map[string]string{"document_chunks": "halfvec(768)", "memories": "halfvec(768)"},
		vectorCounts:  map[string]int{"document_chunks": 12, "memories": 4},
		foreignCounts: map[string]int{"document_chunks": 12, "memories": 4},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)
	reindexer := &fakeReindexer{n: 12}
	uc.reindexer = reindexer

	req := validBootstrapRequest()
	req.AllowEmbeddingReset = true

	result, err := uc.Bootstrap(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, reindexer.calls, "真的清掉了向量就必须重建一次")
	// 【这一条钉的是原子性】newTestUsecase 的 fakeTxManager 把同一个 q 传进
	// 事务回调，所以指针相同 == 它在事务里被调用。传出去之后单独开事务的
	// 实现会拿到另一个 querier。
	assert.Same(t, platform.Querier(q), reindexer.gotQ,
		"重建必须在 Bootstrap 的事务里，否则 ALTER 成功而入队失败会留下一个查不出东西的库")
	assert.Equal(t, 12, result.RequeuedDocuments)
}

// 同模型同维度地重存一次：什么都没清，就不该触发全库重建。
//
// 【为什么按 resetPerformed 判而不是按请求里的开关】前端判不出来"这次到底
// 会不会清"，所以换模型时它会把 allowEmbeddingReset 一律传 true。照开关判的话，
// 每次轮换 Key、改一个填错的字段都会导致全库重建一遍。
func TestBootstrap_NothingResetDoesNotRequeue(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		columnTypes:  map[string]string{"document_chunks": "halfvec(768)", "memories": "halfvec(768)"},
		vectorCounts: map[string]int{"document_chunks": 12, "memories": 4},
		// 没有 foreign 向量、列类型也对 —— 现有向量就是新模型产生的。
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)
	reindexer := &fakeReindexer{n: 12}
	uc.reindexer = reindexer

	req := validBootstrapRequest()
	req.AllowEmbeddingReset = true

	result, err := uc.Bootstrap(context.Background(), req)

	require.NoError(t, err)
	assert.Zero(t, reindexer.calls, "什么都没清就不该重建")
	assert.Zero(t, result.RequeuedDocuments)
}

// 重建失败必须让整个 Bootstrap 失败——否则调用方收到 201，而库里既没有
// 旧向量、也没有任何任务在重建。
func TestBootstrap_ReindexFailureFailsTheWholeBootstrap(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		columnTypes:   map[string]string{"document_chunks": "halfvec(768)", "memories": "halfvec(768)"},
		vectorCounts:  map[string]int{"document_chunks": 12, "memories": 4},
		foreignCounts: map[string]int{"document_chunks": 12, "memories": 4},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)
	uc.reindexer = &fakeReindexer{err: errors.New("queue is down")}

	req := validBootstrapRequest()
	req.AllowEmbeddingReset = true

	result, err := uc.Bootstrap(context.Background(), req)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "queue is down", "底层原因要能看出来")
}
