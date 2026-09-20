package platform

import (
	"context"
	"log/slog"
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

// isLoopbackHost 判断 Host 头（形如 "127.0.0.1:3210"）是不是回环地址。
func isLoopbackHost(host string) bool {
	name := host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		name = host[:i]
	}
	switch name {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
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
