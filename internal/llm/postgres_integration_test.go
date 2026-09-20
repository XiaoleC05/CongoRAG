// 连真实 PostgreSQL 的集成测试。
//
// 【为什么不用 testcontainers-go】那是 M5 的范围（开发文档 §10）。这一批测试
// 改用环境变量门控：设了 CONGORAG_TEST_DB_URL 才跑，没设就跳过——本地开发时
// `make up` 起的那个 compose Postgres 就够用，CI 加上这个环境变量就会跑。
//
// 【只有网络请求是假的】Registry.ProbeEmbeddingDimension 用 fakeRegistry
// 代替——这批测试要验证的是"ALTER/INSERT 在真实 Postgres 上到底对不对"，
// 不是"打真实的 LLM API 会不会成功"，两件事分开验证，网络抖动不该让
// 数据库相关的测试变得 flaky。
//
// 【会动全局 schema，且这个副作用曾经真的咬过一次】alterVectorColumns
// 改的是 document_chunks/memories 两张表的列类型，不是某一行数据——
// 光删掉测试自己插入的 provider/model 行，列类型不会跟着变回去。
//
// 【真实事故记录】文档上传（M1 第三条竖线）做完、有真实 768→1024 维
// 数据流转起来之后，这条测试用假的 768 维跑过一次，退出时列类型停在
// halfvec(768)；而 llm_models 里当时"最新"的 embedding 模型实际是
// 1024 维（一次真实的 BYOK Bootstrap 留下的）。两者一旦不一致，
// retrieval.Usecase.IndexDocument 插入真实 1024 维向量时会直接报
// "ERROR: expected 768 dimensions, not 1024 (SQLSTATE 22000)"——
// 不是这条测试本身报错，是它执行完之后，另一条完全独立的生产路径
// 在下一次跑的时候才炸。这正是"改动全局状态的测试，副作用在测试
// 通过之后才暴露"的典型形状。
//
// 【现在的修复】t.Cleanup 里不仅删测试自己插入的行，还要把两张表的
// 列类型改回测试开始前的样子——用 information_schema 在跑之前探测
// 当时的真实类型，跑完照原样 ALTER 回去。如果探测到跑之前列还没有
// 维度（全新数据库，从没做过 BYOK），就 ALTER 回不带维度的 halfvec，
// 和 0001 迁移刚建完时的状态一致。
package llm

import (
	"context"
	"crypto/rand"
	"fmt"
	"encoding/hex"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// requireTestDB 跳过测试,除非 CONGORAG_TEST_DB_URL 设置了。
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

// columnType 拿 embedding 列此刻真实的类型字符串（"halfvec"、
// "halfvec(1024)" 之类）——用 format_type() 而不是 information_schema
// 的 udt_name：那个只给"halfvec"三个字，量不出维度，跑之前是
// halfvec(1024)、恢复成 halfvec(768) 也会被误判成"没变"。
func columnType(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var formatted string
	err := pool.QueryRow(context.Background(),
		`SELECT format_type(a.atttypid, a.atttypmod)
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = $1 AND a.attname = 'embedding' AND a.attnum > 0 AND NOT a.attisdropped`,
		table).Scan(&formatted)
	require.NoError(t, err)
	return formatted
}

// restoreColumnType 把 embedding 列的类型改回 want（columnType 跑之前
// 拍下的快照）。用 USING NULL——反正这条测试跑完之后,这张表在测试期间
// 写进去的任何向量本来就该被清空,不存在"保留数据"这回事。
func restoreColumnType(t *testing.T, pool *pgxpool.Pool, table, want string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding TYPE %s USING NULL`, table, want))
	// 不用 require——这是清理阶段,即使恢复失败也不该让测试本身看起来失败
	// （测试的断言部分已经跑完了），但要把这个反常情况打印出来,不能沉默。
	if err != nil {
		t.Logf("WARNING: failed to restore %s.embedding to %q: %v — 请手动检查 schema 状态", table, want, err)
	}
}

// testSecretBox 用一把随机生成、只存在于这次测试进程里的密钥构造一个
// 真的 AES-GCM box——这批测试要验证"真实加密的密文能在真实数据库里存取
// 往返"，所以密码学部分不用假的,但不需要走 resolveMasterKey 的文件系统
// 三级来源（那部分已经在 secretbox_test.go 单独验收过）。
func testSecretBox(t *testing.T) platform.SecretBox {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	box, err := platform.NewSecretBox(&platform.Config{MasterKey: hex.EncodeToString(key)})
	require.NoError(t, err)
	return box
}

// TestIntegration_Bootstrap_EndToEnd 跑一次完整的 Bootstrap,除了网络探测
// 全部是真实实现：真的 pgxpool、真的 PgConfigRepo、真的 TxManager、真的
// AES-GCM SecretBox。跑完之后直接查 information_schema 和 pg_indexes,
// 确认 ALTER 和建索引真的在数据库层面发生了,不是"SQL 没报错"这种弱验证。
func TestIntegration_Bootstrap_EndToEnd(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()

	// 跑之前先拍一张"两张表向量列此刻长什么样"的快照,跑完照原样还原——
	// 这是这条测试唯一能不破坏其它并发使用同一个数据库的东西
	// （交互式手动测试、别的测试）的办法。见文件顶部注释的事故记录。
	preTestChunksType := columnType(t, pool, "document_chunks")
	preTestMemoriesType := columnType(t, pool, "memories")
	t.Cleanup(func() {
		restoreColumnType(t, pool, "document_chunks", preTestChunksType)
		restoreColumnType(t, pool, "memories", preTestMemoriesType)
	})

	repo := NewPgConfigRepo()
	box := testSecretBox(t)
	txm := platform.NewTxManager(pool)
	reg := &fakeRegistry{probeDim: 768} // 换一个和其它测试不同的维度,便于确认这次跑的是这次的结果
	uc := NewUsecase(repo, box, reg, txm, pool)

	req := validBootstrapRequest()
	req.APIKey = "sk-integration-test-real-crypto-path"

	result, err := uc.Bootstrap(ctx, req)
	require.NoError(t, err)
	t.Cleanup(func() {
		// provider 删除会级联删掉两个 model（ON DELETE CASCADE）。
		_, _ = pool.Exec(context.Background(), `DELETE FROM llm_providers WHERE id = $1`, result.Provider.ID)
	})

	// ── 数据库里的三行确实存在,且能整条链路解密回原始 Key ──
	gotProvider, err := repo.GetProvider(ctx, pool, result.Provider.ID)
	require.NoError(t, err)
	assert.Equal(t, req.BaseURL, gotProvider.BaseURL)

	ciphertext, err := repo.GetProviderKey(ctx, pool, result.Provider.ID)
	require.NoError(t, err)
	plain, err := box.Open(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, req.APIKey, string(plain), "从真实数据库读回的密文,用真实 AES-GCM 解密,必须等于原始 Key")

	gotEmbedding, err := repo.GetModel(ctx, pool, result.EmbeddingModel.ID)
	require.NoError(t, err)
	assert.Equal(t, 768, gotEmbedding.EmbeddingDim)

	// ── document_chunks.embedding 真的变成了 halfvec(768) ──
	// pgvector 的类型在 information_schema 里 udt_name 是 halfvec,
	// 维度信息在 pg_catalog.pg_attribute.atttypmod 里（不是 udt_name 能带出来的），
	// 直接拿 format_type() 最准确,它会把维度一起格式化成 "halfvec(768)"。
	for _, table := range []string{"document_chunks", "memories"} {
		var formatted string
		err := pool.QueryRow(ctx,
			`SELECT format_type(a.atttypid, a.atttypmod)
			 FROM pg_attribute a
			 JOIN pg_class c ON c.oid = a.attrelid
			 WHERE c.relname = $1 AND a.attname = 'embedding' AND a.attnum > 0 AND NOT a.attisdropped`,
			table).Scan(&formatted)
		require.NoError(t, err)
		assert.Equal(t, "halfvec(768)", formatted, "%s.embedding 的真实列类型", table)
	}

	// ── 两个 HNSW 索引真的建出来了,而且 opclass 是 halfvec_cosine_ops ──
	for _, idx := range []string{"document_chunks_embedding_hnsw_idx", "memories_embedding_hnsw_idx"} {
		var indexdef string
		err := pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE indexname = $1`, idx,
		).Scan(&indexdef)
		require.NoError(t, err, "索引 %s 应该存在", idx)
		assert.Contains(t, indexdef, "hnsw")
		assert.Contains(t, indexdef, "halfvec_cosine_ops")
	}

	// ── 索引真的能被用来插入和查询一个向量,不只是"建出来了但用不了" ──
	vec := make([]float32, 768)
	for i := range vec {
		vec[i] = float32(i) / 768
	}
	docID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status)
		 VALUES ($1, (SELECT id FROM knowledge_bases LIMIT 1), 'probe.txt', $2, 'ready')`,
		docID, "integration-test-"+docID.String())
	if err != nil {
		t.Skipf("跳过向量插入验证：环境里没有可用的 knowledge_bases 行（%v）", err)
		return
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, docID)
	})

	var chunkCount int
	err = pool.QueryRow(ctx,
		`INSERT INTO document_chunks (document_id, content, embedding, embedding_model)
		 VALUES ($1, 'probe chunk', $2::halfvec, $3)
		 RETURNING 1`,
		docID, formatVectorLiteral(vec), result.EmbeddingModel.ID.String(),
	).Scan(&chunkCount)
	require.NoError(t, err, "插入一条带向量的分块必须成功——证明列类型和索引真的兼容")

	var distance float64
	err = pool.QueryRow(ctx,
		`SELECT embedding <=> $1::halfvec
		 FROM document_chunks
		 WHERE document_id = $2 AND embedding_model = $3`,
		formatVectorLiteral(vec), docID, result.EmbeddingModel.ID.String(),
	).Scan(&distance)
	require.NoError(t, err, "余弦距离查询必须成功——证明 <=> 操作符和 halfvec 列真的兼容")
	assert.InDelta(t, 0, distance, 1e-4, "同一个向量和自己的余弦距离应该几乎是 0")
}

// TestIntegration_PgConfigRepo_ModelKindConstraint 直接绕过 Go 层的 Kind
// 类型,插入一个非法的 kind 值,证明数据库层的 CHECK 约束是真的存在、真的
// 在拦——不是只活在 Go 代码的 if 判断里。0002 迁移写了这条约束但从没
// 被任何测试触发过,这条测试就是触发它的地方。
func TestIntegration_PgConfigRepo_ModelKindConstraint(t *testing.T) {
	pool := requireTestDB(t)
	ctx := context.Background()

	providerID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO llm_providers (id, base_url, api_key_encrypted) VALUES ($1, 'https://example.invalid', 'x')`,
		providerID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM llm_providers WHERE id = $1`, providerID)
	})

	_, err = pool.Exec(ctx,
		`INSERT INTO llm_models (id, provider_id, model_id, kind) VALUES ($1, $2, 'x', 'not-a-real-kind')`,
		uuid.New(), providerID)

	require.Error(t, err, "kind 不是 chat/embedding 时,数据库必须拒绝,不能安静地插进去")
}

// formatVectorLiteral 把 Go 的 []float32 转成 pgvector 认识的文本字面量
// "[0.1,0.2,...]"。生产代码从不需要这个（写路径走的是 pgvector-go 的
// pgx 编解码器,见开发文档后续接检索时的说明),这里只是集成测试图省事,
// 不想为了一次性验证去接整套 pgxvec.RegisterTypes。
func formatVectorLiteral(v []float32) string {
	s := "["
	for i, f := range v {
		if i > 0 {
			s += ","
		}
		s += formatFloat32(f)
	}
	return s + "]"
}

func formatFloat32(f float32) string {
	return strconv.FormatFloat(float64(f), 'f', -1, 32)
}
