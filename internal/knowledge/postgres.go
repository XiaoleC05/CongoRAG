// KBRepo 的 PostgreSQL 实现。所有 SQL 都收敛在这个文件里。
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

// 编译期断言：签名与接口不符时这里编译失败。
var _ KBRepo = (*PgRepo)(nil)

// PgRepo 是无状态的。
//
// 连接不由它持有，而是每次调用时通过 q 参数传入——这样事务边界由调用方控制。
// 所以这里没有字段，构造函数也不收参数。
type PgRepo struct{}

func NewPgRepo() *PgRepo {
	return &PgRepo{}
}

// ────────────────────────────────────────────────────────────────
// Insert
// ────────────────────────────────────────────────────────────────

func (r *PgRepo) Insert(ctx context.Context, q platform.Querier, kb *KB) error {
	// Exec 返回 (影响行数, 错误)。插入不需要行数，用 _ 丢掉。
	_, err := q.Exec(ctx,
		`INSERT INTO knowledge_bases (id, name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4)`,
		kb.ID, kb.Name, kb.CreatedAt, kb.UpdatedAt,
	)
	if err != nil {
		// %w 保留原始错误，上层才能用 errors.Is 判断。
		// WrapPgErr 把 SQLSTATE 翻译成项目的 sentinel
		//（唯一约束冲突 23505 → platform.ErrDuplicateKey）。
		return fmt.Errorf("insert knowledge base %s: %w", kb.ID, platform.WrapPgErr(err))
	}
	return nil
}

// ────────────────────────────────────────────────────────────────
// List
// ────────────────────────────────────────────────────────────────

func (r *PgRepo) List(ctx context.Context, q platform.Querier) ([]*KB, error) {
	rows, err := q.Query(ctx,
		`SELECT id, name, created_at, updated_at
		 FROM knowledge_bases
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", platform.WrapPgErr(err))
	}

	// 结果集持有一条数据库连接，不关的话连接不会还给连接池。
	// 用 defer 是因为下面的循环里有多个 return 出口。
	defer rows.Close()

	var kbs []*KB

	for rows.Next() {
		// 每轮必须新建。写在循环外面的话所有元素会指向同一个对象。
		kb := &KB{}

		// Scan 按列顺序填充，必须传指针，否则填进去的是副本。
		if err := rows.Scan(&kb.ID, &kb.Name, &kb.CreatedAt, &kb.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan knowledge base: %w", err)
		}

		kbs = append(kbs, kb)
	}

	// Next() 返回 false 有两种原因：读完了，或者中途出错。
	// 只有 Err() 能区分。漏掉这句，中途的错误会被静默吞掉，
	// 得到一个"看起来成功但少了几行"的结果。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge bases: %w", err)
	}

	return kbs, nil
}

// ────────────────────────────────────────────────────────────────
// ByID
// ────────────────────────────────────────────────────────────────

func (r *PgRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*KB, error) {
	kb := &KB{}

	// QueryRow 不返回结果集，错误只能从 Scan 拿到，所以连起来写。
	err := q.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at
		 FROM knowledge_bases
		 WHERE id = $1`, id,
	).Scan(&kb.ID, &kb.Name, &kb.CreatedAt, &kb.UpdatedAt)

	// 把 pgx 的"没有这一行"翻译成项目自己的"找不到"，
	// 上层就不需要认识 pgx 的内部错误。
	//
	// 顺序不能反：这个判断必须在下面 err != nil 之前，
	// 否则 ErrNoRows 会被当成普通错误，最终返回 500 而不是 404。
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("knowledge base %s: %w", id, platform.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get knowledge base %s: %w", id, platform.WrapPgErr(err))
	}

	return kb, nil
}

// ────────────────────────────────────────────────────────────────
// Rename
// ────────────────────────────────────────────────────────────────

func (r *PgRepo) Rename(ctx context.Context, q platform.Querier, id uuid.UUID, newName string, updatedAt time.Time) error {
	// 接住 CommandTag，它记录这条语句影响了几行。
	//
	// updated_at 用 $3 而不是 SQL 的 now()，让所有时间戳来自同一个时钟。
	// 原因见 usecase.go 里 Rename 的注释。
	tag, err := q.Exec(ctx,
		`UPDATE knowledge_bases
		 SET name = $2, updated_at = $3
		 WHERE id = $1`,
		id, newName, updatedAt)
	if err != nil {
		return fmt.Errorf("rename knowledge base %s: %w", id, platform.WrapPgErr(err))
	}

	// UPDATE 目标不存在时影响 0 行，但不报错。
	// 不判断的话接口会返回"成功"，而数据库里什么都没变。
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("knowledge base %s: %w", id, platform.ErrNotFound)
	}

	return nil
}

// ────────────────────────────────────────────────────────────────
// Delete
// ────────────────────────────────────────────────────────────────

func (r *PgRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	// 只删这一行。它下面的 documents 和 document_chunks
	// 由迁移里的 ON DELETE CASCADE 自动删除。
	_, err := q.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete knowledge base %s: %w", id, platform.WrapPgErr(err))
	}

	// 这里不判断 RowsAffected，与 Rename 不同：
	// DELETE 做成幂等的——删一个已经不存在的东西，结果和删成功一样。
	// 要和 Rename 保持一致（不存在就报 ErrNotFound）也可以，是个取舍。
	return nil
}
