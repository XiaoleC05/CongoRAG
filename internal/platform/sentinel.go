package platform

import "errors"

// sentinel 错误——全项目只在这里声明一次，各包一律用 %w 包装，不各自重声明。
//
// handler 是唯一把 error 翻译成 HTTP 的地方（apps/api/internal/api/problem.go），
// 它靠 errors.Is 认这些值。新增一个 sentinel 就要同步更新那张映射表。
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrInvalid      = errors.New("invalid argument")
	ErrDuplicateKey = errors.New("duplicate key")
	ErrForeignKey   = errors.New("foreign key violation")
	ErrUpstream     = errors.New("upstream provider error")

	// ErrIdempotentHit 是幂等键冲突。它不是错误路径：
	// usecase 捕获后改为"返回已创建资源"，所以不出现在 classify 的映射表里。
	ErrIdempotentHit = errors.New("idempotency key hit")
)
