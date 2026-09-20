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

func (r *PgDocRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*Document, error) {
	d := &Document{}
	var status string
	err := q.QueryRow(ctx,
		`SELECT id, knowledge_base_id, filename, storage_key, status, byte_size, created_at, updated_at
		 FROM documents
		 WHERE id = $1`, id,
	).Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.StorageKey, &status, &d.ByteSize, &d.CreatedAt, &d.UpdatedAt)

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

func (r *PgDocRepo) ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]*Document, error) {
	rows, err := q.Query(ctx,
		`SELECT id, knowledge_base_id, filename, storage_key, status, byte_size, created_at, updated_at
		 FROM documents
		 WHERE knowledge_base_id = $1
		 ORDER BY created_at DESC`, kbID)
	if err != nil {
		return nil, fmt.Errorf("list documents of knowledge base %s: %w", kbID, platform.WrapPgErr(err))
	}
	defer rows.Close()

	var out []*Document
	for rows.Next() {
		d := &Document{}
		var status string
		if err := rows.Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.StorageKey, &status, &d.ByteSize, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		d.Status = Status(status)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return out, nil
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
