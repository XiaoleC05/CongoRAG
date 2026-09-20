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

// embeddingColumnType 读一张表 embedding 列此刻真实的类型字符串
// （"halfvec" 或 "halfvec(1024)"）。用 format_type() 而不是 information_schema
// 的 udt_name：后者只给 "halfvec" 三个字,量不出维度,而"已经是 halfvec(dim)
// 了没有"这件事正是下面那条 ALTER 要不要跑的全部依据。
func embeddingColumnType(ctx context.Context, q platform.Querier, table string) (string, error) {
	var formatted string
	err := q.QueryRow(ctx,
		`SELECT format_type(a.atttypid, a.atttypmod)
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = $1 AND a.attname = 'embedding' AND a.attnum > 0 AND NOT a.attisdropped`,
		table).Scan(&formatted)
	if err != nil {
		return "", fmt.Errorf("read %s.embedding column type: %w", table, platform.WrapPgErr(err))
	}
	return formatted, nil
}

// nonNullEmbeddingCount 数一张表里已经存下来的向量有多少行。
// 这是"改列会不会毁掉已有数据"的判据：USING NULL 只影响非 NULL 的向量,
// 空表上它才是无害的。
func nonNullEmbeddingCount(ctx context.Context, q platform.Querier, table string) (int, error) {
	var n int
	// table 只可能来自本文件里写死的 vectorColumns,不是外部输入。
	err := q.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE embedding IS NOT NULL`, table)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s embeddings: %w", table, platform.WrapPgErr(err))
	}
	return n, nil
}

// foreignEmbeddingCount 数一张表里"非 NULL 但不是这个模型生成的"向量有多少行。
//
// 【为什么需要它：只看列类型挡不住换模型】比较的是 embedding_model 这一列，
// 存进去的是 provider 那侧的模型名（retrieval 的 InsertChunks 传的是
// model.ModelID，不是 llm_models 的主键 uuid）。换成另一个维度相同的模型时
// 列类型不变，光比 format_type 会认为"什么都不用做"而放过去；可检索谓词
// embedding_model = $2 里的 $2 已经跟着变成新模型了，于是每一行都匹配不上——
// 文档仍显示 ready、检索静默返回零条，正是这条 ALTER 最初要防的那个形态。
//
// 【用模型名而不是行 id】Bootstrap 每次都为 model 新铸一个 uuid，拿行 id 比的话
// "原样再保存一次"也会被当成换模型而拒绝，那是最常见的一次良性操作。
func foreignEmbeddingCount(ctx context.Context, q platform.Querier, table, embedModel string) (int, error) {
	var n int
	// table 只可能来自本文件里写死的 vectorColumns,不是外部输入。
	err := q.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE embedding IS NOT NULL AND embedding_model <> $1`, table),
		embedModel).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count foreign embeddings in %s: %w", table, platform.WrapPgErr(err))
	}
	return n, nil
}

// alterVectorColumns 把两张表的 embedding 列改成 halfvec(dim),再建 HNSW 索引。
//
// 【幂等：类型已经是 halfvec(dim) 就整条 ALTER 都不执行】
// PostgreSQL 只在 USING 表达式是恒等表达式（裸列名）时才跳过表重写；这里的
// USING 是 NULL 常量,ATColumnChangeRequiresRewrite 恒为 true——即使目标
// typmod 与当前完全相同,这条 ALTER 也会重写整张表,USING NULL 对每一行求值,
// 把所有已存向量置为 NULL。于是"再引导一次"（轮换 Key、改一个填错的字段、
// 加第二个 provider）会清空整个知识库的向量：documents 仍显示 ready、
// 检索静默返回零条、不报错也不打日志。以前这里写下的理由"这两张表此刻
// 本来就是空的"只在第一次运行时成立,所以现在必须先读类型再决定。
//
// 【已存向量不是这个模型产生的：也拒绝】这一条与列类型无关，所以必须在
// 类型判断之前独立查一次。换成一个维度相同的模型时 current == want，光比
// 类型会认为"什么都不用做"放过去，而检索谓词 embedding_model = $2 里的 $2
// 已经变成新模型——每一行都匹配不上，文档仍显示 ready、检索静默返回零条。
// 判据见 foreignEmbeddingCount：比的是 provider 那侧的模型名。
//
// 【类型确实要变、而表里已有向量时：同样拒绝】用 USING NULL 抹掉向量就是
// 永久丢失——v1.0 没有重嵌入 / 重建索引的路径（ProcessDocument 的 switch
// 拒绝 ready 状态的文档,也没有 reindex 接口）。
//
// 两种情况都返回 ErrConflict，让这次保存失败、由调用方明确决定，而不是静默
// 把库变成查不出东西的状态。错误消息会被 problem.go 取最内层那句当作 409 的
// detail，所以必须写清楚"会损失什么"和"应用内没有恢复入口"——不能指向一个
// 产品里根本做不到的动作。
//
// 另外两条不变（技术方案 §六 / 代码架构设计 §5.5）：
//   - opclass 必须是 halfvec_cosine_ops：列是 halfvec、检索用 <=>（余弦距离）,
//     写成 vector_cosine_ops 建不起来（opclass 不接受 halfvec）；
//     写成 halfvec_l2_ops 配 <=> 才是最危险的——不报错，但 planner 会静默
//     放弃索引退化成顺序扫描。
//   - 不用 DROP INDEX：PostgreSQL 重写表（ALTER COLUMN TYPE）时会自动重建
//     依赖这张表的索引，先手动删一次纯属多余。
func alterVectorColumns(ctx context.Context, q platform.Querier, dim int, embedModel string) error {
	want := fmt.Sprintf("halfvec(%d)", dim)
	for _, vc := range vectorColumns {
		foreign, err := foreignEmbeddingCount(ctx, q, vc.table, embedModel)
		if err != nil {
			return err
		}
		if foreign > 0 {
			return fmt.Errorf(
				"%s 里有 %d 行向量不是 %s 生成的,而检索只认当前生效模型产生的向量,改完配置这些分块一条都查不到（文档仍显示 ready,检索静默返回零条）。v1.0 没有重嵌入路径,应用内也没有清空这些向量的入口,要继续只能换回原来的模型,或者先在数据库里手工清掉它们: %w",
				vc.table, foreign, embedModel, platform.ErrConflict)
		}

		current, err := embeddingColumnType(ctx, q, vc.table)
		if err != nil {
			return err
		}

		if current != want {
			vectors, err := nonNullEmbeddingCount(ctx, q, vc.table)
			if err != nil {
				return err
			}
			if vectors > 0 {
				return fmt.Errorf(
					"%s.embedding 现在是 %s 且已有 %d 行非 NULL 向量,改成 %s 会把它们全部清空,而 v1.0 没有重嵌入路径（文档仍显示 ready、检索静默返回零条）。应用内没有清空这些向量的入口,要继续只能先在数据库里手工清掉它们: %w",
					vc.table, current, vectors, want, platform.ErrConflict)
			}

			alterSQL := fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN embedding TYPE halfvec(%d) USING NULL`,
				vc.table, dim)
			if _, err := q.Exec(ctx, alterSQL); err != nil {
				return fmt.Errorf("alter %s.embedding: %w", vc.table, platform.WrapPgErr(err))
			}
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
