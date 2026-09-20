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
// 23505 按约束名分流：幂等键的重复不是错误，而是"返回已创建资源"。
// 一律映射成 409 的话，用户重复提交会看到报错而不是既有结果。
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
		return fmt.Errorf("%w: %s", ErrDuplicateKey, pgErr.ConstraintName)

	case "23503": // foreign_key_violation
		return fmt.Errorf("%w: %s", ErrForeignKey, pgErr.ConstraintName)
	}

	return err
}

// 约束名要和 migrations 里实际生成的保持一致。
// PRIMARY KEY (endpoint, idempotency_key) 默认生成 idempotency_keys_pkey——
// 改表名或改成命名约束时，这里必须同步改。
const constraintIdempotencyKey = "idempotency_keys_pkey"
