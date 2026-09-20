package platform

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier 是"能执行 SQL 的东西"。
//
// *pgxpool.Pool（自动提交）和 pgx.Tx（在事务里）都满足它。
// 所有 repo 方法的第一个参数都是它——传 pool 是自动提交，传 tx 是参与事务。
// 事务的开关由 usecase 决定，repo 不自己开事务。
//
// 不用 pgx.Tx 是因为跨模块的 port（ChunkIndexer / Enqueuer / FileCleaner）
// 一旦在签名里出现 pgx.Tx，实现方的驱动类型就泄漏进了消费方的 usecase.go。
// Querier 是同样的能力，但不绑定具体驱动。
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxManager 让 usecase 圈定原子边界。
//
// 用法：
//
//	u.txm.InTx(ctx, func(q platform.Querier) error {
//	    u.repoA.Insert(ctx, q, a)
//	    u.repoB.Update(ctx, q, b)
//	    return nil
//	})
//
// fn 返回 error 就回滚，返回 nil 就提交。fn 里不要做 I/O 之外的慢操作——
// 事务开着的时候占用连接，也别在里面调 LLM。
type TxManager interface {
	InTx(ctx context.Context, fn func(q Querier) error) error
}

// 编译期断言：两个具体类型都必须满足 Querier。
// 放在非测试文件里，任何人破坏了这层适配，整个包编译不过。
var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = (pgx.Tx)(nil)
)

type txManager struct {
	pool *pgxpool.Pool
}

// NewTxManager 返回基于 pgxpool 的事务管理器。
func NewTxManager(pool *pgxpool.Pool) TxManager {
	return &txManager{pool: pool}
}

func (m *txManager) InTx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// fn 出错或 panic 都回滚。回滚失败不覆盖原始错误——原始错误更有价值。
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
