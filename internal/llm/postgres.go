// ConfigRepo 的 PostgreSQL 实现,以及 BootstrapEmbedding 用到的 ALTER 语句。
// 所有 SQL 都收敛在这个文件里。
package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ ConfigRepo = (*PgConfigRepo)(nil)

// PgConfigRepo 是无状态的,连接由调用方通过 q 参数传入——和 knowledge.PgRepo
// 同一个模式（代码架构设计 §8.1：只有 usecase 开事务）。
type PgConfigRepo struct{}

func NewPgConfigRepo() *PgConfigRepo {
	return &PgConfigRepo{}
}

// ────────────────────────────────────────────────────────────────
// Provider
// ────────────────────────────────────────────────────────────────

func (r *PgConfigRepo) UpsertProvider(ctx context.Context, q platform.Querier, p *Provider, keyCiphertext []byte) error {
	_, err := q.Exec(ctx,
		`INSERT INTO llm_providers (id, base_url, api_key_encrypted, created_at)
		 VALUES ($1, $2, $3, $4)`,
		p.ID, p.BaseURL, keyCiphertext, p.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert provider %s: %w", p.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgConfigRepo) ListProviders(ctx context.Context, q platform.Querier) ([]*Provider, error) {
	rows, err := q.Query(ctx,
		`SELECT id, base_url, created_at
		 FROM llm_providers
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []*Provider
	for rows.Next() {
		p := &Provider{}
		if err := rows.Scan(&p.ID, &p.BaseURL, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate providers: %w", err)
	}
	return out, nil
}

func (r *PgConfigRepo) GetProvider(ctx context.Context, q platform.Querier, id uuid.UUID) (*Provider, error) {
	p := &Provider{}
	err := q.QueryRow(ctx,
		`SELECT id, base_url, created_at
		 FROM llm_providers
		 WHERE id = $1`, id,
	).Scan(&p.ID, &p.BaseURL, &p.CreatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("provider %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get provider %s: %w", id, platform.WrapPgErr(err))
	}
	return p, nil
}

func (r *PgConfigRepo) GetProviderKey(ctx context.Context, q platform.Querier, id uuid.UUID) ([]byte, error) {
	var ciphertext []byte
	err := q.QueryRow(ctx,
		`SELECT api_key_encrypted FROM llm_providers WHERE id = $1`, id,
	).Scan(&ciphertext)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("provider %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get provider key %s: %w", id, platform.WrapPgErr(err))
	}
	return ciphertext, nil
}

// ────────────────────────────────────────────────────────────────
// Model
// ────────────────────────────────────────────────────────────────

func (r *PgConfigRepo) UpsertModel(ctx context.Context, q platform.Querier, m *Model) error {
	_, err := q.Exec(ctx,
		`INSERT INTO llm_models
		   (id, provider_id, model_id, kind, capabilities,
		    context_window, max_output_tokens, tokenizer_type, embedding_dim, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		m.ID, m.ProviderID, m.ModelID, string(m.Kind), m.Capabilities.toSlice(),
		m.ContextWindow, m.MaxOutputTokens, m.TokenizerType, m.EmbeddingDim, m.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert model %s: %w", m.ID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgConfigRepo) ListModels(ctx context.Context, q platform.Querier) ([]*Model, error) {
	rows, err := q.Query(ctx,
		`SELECT id, provider_id, model_id, kind, capabilities,
		        context_window, max_output_tokens, tokenizer_type, embedding_dim, created_at
		 FROM llm_models
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []*Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate models: %w", err)
	}
	return out, nil
}

func (r *PgConfigRepo) GetModel(ctx context.Context, q platform.Querier, id uuid.UUID) (*Model, error) {
	row := q.QueryRow(ctx,
		`SELECT id, provider_id, model_id, kind, capabilities,
		        context_window, max_output_tokens, tokenizer_type, embedding_dim, created_at
		 FROM llm_models
		 WHERE id = $1`, id)

	m, err := scanModel(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("model %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get model %s: %w", id, platform.WrapPgErr(err))
	}
	return m, nil
}

// rowScanner 是 pgx.Row 和 pgx.Rows 的公共子集——两者都有 Scan,
// 让 scanModel 同时给 ListModels（多行）和 GetModel（单行）复用,
// 不用把同样的字段列表写两遍。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanModel(row rowScanner) (*Model, error) {
	m := &Model{}
	var kind string
	var caps []string
	if err := row.Scan(
		&m.ID, &m.ProviderID, &m.ModelID, &kind, &caps,
		&m.ContextWindow, &m.MaxOutputTokens, &m.TokenizerType, &m.EmbeddingDim, &m.CreatedAt,
	); err != nil {
		return nil, err
	}
	m.Kind = Kind(kind)
	m.Capabilities = capabilitiesFromSlice(caps)
	return m, nil
}

// ────────────────────────────────────────────────────────────────
// 向量列的 ALTER（BootstrapEmbedding 用,开发文档 §4.4 步骤 ③）
// ────────────────────────────────────────────────────────────────

// vectorColumns 是仓库里目前所有存 embedding 的表。
// 0001_init 建表时特意留了这两张，为的就是这一刻能直接 ALTER。
var vectorColumns = []struct {
	table string
	index string // 索引名,ALTER 可以重跑（IF NOT EXISTS 挡第二次执行），索引不能同名两次建
}{
	{table: "document_chunks", index: "document_chunks_embedding_hnsw_idx"},
	{table: "memories", index: "memories_embedding_hnsw_idx"},
}

// alterVectorColumns 把两张表的 embedding 列从"不带维度的 halfvec"
// 改成 halfvec(dim),再建 HNSW 索引。
//
// 三个各自的原因（技术方案 §六 / 代码架构设计 §5.5）都写在旁边：
//   - USING NULL：同时清空旧值和改类型,只重写一次表（20k 行实测省了一半时间）；
//     这两张表此刻本来就是空的,USING NULL 在这里只是"以防将来复用这段代码
//     时表已经不是空的"的保险,不是当前性能考量。
//   - opclass 必须是 halfvec_cosine_ops：列是 halfvec、检索用 <=>（余弦距离）,
//     写成 vector_cosine_ops 建不起来（opclass 不接受 halfvec）；
//     写成 halfvec_l2_ops 配 <=> 才是最危险的——不报错，但 planner 会静默
//     放弃索引退化成顺序扫描。
//   - 不用 DROP INDEX：PostgreSQL 重写表（ALTER COLUMN TYPE）时会自动重建
//     依赖这张表的索引，先手动删一次纯属多余。
func alterVectorColumns(ctx context.Context, q platform.Querier, dim int) error {
	for _, vc := range vectorColumns {
		alterSQL := fmt.Sprintf(
			`ALTER TABLE %s ALTER COLUMN embedding TYPE halfvec(%d) USING NULL`,
			vc.table, dim)
		if _, err := q.Exec(ctx, alterSQL); err != nil {
			return fmt.Errorf("alter %s.embedding: %w", vc.table, platform.WrapPgErr(err))
		}

		indexSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding halfvec_cosine_ops)`,
			vc.index, vc.table)
		if _, err := q.Exec(ctx, indexSQL); err != nil {
			return fmt.Errorf("create hnsw index on %s: %w", vc.table, platform.WrapPgErr(err))
		}
	}
	return nil
}
