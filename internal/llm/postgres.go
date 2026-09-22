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

// UpdateModel 见 port.go 的契约。
func (r *PgConfigRepo) UpdateModel(ctx context.Context, q platform.Querier, m *Model) error {
	tag, err := q.Exec(ctx,
		`UPDATE llm_models
		    SET model_id = $2, capabilities = $3, context_window = $4,
		        max_output_tokens = $5, tokenizer_type = $6
		  WHERE id = $1`,
		m.ID, m.ModelID, m.Capabilities.toSlice(),
		m.ContextWindow, m.MaxOutputTokens, m.TokenizerType)
	if err != nil {
		return fmt.Errorf("update model %s: %w", m.ID, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("model %s: %w", m.ID, platform.ErrNotFound)
	}
	return nil
}

// DeleteModel 见 port.go 的契约。
func (r *PgConfigRepo) DeleteModel(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM llm_models WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete model %s: %w", id, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("model %s: %w", id, platform.ErrNotFound)
	}
	return nil
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
// 永久丢失。
//
// 【allowReset 是 issue #39 加的出口】为 false 时两种情况都返回
// ErrEmbeddingResetRequired，让这次保存失败、由调用方明确决定；为 true 时
// 先清掉不属于新模型的那些向量（类型要变的情况下由下面那条 ALTER 的
// USING NULL 完成），再由调用方在**同一个事务里**把全部文档重新排队重建——
// 所以"永久丢失"这个前提不再成立：丢掉的会在重建完成后回来。
//
// 【返回值里的 resetPerformed 就是"这次真的清掉过东西"】调用方据此决定
// 要不要重建；它不是"用户传了 allowReset"的回声——同模型同维度地重存一次
// 会传 true，但什么都没清，那时候不该触发一次全库重建。
//
// 拒绝时返回的是本包的 ErrEmbeddingResetRequired 而不是 platform.ErrConflict：
// 前端要按它弹确认框，用一个笼统的 conflict 分不出来（见 errors.go 的注释）。
// 错误消息会被 problem.go 取最内层那句当作 detail，所以必须写清楚"会损失
// 什么"以及"怎么继续"。
//
// 另外两条不变（技术方案 §六 / 代码架构设计 §5.5）：
//   - opclass 必须是 halfvec_cosine_ops：列是 halfvec、检索用 <=>（余弦距离）,
//     写成 vector_cosine_ops 建不起来（opclass 不接受 halfvec）；
//     写成 halfvec_l2_ops 配 <=> 才是最危险的——不报错，但 planner 会静默
//     放弃索引退化成顺序扫描。
//   - 不用 DROP INDEX：PostgreSQL 重写表（ALTER COLUMN TYPE）时会自动重建
//     依赖这张表的索引，先手动删一次纯属多余。
func alterVectorColumns(ctx context.Context, q platform.Querier, dim int, embedModel string, allowReset bool) (resetPerformed bool, err error) {
	want := fmt.Sprintf("halfvec(%d)", dim)
	for _, vc := range vectorColumns {
		foreign, err := foreignEmbeddingCount(ctx, q, vc.table, embedModel)
		if err != nil {
			return resetPerformed, err
		}
		if foreign > 0 {
			if !allowReset {
				return resetPerformed, fmt.Errorf(
					"%s 里有 %d 行向量不是 %s 生成的,而检索只认当前生效模型产生的向量,改完配置这些分块一条都查不到（文档仍显示 ready,检索静默返回零条）。要换模型就把 allowEmbeddingReset 打开,服务端会在同一个事务里清空这些向量、改列类型,并把全部文档重新排队重建: %w",
					vc.table, foreign, embedModel, ErrEmbeddingResetRequired)
			}
			// 【只在允许重置时才真的清】清掉之后这些分块检索不到，直到
			// 重建任务重跑完——这是用户已经确认过的代价。
			if err := clearStaleEmbeddings(ctx, q, vc.table, embedModel); err != nil {
				return resetPerformed, err
			}
			resetPerformed = true
		}

		current, err := embeddingColumnType(ctx, q, vc.table)
		if err != nil {
			return resetPerformed, err
		}

		if current != want {
			vectors, err := nonNullEmbeddingCount(ctx, q, vc.table)
			if err != nil {
				return resetPerformed, err
			}
			if vectors > 0 && !allowReset {
				return resetPerformed, fmt.Errorf(
					"%s.embedding 现在是 %s 且已有 %d 行非 NULL 向量,改成 %s 会把它们全部清空。要换模型就把 allowEmbeddingReset 打开,服务端会改列类型并把全部文档重新排队重建: %w",
					vc.table, current, vectors, want, ErrEmbeddingResetRequired)
			}

			// 【不需要在这里显式清空】下面这条 ALTER 的 USING NULL 对每一行
			// 求值，会把所有已存向量置空——那正是"改列类型"的语义。只是要
			// 记得它发生过（resetPerformed），调用方据此把文档重新排队。
			if vectors > 0 {
				resetPerformed = true
			}

			alterSQL := fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN embedding TYPE halfvec(%d) USING NULL`,
				vc.table, dim)
			if _, err := q.Exec(ctx, alterSQL); err != nil {
				return resetPerformed, fmt.Errorf("alter %s.embedding: %w", vc.table, platform.WrapPgErr(err))
			}
		}

		indexSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding halfvec_cosine_ops)`,
			vc.index, vc.table)
		if _, err := q.Exec(ctx, indexSQL); err != nil {
			return resetPerformed, fmt.Errorf("create hnsw index on %s: %w", vc.table, platform.WrapPgErr(err))
		}
	}
	return resetPerformed, nil
}

// clearStaleEmbeddings 清掉一张表里「不是当前生效模型生成的」向量。
//
// 【必须用 IS DISTINCT FROM，不能用 <>】embedding_model 为 NULL 的行用
// `<> $1` 比较得到的是 NULL 而不是 true，会被整条 WHERE 漏掉——那些行就是
// 「有向量但没有模型标记」的残留，正是最该被清掉的一类。
//
// 置 NULL 而不是 DELETE：这些行代表的分块内容本身还在（文件的切分结果），
// 要重建的只是向量。删行会让 document_chunks 少一批，检索会缺内容。
func clearStaleEmbeddings(ctx context.Context, q platform.Querier, table, embedModel string) error {
	// table 只可能来自本文件里写死的 vectorColumns,不是外部输入。
	_, err := q.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET embedding = NULL, embedding_model = NULL
		 WHERE embedding IS NOT NULL AND embedding_model IS DISTINCT FROM $1`, table),
		embedModel)
	if err != nil {
		return fmt.Errorf("clear stale embeddings in %s: %w", table, platform.WrapPgErr(err))
	}
	return nil
}
