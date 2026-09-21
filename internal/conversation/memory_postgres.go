// memory_postgres.go 是 MemoryRepo 唯一的实现——长期记忆存取，和会话/
// 消息/事件那批 SQL（postgres.go）分开放在自己的文件里，理由和
// knowledge 包 document_postgres.go 分离出 postgres.go 一样：
// 表不同、关注点不同，放一个文件只会增加找起来的成本。
package conversation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ MemoryRepo = (*PgMemoryRepo)(nil)

// PgMemoryRepo 是无状态的，连接由调用方通过 q 传入——和本包其余 Repo
// 同一个模式。
type PgMemoryRepo struct{}

func NewPgMemoryRepo() *PgMemoryRepo {
	return &PgMemoryRepo{}
}

// Insert 写一条长期记忆。embeddingModel 和 retrieval.PgRepo.InsertChunks
// 同样的理由要单独存一列：后续换了 embedding 模型后,SearchByRelevance
// 必须能按"当前生效模型"过滤,不然不同维度的向量混在一次 <=> 运算里
// 会直接报 22000（技术方案反复强调的那条）。
func (r *PgMemoryRepo) Insert(ctx context.Context, q platform.Querier, m *domain.Memory, vec []float32, embeddingModel string) error {
	metadata := metadataJSON(m.Metadata)
	_, err := q.Exec(ctx,
		`INSERT INTO memories (id, scope, content, embedding, embedding_model, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		m.ID, m.Scope, m.Content, pgvector.NewHalfVector(vec), embeddingModel, metadata,
	)
	if err != nil {
		return fmt.Errorf("insert memory %s: %w", m.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgMemoryRepo) ListNeedingEmbedding(ctx context.Context, q platform.Querier, activeModel string, limit int) ([]*domain.Memory, error) {
	// 【判据与 clearStaleEmbeddings 对称】那边清的是
	// `embedding IS NOT NULL AND embedding_model IS DISTINCT FROM $1`，
	// 这边要的正是它清完之后的样子，外加从来就没算过向量的那些行。
	//
	// 【必须用 IS DISTINCT FROM 而不是 <>】embedding_model 为 NULL 的行用
	// `<> $1` 比较得到 NULL 而不是 true，会被漏掉——而那些行恰恰是最需要
	// 补算的（向量为空、模型标记也为空）。
	rows, err := q.Query(ctx,
		`SELECT id, scope, content
		 FROM memories
		 WHERE embedding IS NULL OR embedding_model IS DISTINCT FROM $1
		 ORDER BY created_at
		 LIMIT $2`, activeModel, limit)
	if err != nil {
		return nil, fmt.Errorf("list memories needing embedding: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	out := make([]*domain.Memory, 0)
	for rows.Next() {
		m := &domain.Memory{}
		if err := rows.Scan(&m.ID, &m.Scope, &m.Content); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories needing embedding: %w", err)
	}
	return out, nil
}

func (r *PgMemoryRepo) UpdateEmbedding(ctx context.Context, q platform.Querier, id uuid.UUID, vec []float32, embeddingModel string) error {
	tag, err := q.Exec(ctx,
		`UPDATE memories SET embedding = $2, embedding_model = $3 WHERE id = $1`,
		id, pgvector.NewHalfVector(vec), embeddingModel)
	if err != nil {
		return fmt.Errorf("update memory %s embedding: %w", id, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		// 记忆被删掉了（目前没有删除入口，但重建是异步的，将来可能有）。
		// 这不是错误：调用方按"已处理"继续即可。
		return fmt.Errorf("memory %s: %w", id, platform.ErrNotFound)
	}
	return nil
}

// SearchByRelevance 按余弦距离找出一个 scope 下 Top-K 最相关的记忆。
//
// 【按 scope 过滤,不是全表检索】方案没有多用户预期（部署边界是本地
// 单机应用，见开发文档 §1），scope 因此不是"哪个用户"，而是"哪一类
// 记忆"的分类维度（比如"全局偏好" vs 未来可能的"某个知识库下的偏好"）——
// MemoryRequest 没有携带 scope 字段是本实现的选择：ExtractPreferences
// 目前只写一种 scope（见 usecase 里的 defaultMemoryScope），
// 多 scope 场景出现之前不提前给 MemoryRequest 加一个用不上的参数。
func (r *PgMemoryRepo) SearchByRelevance(ctx context.Context, q platform.Querier, req domain.MemoryRequest, vec []float32, embeddingModel string) ([]domain.Memory, error) {
	rows, err := q.Query(ctx,
		`SELECT id, scope, content, metadata
		 FROM memories
		 WHERE scope = $1
		   AND embedding IS NOT NULL
		   AND embedding_model = $2
		 ORDER BY embedding <=> $3::halfvec
		 LIMIT $4`,
		defaultMemoryScope, embeddingModel, pgvector.NewHalfVector(vec), req.K,
	)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []domain.Memory
	for rows.Next() {
		m := domain.Memory{}
		var metadataRaw []byte
		if err := rows.Scan(&m.ID, &m.Scope, &m.Content, &metadataRaw); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		m.Metadata = unmarshalMetadata(metadataRaw)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return out, nil
}

// metadataJSON/unmarshalMetadata 把 map[string]any 和 jsonb 列互相转换。
// 反序列化失败时返回空 map 而不是报错——和 scanMessages 处理坏掉的
// token_usage 同一个取舍（postgres.go 的注释）：一行历史数据格式不对,
// 不该让整条 SearchByRelevance 查询失败。
func metadataJSON(m map[string]any) []byte {
	if m == nil {
		m = map[string]any{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func unmarshalMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
