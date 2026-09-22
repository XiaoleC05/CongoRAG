package platform

import "errors"

// sentinel 错误——全项目只在这里声明一次，各包一律用 %w 包装，不各自重声明。
//
// 本包的 Classify 是唯一那张"错误 → 客户端看到什么"的表（HTTP 状态码、
// Problem.type、SSE error 帧的 type 都从它取值），它靠 errors.Is 认这些值。
// 新增一个 sentinel 就要同步更新那张表——漏了不会编译失败，只会静默落进
// internal_error。
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

	// ErrToolEffectApplied 是 tool_effect_log 的唯一约束冲突（issue #63）。
	//
	// 【它和 ErrDuplicateKey 的区别不是"严重程度"，是"接下来做什么"】
	// 业务唯一约束冲突意味着"这次请求不该成功"（409）；而这个冲突意味着
	// "这个副作用已经发生过了"——恢复路径要据此**跳过重放**，而不是报错。
	// 两者 SQLSTATE 都是 23505，只能按约束名分流（见 WrapPgErr）。
	//
	// 【它同时是 resume 正确性的判据】文档 §9.5 写明：看到这个冲突 =
	// 工具被重复执行了 = resume 没生效。所以它必须能被上层用 errors.Is
	// 认出来——当成普通 ErrDuplicateKey 的话，那个判据就消失了。
	ErrToolEffectApplied = errors.New("tool effect already applied")

	// ErrStateSchemaVersionMismatch 是 checkpoint 快照结构版本不匹配
	// （issue #65）。它不是"数据坏了"，是"这份快照是旧代码写的，
	// 新代码读不懂它的结构"——项目文档 §9.4 要求明确拒绝恢复并提示
	// 重新发起，而不是硬着头皮反序列化出一个半截状态。
	ErrStateSchemaVersionMismatch = errors.New("state schema version mismatch")

	// ErrReplayUnsafe 是"这一步的副作用不允许被平台自动重放"（issue #61）。
	//
	// 【它和 ErrToolEffectApplied 的分工】那个说的是"已经确认跑过了"，
	// 这个说的是"我们不知道跑没跑，但这类工具不该由平台替你决定重跑"——
	// 判据来自 ToolMetadata 的 side_effect_level / retry_policy。
	// 两者的用户下一步动作不同：前者只能重新发起一次运行，
	// 后者还多一条"把这一步的执行改成幂等的"。
	ErrReplayUnsafe = errors.New("replay unsafe for this step")
)
