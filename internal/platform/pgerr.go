package platform

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// WrapPgErr 把 PostgreSQL 的驱动错误翻译成项目的 sentinel 错误。
//
// 这是唯一认识 SQLSTATE 的地方。各模块的 postgres.go 里所有 Exec/Query
// 的错误都要过一遍它，上层才能用 errors.Is 判断。
//
// 23505 按约束名分流。**三个约束、三种含义**，只看 SQLSTATE 一律映射成
// 409 的话，两种"不是错误"的路径都会被说成冲突：
//
//	幂等键重复        → 返回已创建资源（不是错误）
//	tool_effect_log   → 这个副作用已经发生了（resume 要据此跳过重放，不是错误）
//	其余唯一约束       → 真正的业务冲突（409）
//
// 每加一个"23505 但不是错误"的约束，都要在这里加一条分支，
// 否则它会静默地走成 409 或 500（issue #58）。
func WrapPgErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		if pgErr.ConstraintName == constraintIdempotencyKey {
			return fmt.Errorf("%w: %s", ErrIdempotentHit, pgErr.ConstraintName)
		}
		if pgErr.ConstraintName == constraintToolEffectLog {
			return fmt.Errorf("%w: %s", ErrToolEffectApplied, pgErr.ConstraintName)
		}
		return fmt.Errorf("%w: %s", ErrDuplicateKey, pgErr.ConstraintName)

	case "23503": // foreign_key_violation
		return fmt.Errorf("%w: %s", ErrForeignKey, pgErr.ConstraintName)

	case "23502": // not_null_violation
		// 约束违反同样来自请求数据（比如可选字段省略时绑了个 nil 切片），
		// 归类成 invalid_argument 才能让客户端看出是自己这一侧的输入问题；
		// 不映射的话它会落到 classify() 的 default，变成一个连字段名都
		// 问不出来的通用 500（issue #18）。
		return fmt.Errorf("%w: null value in column %s", ErrInvalid, pgErr.ColumnName)
	}

	return err
}

// 约束名要和 migrations 里实际生成的保持一致。
// PRIMARY KEY (endpoint, idempotency_key) 默认生成 idempotency_keys_pkey——
// 改表名或改成命名约束时，这里必须同步改。
const constraintIdempotencyKey = "idempotency_keys_pkey"

// constraintToolEffectLog 是 migrations/0010_tool_effect_log.up.sql 里
// 显式命名的那个 UNIQUE 约束。**没有用默认名**：Postgres 给
// UNIQUE (step_id, effect_key) 生成的默认名是 tool_effect_log_step_id_effect_key_key，
// 而这里的分流逻辑依赖这个名字，显式命名比依赖生成规则更稳。
const constraintToolEffectLog = "tool_effect_log_step_effect_unique"
