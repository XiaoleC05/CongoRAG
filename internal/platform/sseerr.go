package platform

import "errors"

// SSEErrorType 把 error 映射成 SSE error 事件的 type 字段。
//
// 【为什么在 platform】docs/sse-protocol.md 规定 SSE 错误的 type 和 REST
// 的 Problem.type 是同一套枚举，那就只能有一处定义——聊天的流式回答
// （conversation）和 Agent 的运行流（agent）都要用它，各自维护一份迟早
// 会漂移。放在这里是因为 sentinel 全项目只在 sentinel.go 声明一次，
// 而这个函数认的就是这些 sentinel。
//
// 【context_overflow 不在这里】它属于 ctxmgr 的私有语义，而本包不能
// import ctxmgr（llm 依赖本包，ctxmgr 依赖 llm，反向 import 会成环），
// 所以那一档由 conversation 在调用本函数之前先判，其余档位共用。
func SSEErrorType(err error) string {
	switch {
	case errors.Is(err, ErrInvalid):
		return "invalid_argument"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrUpstream):
		return "upstream_llm_error"
	default:
		return "internal_error"
	}
}
