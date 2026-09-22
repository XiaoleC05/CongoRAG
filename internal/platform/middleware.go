package platform

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ContextKey 是往 context 里塞东西时用的键。
//
// 键必须用包内自定义类型，不能用 string 裸值，
// 否则不同包用同一个字符串会互相覆盖。
type ContextKey int

const (
	// CtxRequestID 是本次请求的追踪 ID。
	CtxRequestID ContextKey = iota

	// CtxAgentRunID 是一次 Agent 运行的追踪 ID（issue #71）。
	//
	// 【为什么需要第二个追踪 ID】request_id 的作用域是**一次 HTTP 请求**，
	// 而一次 Agent run 会横跨 API 进程（发起）与多轮工具/模型调用。
	// 排查「这次 run 为什么慢/为什么失败」时，缺的正是把散落在多处的
	// 日志行聚成一条时间线的那个字段——尤其在这个 run 被 KILL 掉、
	// 请求 ctx 早就没了之后（M4-C 的崩溃现场）。
	CtxAgentRunID ContextKey = iota
)

// RequestID 给每个请求分配一个 ID，塞进 context 和响应头。
//
// 方案 §2 要求结构化日志里带 request_id。下游任何一层用
// RequestIDFrom(ctx) 就能取到，不用一层层当函数参数传。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := uuid.NewString()
		ctx := context.WithValue(c.Request.Context(), CtxRequestID, id)
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// RequestIDFrom 从 context 里取 request_id，没有就返回空串。
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(CtxRequestID).(string)
	return id
}

// WithAgentRunID 把 run id 塞进 context。
//
// 【谁调用它】agent.Usecase 在 run 行落库之后、执行开始之前调用一次，
// 之后这条运行路径上的每一层（工具调用、终态写入、错误处理）都能用
// AgentRunIDFrom 取到它——和 request_id 由中间件注入是同一个机制，
// 只是注入点是业务层（Agent run 不是 HTTP 请求，没有中间件可挂）。
func WithAgentRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, CtxAgentRunID, runID)
}

// AgentRunIDFrom 从 context 里取 agent_run_id，没有就返回空串。
//
// 【为什么返回空串而不是零值 uuid】调用方要把它直接当 slog 的键值对用，
// 空串在日志里读作「这条日志不属于任何 run」（比如文档处理的 River job），
// 而 uuid.Nil 会读成一个看起来像真 id 的东西。
func AgentRunIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(CtxAgentRunID).(string)
	return id
}

// LogAttrs 返回打日志时该带的追踪字段。
//
// 【为什么做成一个函数，而不是每个调用点写两行】"每条日志都带 request_id
// 和 agent_run_id"这件事靠纪律维持，而纪律会在新增调用点时断掉——断掉
// 不会报错，只会让某一条日志在排查时对不上时间线。抽成一个函数之后，
// 新增调用点只要用了它就不会漏（issue #71）。
//
// 两个字段都缺席时不返回任何 attr，日志行不会出现两个空串。
func LogAttrs(ctx context.Context) []any {
	var attrs []any
	if id := RequestIDFrom(ctx); id != "" {
		attrs = append(attrs, "request_id", id)
	}
	if id := AgentRunIDFrom(ctx); id != "" {
		attrs = append(attrs, "agent_run_id", id)
	}
	return attrs
}

// LoggerFrom 返回一个已经把追踪字段绑好的 logger。
//
// 【为什么不是"每处自己 LogAttrs(ctx)..."】那要求每个调用点都记得展开切片，
// 展开错（比如忘了 `...`）会把整个切片当成一个值打进日志，读起来是一串
// 方括号，而且不报错。绑定一次比每处展开一次更难写错。
func LoggerFrom(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	if attrs := LogAttrs(ctx); len(attrs) > 0 {
		return base.With(attrs...)
	}
	return base
}

// OriginCheck 校验 Origin 和 Host 头，拦住跨站请求和 DNS rebinding。
//
// 方案 §1 的"安全基线"要求。本地单机应用，只允许回环地址访问：
//   - Host 头必须是回环地址（防 DNS rebinding：恶意域名解析到 127.0.0.1 来打你的本地服务）
//   - 浏览器发来的请求（带 Origin 头）：Origin 的 host 也必须是回环地址
//   - 非浏览器客户端（curl 等，不带 Origin 头）：只受 Host 检查约束
//
// 用局域网 IP（如 192.168.x.x）访问会被拒。
// 项目的部署边界是本地单机应用（方案 §1），所以这是刻意的。
func OriginCheck(cfg *Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isLoopbackHost(c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"type":   "forbidden_host",
				"detail": "只允许通过 localhost / 127.0.0.1 访问",
			})
			return
		}

		if origin := c.GetHeader("Origin"); origin != "" && !isLoopbackOrigin(origin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"type":   "forbidden_origin",
				"detail": "跨站请求被拒绝",
			})
			return
		}

		c.Next()
	}
}

// Recovery 捕获 panic，打日志，返回 500。
//
// 用 gin 自带的 CustomRecoveryWithWriter，不自己写 recover——
// 它已经处理好了"连接已断时不要写响应"这类边界。
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, err any) {
		logger.Error("panic recovered",
			"request_id", RequestIDFrom(c.Request.Context()),
			"path", c.Request.URL.Path,
			"panic", err,
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"type":   "internal_error",
			"detail": "服务内部错误",
		})
	})
}

// isLoopbackHost 判断 Host 头（形如 "127.0.0.1:3210"、"[::1]:3210"、
// 不带端口的 "::1" / "[::1]"）是不是回环地址。
//
// 【为什么不能按最后一个冒号截断】IPv6 字面量内部就带冒号：不带端口时
// 那个"最后一位冒号"在地址里，"::1" 会被截成 ":"、"[::1]" 会被截成 "["，
// 于是两个白名单值永远匹配不上，Host: [::1] 的请求被 403 forbidden_host
// 拒掉（带端口的形式反而能过，所以看起来像偶发）。这里改用
// net.SplitHostPort 解析端口：解析失败说明整个 host 就是地址本身（不带
// 端口的 IPv6 正是这种情况），再剥掉方括号交给 net.ParseIP 判定——
// 这样有端口/无端口、带方括号/不带方括号走的是同一条判据。
func isLoopbackHost(host string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	if strings.EqualFold(name, "localhost") {
		return true
	}
	if ip := net.ParseIP(strings.Trim(name, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// isLoopbackOrigin 判断 Origin 头（形如 "http://localhost:5173"）是不是回环地址。
//
// 端口不限——开发期 Vite dev server 在别的端口，生产期前端由 api 内嵌。
func isLoopbackOrigin(origin string) bool {
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		if rest, ok = strings.CutPrefix(origin, "https://"); !ok {
			return false
		}
	}
	return isLoopbackHost(rest)
}
