// DocRepo 的 PostgreSQL 实现。和 postgres.go（KBRepo 的实现）分成两个文件，
// 理由跟 document.go/model.go 一样：内容太多放不进一个文件，不是两个包。
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ DocRepo = (*PgDocRepo)(nil)

// PgDocRepo 和 PgRepo 一样是无状态的：连接由调用方通过 q 参数传入。
type PgDocRepo struct{}

func NewPgDocRepo() *PgDocRepo {
	return &PgDocRepo{}
}

func (r *PgDocRepo) Insert(ctx context.Context, q platform.Querier, d *Document) error {
	_, err := q.Exec(ctx,
		`INSERT INTO documents (id, knowledge_base_id, filename, storage_key, status, byte_size, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		d.ID, d.KnowledgeBaseID, d.Filename, d.StorageKey, string(d.Status), d.ByteSize, d.CreatedAt, d.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert document %s: %w", d.ID, platform.WrapPgErr(err))
	}
	return nil
}

// documentByIDSQL 和下面两条列表 SQL 一样带上了分块数的聚合
// （issue #82）——同一个 Document 类型在三处被读出来，任何一处漏了
// chunkCount 都会让"有的入口显示分块数、有的入口恒为 0"，
// 而那种不一致在界面上很难被发现。
const documentByIDSQL = `SELECT d.id, d.knowledge_base_id, d.filename, d.storage_key,
	        d.status, d.byte_size, d.created_at, d.updated_at,
	        count(c.id)
	 FROM documents d
	 LEFT JOIN document_chunks c ON c.document_id = d.id
	 WHERE d.id = $1
	 GROUP BY d.id`

func (r *PgDocRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*Document, error) {
	d := &Document{}
	var status string
	err := q.QueryRow(ctx, documentByIDSQL, id).Scan(
		&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.StorageKey, &status, &d.ByteSize,
		&d.CreatedAt, &d.UpdatedAt, &d.ChunkCount)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("document %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get document %s: %w", id, platform.WrapPgErr(err))
	}
	d.Status = Status(status)
	return d, nil
}

func (r *PgDocRepo) UpdateStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to Status) error {
	// 第一层防护：业务规则。这一层挡的是"代码逻辑写错了，试图做一次
	// 根本不该发生的迁移"（比如 ready 直接跳到 queued）。
	if !from.CanTransition(to) {
		return fmt.Errorf("document %s: illegal transition %s -> %s: %w", id, from, to, platform.ErrConflict)
	}

	// 第二层防护：SQL 里的 WHERE status = $2 是 CAS（compare-and-swap）。
	// 这一层挡的是"业务规则本身没错，但数据库里的当前状态和调用方以为的
	// 不一样了"——通常是并发的另一次调用先改过了。两种情况（行不存在 /
	// 状态已经变了）用同一个 RowsAffected == 0 分支处理，都映射成
	// ErrConflict：调用方需要的信息是"这次没改成功"，具体是哪种原因
	// 对它的下一步动作没有区别。
	//
	// updated_at 由 Go 生成后传入而不是用 SQL 的 now()：见 KBRepo.Rename
	// 同样的注释——两个时钟来源会导致时间戳看起来倒退。
	tag, err := q.Exec(ctx,
		`UPDATE documents SET status = $3, updated_at = $4 WHERE id = $1 AND status = $2`,
		id, string(from), string(to), time.Now())
	if err != nil {
		return fmt.Errorf("update document %s status: %w", id, platform.WrapPgErr(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document %s: expected status %s but no row matched: %w", id, from, platform.ErrConflict)
	}
	return nil
}

// reindexableStatuses 是可以被重新索引的状态集合，拼进下面三条 SQL 的
// IN 列表里（编译期常量拼接，不是运行期拼用户输入）。
//
// 【为什么包含 'processing'】见 document.go 的 transitions 注释：跳过正在
// processing 的文档，会让它的旧模型向量在换模型时被清空语句漏掉，结果是
// 一份永久「ready 但检索查不到」的残骸。代价是一次可自愈的竞争。
const reindexableStatuses = `('ready', 'failed', 'processing')`

// markForReindexSQL 是三条 Mark*ForReindex 共用的 UPDATE 骨架。
//
// 【updated_at 由 Go 生成后传入】和 UpdateStatus 同一个理由：两个时钟
// 来源会让时间戳看起来倒退。
const markForReindexSQL = `UPDATE documents SET status = 'queued', updated_at = $1
	 WHERE status IN ` + reindexableStatuses

func (r *PgDocRepo) MarkForReindex(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	tag, err := q.Exec(ctx, markForReindexSQL+` AND id = $2`, time.Now(), id)
	if err != nil {
		return fmt.Errorf("mark document %s for reindex: %w", id, platform.WrapPgErr(err))
	}
	// 和 UpdateStatus 同一个约定：影响 0 行说明这个文档不在可重建状态里
	// （已经在 queued、或者 id 根本不存在）——调用方需要的信息是"这次没改
	// 成功"，具体哪种原因对它的下一步没有区别。
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("document %s is not in a reindexable state: %w", id, platform.ErrConflict)
	}
	return nil
}

func (r *PgDocRepo) MarkKnowledgeBaseForReindex(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]uuid.UUID, error) {
	return r.markForReindexReturningIDs(ctx, q,
		markForReindexSQL+` AND knowledge_base_id = $2 RETURNING id`, kbID)
}

func (r *PgDocRepo) MarkAllForReindex(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	return r.markForReindexReturningIDs(ctx, q, markForReindexSQL+` RETURNING id`)
}

// markForReindexReturningIDs 跑一条 RETURNING id 的 UPDATE 并收齐结果。
func (r *PgDocRepo) markForReindexReturningIDs(ctx context.Context, q platform.Querier, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, sql, append([]any{time.Now()}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("mark documents for reindex: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	// 【必须是空切片而不是 nil】调用方会把它传给批量入队；nil 切片的
	// 语义是"没有文档"，而空切片也一样——但返回 nil 时如果哪一层做了
	// len() 之外的判断（比如 JSON 序列化成 null），行为会不一样。
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan document id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents for reindex: %w", err)
	}
	return out, nil
}

// documentsSelectCols 是三个列表查询共用的列（含下面的游标版本），
// 免得列顺序在几处各写一份、改一处漏一处。
const documentsSelectCols = `SELECT d.id, d.knowledge_base_id, d.filename, d.storage_key,
	        d.status, d.byte_size, d.created_at, d.updated_at,
	        count(c.id)
	 FROM documents d
	 LEFT JOIN document_chunks c ON c.document_id = d.id`

// listDocumentsWithCursorSQL 用行值比较做 keyset 分页。
//
// 【为什么是 (created_at, id) < ($2, $3) 而不是两个 AND】行值比较是
// Postgres 里表达「按复合键取更小的一批」的原生写法，而且能和
// documents_kb_created_id_idx 的列顺序 + 方向逐字对上——写成
// `created_at < $2 OR (created_at = $2 AND id < $3)` 语义相同，但规划器
// 不一定能把它推成一次索引范围扫描。
//
// 【方向必须和索引一致】索引是 (knowledge_base_id, created_at DESC, id DESC)，
// ORDER BY 也是 DESC, DESC。不一致的话 Postgres 会退化成「索引扫 + Sort」，
// 分页的意义就没了。
// 【加了分块数聚合之后，列名一律带 d. 前缀、并在末尾 GROUP BY d.id】
// documents 和 document_chunks 都有 id 与 created_at 两列，不带前缀的
// `WHERE knowledge_base_id = ...` 会因为 document_chunks 没有那一列而
// 直接报错（42703），但 `ORDER BY created_at` 不会——它会变成一个有歧义的
// 引用，只在某些写法下才报错。全部带前缀是唯一不需要逐条推敲的写法。
//
// 按 d.id 分组是合法的：它是 documents 的主键，Postgres 允许在选择同表
// 其它列时只按主键分组（函数依赖）。这也正是 LEFT JOIN + count 的常规写法。
const listDocumentsWithCursorSQL = documentsSelectCols + `
	 WHERE d.knowledge_base_id = $1 AND (d.created_at, d.id) < ($2, $3)
	 GROUP BY d.id
	 ORDER BY d.created_at DESC, d.id DESC
	 LIMIT $4`

const listDocumentsSQL = documentsSelectCols + `
	 WHERE d.knowledge_base_id = $1
	 GROUP BY d.id
	 ORDER BY d.created_at DESC, d.id DESC
	 LIMIT $2`

func (r *PgDocRepo) ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID, cur *platform.ListCursor, limit int) ([]*Document, bool, error) {
	// 【为什么取 limit+1 条】多出来的那条不返回，只用来判断"还有没有下一页"。
	// 先 COUNT(*) 再取一页要在同一张表上扫两遍，而且两次查询之间还会插入新行；
	// "总是返回游标、让客户端靠空页停"会让"加载更多"永远亮着、点了没反应。
	// limit+1 让 hasMore 在**同一次查询的同一个快照**里成为事实。
	sql := listDocumentsSQL
	args := []any{kbID}

	if cur != nil {
		// 游标里的排序键是编码时写进去的时间戳字符串；这里把它还原成
		// time.Time 交给 pgx。解不出来说明这个游标不是我们发的。
		ts, err := time.Parse(time.RFC3339Nano, cur.SortKey)
		if err != nil {
			return nil, false, fmt.Errorf("cursor sort key %q is not a timestamp: %w", cur.SortKey, platform.ErrInvalid)
		}
		id, err := uuid.Parse(cur.Tiebreak)
		if err != nil {
			return nil, false, fmt.Errorf("cursor tiebreak %q is not a uuid: %w", cur.Tiebreak, platform.ErrInvalid)
		}
		sql = listDocumentsWithCursorSQL
		args = append(args, ts, id)
	}
	args = append(args, limit+1)

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list documents of knowledge base %s: %w", kbID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	out := make([]*Document, 0, limit)
	for rows.Next() {
		d := &Document{}
		var status string
		if err := rows.Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.StorageKey, &status,
			&d.ByteSize, &d.CreatedAt, &d.UpdatedAt, &d.ChunkCount); err != nil {
			return nil, false, fmt.Errorf("scan document: %w", err)
		}
		d.Status = Status(status)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate documents: %w", err)
	}

	// 【切片的位置】结果是 DESC 序，多取的那条落在**尾部**，所以先切再返回。
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (r *PgDocRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) (string, error) {
	var storageKey string
	err := q.QueryRow(ctx,
		`DELETE FROM documents WHERE id = $1 RETURNING storage_key`, id,
	).Scan(&storageKey)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("document %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("delete document %s: %w", id, platform.WrapPgErr(err))
	}
	return storageKey, nil
}

func (r *PgDocRepo) DeleteByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx,
		`DELETE FROM documents WHERE knowledge_base_id = $1 RETURNING storage_key`, kbID)
	if err != nil {
		return nil, fmt.Errorf("delete documents of knowledge base %s: %w", kbID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan deleted storage key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deleted documents: %w", err)
	}
	return keys, nil
}

func (r *PgDocRepo) ExistingStorageKeys(ctx context.Context, q platform.Querier, keys []string) (map[string]bool, error) {
	// 空输入直接返回空结果，不发一条 SQL——ANY('{}') 在 PostgreSQL 里
	// 语法上没问题，但没必要为了零个候选走一次网络往返。
	if len(keys) == 0 {
		return map[string]bool{}, nil
	}

	rows, err := q.Query(ctx,
		`SELECT storage_key FROM documents WHERE storage_key = ANY($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("query existing storage keys: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	out := make(map[string]bool, len(keys))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan storage key: %w", err)
		}
		out[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage keys: %w", err)
	}
	return out, nil
}
