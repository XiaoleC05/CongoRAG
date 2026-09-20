// Repo 的 PostgreSQL 实现。这是全项目唯一一处真正把向量编码进 SQL 参数
// 的地方——上面 usecase.go 只处理 Go 的 []float32，这里才转成 pgvector
// 认识的 halfvec。
package retrieval

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Repo = (*PgRepo)(nil)

// PgRepo 同样是无状态的——连接由调用方通过 q 传入，和 knowledge.PgRepo
// 是同一个模式。
type PgRepo struct{}

func NewPgRepo() *PgRepo {
	return &PgRepo{}
}

// InsertChunks 逐条插入。
//
// 【为什么不是一条多值 INSERT 或 COPY】M1 阶段一份文档切出来的分块数量
// 通常是几十到几百条，逐条 Exec 的往返成本在这个量级不值得为了省下来
// 换取更复杂的批量写入代码（pgx.Batch 或 COPY）。真的出现"单份文档几万
// 分块"的场景再优化——那时候这里的注释本身就是"什么时候该换"的判据。
func (r *PgRepo) InsertChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error {
	for i, c := range chunks {
		// pgvector.NewHalfVector 把 []float32 包成 pgx 的 halfvec 编解码器
		// 认识的类型；真正的二进制编码发生在 q.Exec 内部（pgxvec.RegisterTypes
		// 已经在装配根注册过，见 apps/worker/internal/app/app.go）。
		vec := pgvector.NewHalfVector(vecs[i])
		_, err := q.Exec(ctx,
			`INSERT INTO document_chunks (id, document_id, content, embedding, embedding_model)
			 VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), docID, c.Content, vec, model,
		)
		if err != nil {
			return fmt.Errorf("insert chunk %d/%d of document %s: %w", i+1, len(chunks), docID, platform.WrapPgErr(err))
		}
	}
	return nil
}

func (r *PgRepo) DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error {
	_, err := q.Exec(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, docID)
	if err != nil {
		return fmt.Errorf("delete chunks of document %s: %w", docID, platform.WrapPgErr(err))
	}
	return nil
}

// Search 按余弦距离找出 Top-K 最相似的分块。
//
// 三个 WHERE 条件各自的理由：
//   - d.knowledge_base_id = $1：检索范围限定在一个知识库内，不跨库搜。
//   - dc.embedding IS NOT NULL：文档刚入库、还没跑完向量化管道时这一列
//     是空的（0001_init 的注释），排除掉还没准备好的分块。
//   - dc.embedding_model = $2：技术方案 §六反复强调的那条——不加这个
//     过滤，一旦表里同时存在不同维度的向量，<=> 运算会直接报 22000。
//
// JOIN documents 只为了拿 filename——domain.Chunk.Filename 只有 Search
// 的返回值会填（见 domain.go 的注释），citation 展示要用到文件名。
func (r *PgRepo) Search(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
	rows, err := q.Query(ctx,
		`SELECT dc.id, dc.document_id, dc.content, d.filename,
		        1 - (dc.embedding <=> $3::halfvec) AS score
		 FROM document_chunks dc
		 JOIN documents d ON d.id = dc.document_id
		 WHERE d.knowledge_base_id = $1
		   AND dc.embedding IS NOT NULL
		   AND dc.embedding_model = $2
		 ORDER BY dc.embedding <=> $3::halfvec
		 LIMIT $4`,
		kbID, model, pgvector.NewHalfVector(vec), topK,
	)
	if err != nil {
		return nil, fmt.Errorf("search chunks of knowledge base %s: %w", kbID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []domain.Chunk
	for rows.Next() {
		c := domain.Chunk{}
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Content, &c.Filename, &c.Score); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}
	return out, nil
}
