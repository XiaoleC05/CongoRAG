// Repo 的 PostgreSQL 实现。这是全项目唯一一处真正把向量编码进 SQL 参数
// 的地方——上面 usecase.go 只处理 Go 的 []float32，这里才转成 pgvector
// 认识的 halfvec。
package retrieval

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
// 【为什么要一次兜底】embedding 上唯一那个 HNSW 索引是全局的（不带
// knowledge_base_id 谓词，见 llm.alterVectorColumns），而这里限定了知识库。
// 过滤发生在 JOIN documents 之后，也就是索引扫描之上：近似扫描先按距离
// 吐出约 hnsw.ef_search（默认 40）个候选，再由上层丢掉不属于本知识库的行，
// 候选耗尽扫描就停止。于是"大表里只占一小部分的知识库"可能只拿到 topK 中
// 的一两行，甚至一行都没有——不报错、不打日志，调用方看到的是"这个知识库
// 里没有相关信息"。
//
// 打开 hnsw.iterative_scan / 调大 hnsw.ef_search 都救不了这个形状：过滤
// 条件在索引扫描之上的 JOIN 节点上，索引拿不到"刚吐出的那行被上层丢了"
// 这个信号（实测记录：把 ef_search 提到 1000、打开 iterative_scan=relaxed_order，
// 仍是 0 行）。
// 而这里的 q 通常是连接池（见 usecase.go 里 Search 不走调用方事务的说明），
// SET LOCAL 没有可靠的作用域，多语句的 "SET; SELECT" 也不保证落在同一条
// 连接上。所以主路径照常用索引（过滤选择率不高时它既快又准），只在返回
// 行数不足 topK 时用 searchExact 精确重算一遍。
func (r *PgRepo) Search(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
	out, err := r.searchANN(ctx, q, kbID, vec, model, topK)
	if err != nil {
		return nil, err
	}
	if len(out) >= topK {
		return out, nil
	}

	exact, err := r.searchExact(ctx, q, kbID, vec, model, topK)
	if err != nil {
		return nil, err
	}
	// 兜底多找回来的行才是"索引被过滤掏空"的证据：分块数本来就少于 topK
	// 的知识库每次搜索都会走到这里，那属于正常情况，不该刷日志。
	if len(exact) > len(out) {
		// 不为一条诊断日志给 PgRepo 加 logger 字段、改构造函数和装配根
		// （同 knowledge/river.go 用 slog.Default() 的取舍）。
		slog.Default().Warn("ann search returned fewer rows than the knowledge base has",
			"knowledge_base_id", kbID, "topk", topK, "ann_rows", len(out), "exact_rows", len(exact))
	}
	return exact, nil
}

// searchANN 是主路径：全局 HNSW 索引取近似 Top-K。
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
func (r *PgRepo) searchANN(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
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
	out, err := scanChunks(rows)
	if err != nil {
		return nil, fmt.Errorf("search chunks of knowledge base %s: %w", kbID, err)
	}
	return out, nil
}

// searchExact 先过滤再排序：把属于这个知识库、且是这个模型算出来的分块
// 先挑出来，再对这批行算距离排序取前 topK。结果与过滤无关（这个知识库
// 有几行匹配、就返回其中最近的 min(topK, 行数) 行），代价是必须把知识库
// 的分块全部算一遍距离——触发它的场景（近似索引短路）本来就意味着这个
// 知识库在大表里只是小头，扫描量不会大。
//
// 【MATERIALIZED 不能省】不加它 CTE 会被内联，整条查询又塌回原来的形状，
// planner 照样可以选"HNSW 索引扫描 + 上层过滤"，等于没改。写成 MATERIALIZED
// 是优化栅栏：内层先按知识库过滤并物化，外层的 ORDER BY ... LIMIT 只能
// 排这批已经过滤好的行。
func (r *PgRepo) searchExact(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
	rows, err := q.Query(ctx,
		`WITH candidates AS MATERIALIZED (
		     SELECT dc.id, dc.document_id, dc.content, dc.embedding, d.filename
		     FROM document_chunks dc
		     JOIN documents d ON d.id = dc.document_id
		     WHERE d.knowledge_base_id = $1
		       AND dc.embedding IS NOT NULL
		       AND dc.embedding_model = $2
		 )
		 SELECT id, document_id, content, filename,
		        1 - (embedding <=> $3::halfvec) AS score
		 FROM candidates
		 ORDER BY embedding <=> $3::halfvec
		 LIMIT $4`,
		kbID, model, pgvector.NewHalfVector(vec), topK,
	)
	if err != nil {
		return nil, fmt.Errorf("exact search in knowledge base %s: %w", kbID, platform.WrapPgErr(err))
	}
	out, err := scanChunks(rows)
	if err != nil {
		return nil, fmt.Errorf("exact search in knowledge base %s: %w", kbID, err)
	}
	return out, nil
}

// scanChunks 把两个查询共用的五列读成 []domain.Chunk。
// 列的顺序必须和上面两条 SELECT 一致。
func scanChunks(rows pgx.Rows) ([]domain.Chunk, error) {
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
