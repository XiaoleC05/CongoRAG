package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ════════════════════════════════════════════════════════════════
// 就绪探针（issue #41）
//
// 在 #41 之前 /healthz 无条件 200，不看数据库也不看迁移版本——一旦 api 进
// 容器，拿它做健康检查会得到"永远健康"：编排器不重启它，也不把流量摘走。
// ════════════════════════════════════════════════════════════════

// stubPinger 按注入的行为回答 Ping。
type stubPinger struct {
	err error
	// blockUntilCancel 为真时一直等到 ctx 被取消才返回——用来验证探针
	// 真的给 Ping 设了超时，而不用真的 sleep 两秒。
	blockUntilCancel bool
	// sawDeadline 记录调用时 ctx 有没有截止时间。
	sawDeadline bool
	// calls 记调用次数，用来确认 /healthz 根本不碰它。
	calls int
}

func (p *stubPinger) Ping(ctx context.Context) error {
	p.calls++
	_, p.sawDeadline = ctx.Deadline()
	if p.blockUntilCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.err
}

// newProbeRouter 组一个带指定 pinger 的最小路由。
func newProbeRouter(p *stubPinger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     p,
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

func getPath(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestReadyz_DatabaseUpReturns200(t *testing.T) {
	p := &stubPinger{}
	w := getPath(newProbeRouter(p), "/readyz")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
	assert.True(t, p.sawDeadline, "探测必须自带超时——连接串没写 connect_timeout 时 pgx 只认 ctx")
}

// 数据库不可用 → 503 + problem+json，且 detail 必须是固定文案而不是底层
// Go 错误的原文。
//
// 【这条同时钉住"为什么不走 s.fail"】fail 的 5xx 分支会把 detail 换成
// "服务内部错误"，那恰恰丢掉了探针最要说清的信息（是数据库）。
func TestReadyz_DatabaseDownReturns503Problem(t *testing.T) {
	p := &stubPinger{err: errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")}
	w := getPath(newProbeRouter(p), "/readyz")

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Equal(t, problemContentType, w.Header().Get("Content-Type"))

	var got Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "unavailable", got.Type)
	assert.Equal(t, http.StatusServiceUnavailable, got.Status)
	require.NotNil(t, got.Detail)
	assert.Equal(t, "数据库连接不可用", *got.Detail)
	assert.NotContains(t, *got.Detail, "dial tcp",
		"不能把底层 Go 错误的原文透给调用方——那既不可行动、也泄漏内部结构")
}

// 依赖挂住时探针必须自己有上限，而不是跟着一起挂。
func TestReadyz_ProbeIsBoundedAgainstAHangingDependency(t *testing.T) {
	p := &stubPinger{blockUntilCancel: true}

	start := time.Now()
	w := getPath(newProbeRouter(p), "/readyz")
	elapsed := time.Since(start)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.True(t, p.sawDeadline, "Ping 收到的 ctx 必须带截止时间")
	assert.Less(t, elapsed, 5*time.Second, "探针不该等到测试超时才发现依赖挂了")
}

// 【这条是整个 issue 的回归守卫】存活与就绪不能塌成一个：
// 数据库挂了，/healthz 仍然必须是 200——否则编排器会把"数据库抖动"
// 判成"进程死了"，反复重启实例。
func TestHealthz_StaysGreenWhenDatabaseIsDown(t *testing.T) {
	p := &stubPinger{err: errors.New("database is gone")}
	r := newProbeRouter(p)

	w := getPath(r, "/healthz")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
	assert.Zero(t, p.calls, "存活探针根本不该碰数据库")
}

// 契约必须声明 503——漏了不会报错，只会让按声明状态集写代码的调用方
// （比如交付期的编排器模板）少处理一个分支。
func TestContractDeclaresReadyzResponses(t *testing.T) {
	codes := contractOperationResponses(t, "readyz")
	require.NotEmpty(t, codes, "契约里找不到 operationId readyz 的 responses")

	assert.True(t, codes["200"], "readyz 必须声明 200")
	assert.True(t, codes["503"], "readyz 必须声明 503——不声明调用方就不知道要处理它")
}

// 带尾斜杠的 /readyz/ 不能被 SPA 兜底接走（那是 200 + index.html，
// 和"端点拼错了应有 404 Problem"不一致）。
func TestSPA_ReadyzTrailingSlashIsProblemNotHTML(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	w := doWithHeader(r, http.MethodGet, "/readyz/")

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "not_found")
	assert.NotContains(t, w.Body.String(), "<!doctype html>")
}
