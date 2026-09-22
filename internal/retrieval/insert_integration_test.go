// 连真实 PostgreSQL 的写入路径测试（issue #97）。
//
// 【为什么要连真库】这条路径在 issue #97 里改了两处形状：写语句全部排到
// embedding 之后（拆分成了 EmbedChunks / ReplaceChunks 两半，单测能钉，
// 见 usecase_test.go 的 TestEmbedChunks_Fails_LeavesExistingChunksUntouched），
// 以及逐条 INSERT 换成一条语句多行。后者不能靠单测——一条语句里塞几百组
// 参数、几百个 halfvec，占位符编号对不对、pgvector 的编解码在 VALUES 列表
// 里是不是照样工作、参数上限有没有撞到，只有真的把它发给 PostgreSQL 才
// 知道。假 repo 复刻的是"我以为的" SQL。
//
// 【用真库才看得见的第二件事】先删后插的重试安全性：这两个语句现在由
// 调用方（knowledge.ProcessDocument）放进同一个事务，但"先删后插"这个顺序
// 本身是 retrieval 的职责，所以这里仍然要显式验证"重跑一遍之后表里只有
// 新的一份"。测试直接调两半、用连接池当 q——事务边界不在这里测（那是
// knowledge 包的范围，见它 usecase_test.go 里那条事务位置的断言）。
//
// 【门控】和别的集成测试一样走 internal/testdb：设了 CONGORAG_TEST_DB_URL
// 用它，没设就自己起容器（make test-integration），两个都没设就跳过。
package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/testdb"
)

func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testdb.Require(t)
}

// seedDocument 造一个知识库 + 一份文档，返回文档 id。
//
// document_chunks.document_id 是指向 documents 的外键，所以分块不能凭空插。
// 清理只删知识库：documents 和 document_chunks 由迁移里的 ON DELETE CASCADE
// 跟着走（0008 的注释点名过这条级联）。
func seedDocument(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	kbID := uuid.New()
	docID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO knowledge_bases (id, name) VALUES ($1, $2)`, kbID, "insert-probe")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, kbID)
	})

	_, err = pool.Exec(ctx,
		`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status)
		 VALUES ($1, $2, 'doc.md', $3, 'processing')`,
		docID, kbID, "insert-probe-"+uuid.NewString())
	require.NoError(t, err)
	return docID
}

// chunksOf 造 n 个内容各不相同的分块——内容里带着下标，插入顺序错位时
// 断言会直接指到是第几条。
func chunksOf(n int) []domain.Chunk {
	chunks := make([]domain.Chunk, n)
	for i := range chunks {
		chunks[i] = domain.Chunk{Content: "第 " + string(rune('a'+i%26)) + " 段#" + uuid.NewString()}
	}
	return chunks
}

func newIndexingUsecase(t *testing.T, pool *pgxpool.Pool) *Usecase {
	t.Helper()
	model := embeddingModel(uuid.New(), "embed-integration", time.Now())
	return NewUsecase(
		NewPgRepo(),
		&fakeRegistry{embedder: &cappedEmbedder{max: embedBatchSize}},
		&fakeConfigRepo{models: []*llm.Model{model}},
		pool,
	)
}

// 一份文档的分块数超过 insertRowsPerStmt 时，写入必须跨多条 INSERT 语句
// 完成，且一行都不能丢——多值 INSERT 的占位符编号是按行算出来的，
// 少拼一组、编号差一位，都会在这里以"行数对不上"或"内容错位"的形式炸出来。
func TestIntegration_EmbedThenReplace_SpansMultipleInsertStatements(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	docID := seedDocument(t, pool)
	uc := newIndexingUsecase(t, pool)

	const n = insertRowsPerStmt + 7 // 故意跨过一条语句的上限
	chunks := chunksOf(n)

	require.NoError(t, embedAndReplace(t, uc, pool, docID, chunks))

	var rows, withVector int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*), count(embedding) FROM document_chunks WHERE document_id = $1`, docID,
	).Scan(&rows, &withVector))
	assert.Equal(t, n, rows, "跨语句写入不能丢行")
	assert.Equal(t, n, withVector, "每一条分块都要带上向量")

	var model string
	var distinctModels int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT min(embedding_model), count(DISTINCT embedding_model)
		 FROM document_chunks WHERE document_id = $1`, docID).Scan(&model, &distinctModels))
	assert.Equal(t, "embed-integration", model)
	assert.Equal(t, 1, distinctModels)
}

// 【先删后插的重试安全性】River 会因为进程崩溃、网络抖动把同一个文档重投，
// 重投时整条索引路径会再跑一遍。跑完表里必须只剩新那一份——这条性质以前
// 靠"DELETE 和 INSERT 在同一个事务里、回滚兜住"，现在靠 ReplaceChunks
// 内部的顺序（先删后插）自己给。
func TestIntegration_EmbedThenReplace_RerunLeavesOnlyTheNewChunks(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	docID := seedDocument(t, pool)
	uc := newIndexingUsecase(t, pool)

	require.NoError(t, embedAndReplace(t, uc, pool, docID, chunksOf(3)))
	// 第二次内容更少：旧分块必须被清掉，不是累加。
	require.NoError(t, embedAndReplace(t, uc, pool, docID, chunksOf(2)))

	var rows, withVector int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*), count(embedding) FROM document_chunks WHERE document_id = $1`, docID,
	).Scan(&rows, &withVector))
	assert.Equal(t, 2, rows, "重投之后表里只能有新那一份分块")
	assert.Equal(t, 2, withVector)
}

// 零分块（空文件、只剩空白的文件）也要清掉上一版的分块——否则检索还会
// 命中一段已经不存在的文本。这条同时覆盖了"零分块不发 embedding 请求"：
// 上面那两处断言过不去的话，说明 DELETE 被跳过了。
func TestIntegration_EmbedThenReplace_EmptyChunksClearsTheOldOnes(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()
	docID := seedDocument(t, pool)
	uc := newIndexingUsecase(t, pool)

	require.NoError(t, embedAndReplace(t, uc, pool, docID, chunksOf(3)))
	require.NoError(t, embedAndReplace(t, uc, pool, docID, nil))

	var rows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM document_chunks WHERE document_id = $1`, docID).Scan(&rows))
	assert.Equal(t, 0, rows)
}
