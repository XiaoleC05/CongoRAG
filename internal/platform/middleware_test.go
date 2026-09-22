package platform

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OriginCheck 是安全边界：放宽了不会报错，只是服务对局域网敞开。
// 所以这里把"该放行"和"该拒绝"两组都钉死。

func newMiddlewareRouter(h ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(h...)
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return r
}

// 发一个请求。host 为空表示用 httptest 的默认值。
func doReq(r *gin.Engine, host, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ────────────────────────────────────────────────────────────────
// OriginCheck：Host 检查（防 DNS rebinding）
// ────────────────────────────────────────────────────────────────

func TestOriginCheck_AllowsLoopbackHosts(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	// IPv6 的四种写法都要列全：带端口的 "[::1]:3210" 能过，掩盖了
	// "::1" / "[::1]" 被末位冒号截断后永不匹配这件事，所以无端口的形式
	// 必须单独钉住（Host 不写端口本来就是合法的，scheme 默认端口就是它）。
	for _, host := range []string{
		"localhost:3210",
		"127.0.0.1:3210",
		"localhost",
		"127.0.0.1",
		"[::1]:3210",
		"[::1]:80",
		"[::1]",
		"::1",
	} {
		w := doReq(r, host, "")
		assert.Equal(t, http.StatusOK, w.Code, "Host: %s 该放行", host)
	}
}

// DNS rebinding：攻击者把一个自己控制的域名解析到 127.0.0.1，
// 用户浏览器访问那个域名时，请求会真的打到本机服务上。
// 拦点是 Host 头——它带的是用户输入的域名，不是解析后的 IP。
func TestOriginCheck_RejectsNonLoopbackHosts(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	for _, host := range []string{
		"evil.com",                // DNS rebinding
		"evil.com:3210",           //
		"192.168.1.10:3210",       // 局域网 IP，方案 §1 明确不允许
		"10.0.0.5:3210",           //
		"localhost.evil.com:3210", // 前缀像 localhost，但不是
		"127.0.0.1.evil.com:3210", //
		"notlocalhost:3210",       //
		"[2001:db8::1]:3210",      // 是 IPv6 但不是回环——不能因为"认 IPv6"就放行
		"2001:db8::1",             //
	} {
		w := doReq(r, host, "")
		assert.Equal(t, http.StatusForbidden, w.Code, "Host: %s 该拒绝", host)
	}
}

// ────────────────────────────────────────────────────────────────
// OriginCheck：Origin 检查（防跨站）
// ────────────────────────────────────────────────────────────────

func TestOriginCheck_AllowsLoopbackOrigins(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	for _, origin := range []string{
		"http://localhost:5173", // Vite dev server
		"http://127.0.0.1:3210", // 内嵌前端
		"https://localhost:5173",
		"http://localhost",
		"http://[::1]",      // 无端口的 IPv6 回环 Origin
		"http://[::1]:3210", //
	} {
		w := doReq(r, "localhost:3210", origin)
		assert.Equal(t, http.StatusOK, w.Code, "Origin: %s 该放行", origin)
	}
}

func TestOriginCheck_RejectsCrossSiteOrigins(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	for _, origin := range []string{
		"http://evil.com",
		"https://evil.com",
		"http://localhost.evil.com",
		"http://192.168.1.10:5173",
		"file://", // 本地 HTML 文件发起的请求
		"null",    // 沙箱 iframe 的 Origin
	} {
		w := doReq(r, "localhost:3210", origin)
		assert.Equal(t, http.StatusForbidden, w.Code, "Origin: %s 该拒绝", origin)
	}
}

// curl / 脚本不带 Origin 头，只受 Host 检查约束。
// 把"没有 Origin"当成拒绝的话，本地脚本和健康检查全挂。
func TestOriginCheck_MissingOriginIsAllowed(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	w := doReq(r, "localhost:3210", "")

	assert.Equal(t, http.StatusOK, w.Code)
}

// Host 不合法时【不该再看 Origin】——先拒绝，理由是 forbidden_host。
// 两个检查的顺序决定了报错信息指向哪个原因。
func TestOriginCheck_HostCheckedBeforeOrigin(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	w := doReq(r, "evil.com", "http://localhost:5173")

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "forbidden_host")
}

// 被拒的请求不能到达 handler。只看状态码测不出来——
// 中间件忘了 return 的话，handler 仍会执行（可能已经改了数据），
// 只是响应体被覆盖掉。
func TestOriginCheck_RejectedRequestDoesNotReachHandler(t *testing.T) {
	reached := false
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(OriginCheck(&Config{}))
	r.GET("/ping", func(c *gin.Context) {
		reached = true
		c.String(http.StatusOK, "pong")
	})

	doReq(r, "evil.com", "")

	assert.False(t, reached, "被拒的请求不该执行 handler")
}

// ────────────────────────────────────────────────────────────────
// 中间件产出的错误体也必须是 Problem（issue #112）
// ────────────────────────────────────────────────────────────────

// 上面那条只断言了 body 里含 forbidden_host，断言不到形状。而老实现用的是
// AbortWithStatusJSON：媒体类型 application/json、body 里没有 status 字段——
// 契约（Problem 的 required: [type, status]、媒体类型 application/problem+json）
// 两样都要，前端按 type 分支的代码在中间件这条路径上会失效。
func TestOriginCheck_RejectionBodyIsProblem(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	w := doReq(r, "evil.com", "")

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, ProblemContentType, w.Header().Get("Content-Type"))

	var got Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "forbidden_host", got.Type)
	assert.Equal(t, http.StatusForbidden, got.Status, "status 是契约里的必填字段")
	assert.NotEmpty(t, got.Title)
}

// 每个错误响应都要带 no-store：403 会被浏览器缓存吗不重要，
// 重要的是这条策略只有一处实现——哪天它只在 handler 那条路径上生效，
// 中间件拒掉的请求就会留下一个可缓存的 403。
func TestOriginCheck_RejectionIsNotCacheable(t *testing.T) {
	r := newMiddlewareRouter(OriginCheck(&Config{}))

	w := doReq(r, "evil.com", "")

	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// ────────────────────────────────────────────────────────────────
// Recovery：响应已经写出去之后 panic（issue #111）
// ────────────────────────────────────────────────────────────────

// newRecoveryRouter 挂上 Recovery，处理 GET /ping。
func newRecoveryRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))))
	r.GET("/ping", handler)
	return r
}

// 什么都没写出去时，仍然是 500 + Problem。
func TestRecovery_ResponseNotStarted_WritesProblem(t *testing.T) {
	r := newRecoveryRouter(func(c *gin.Context) { panic("boom") })

	w := doReq(r, "localhost:3210", "")

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, ProblemContentType, w.Header().Get("Content-Type"))

	var got Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "internal_error", got.Type)
	assert.Equal(t, http.StatusInternalServerError, got.Status)
	assert.Equal(t, InternalErrorDetail, got.Detail)
}

// SSE 已经开始时，panic 只能补一条 error 帧。
//
// 【为什么这条必须存在】老实现无条件 AbortWithStatusJSON：那段 JSON 会被
// 【追加进 text/event-stream 的正文】——它没有 \n\n 收尾，不是合法 SSE 帧，
// 客户端解析器会把残帧丢掉；也没有 error / done 帧收场。客户端的表现是
// 流静默结束、onError 不触发（它只在抛异常时报错），用户永远等不到回复
// 也不知道出了什么事。
func TestRecovery_SSEAlreadyStarted_WritesErrorFrameNotJSON(t *testing.T) {
	r := newRecoveryRouter(func(c *gin.Context) {
		// 照 sse.go 的 newSSESink：先设头写状态码，再发一帧。
		c.Header("Content-Type", "text/event-stream")
		c.Status(http.StatusOK)
		fmt.Fprint(c.Writer, "event: token\ndata: {\"text\":\"hi\"}\n\n")
		c.Writer.Flush()
		panic("boom")
	})

	w := doReq(r, "localhost:3210", "")

	// 状态码改不了了（响应头早就提交），客户端拿到的是 200 + 一条 error 帧。
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	assert.NotContains(t, body, `"status":500`,
		"不能把 Problem 的 JSON 塞进事件流：%q", body)
	assert.True(t, strings.HasSuffix(body, "\n\n"), "最后必须是一个完整帧：%q", body)

	frame := lastFrame(body)
	assert.True(t, strings.HasPrefix(frame, "event: error\n"), "最后一帧要是 error：%q", frame)
	assert.Contains(t, frame, `"type":"internal_error"`)
	assert.NotContains(t, frame, "id: ",
		"这帧没有对应的持久化事件，编不出真实 event_id（写 0 会把续传游标退回起点）")
}

// 响应写了一半、但不是 SSE：什么都不要再写。
// 半截 JSON 比没有 JSON 更糟——客户端会把它当成一个（必然解析失败的）完整
// 响应，而不是一次短读。
func TestRecovery_NonSSEResponseAlreadyWritten_WritesNothing(t *testing.T) {
	r := newRecoveryRouter(func(c *gin.Context) {
		c.Data(http.StatusOK, "application/octet-stream", []byte("partial"))
		panic("boom")
	})

	w := doReq(r, "localhost:3210", "")

	assert.Equal(t, "partial", w.Body.String(),
		"已经提交的正文后面不该再追加任何东西")
}

// lastFrame 取响应体里最后一帧的原文。帧之间以空行分隔，见
// docs/sse-protocol.md；完整的帧解析器在 web/src/lib/streamChat.ts。
func lastFrame(body string) string {
	frames := strings.Split(strings.TrimRight(body, "\n"), "\n\n")
	return frames[len(frames)-1]
}

// ────────────────────────────────────────────────────────────────
// RequestID
// ────────────────────────────────────────────────────────────────

func TestRequestID_SetsHeaderAndContext(t *testing.T) {
	var fromCtx string
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/ping", func(c *gin.Context) {
		fromCtx = RequestIDFrom(c.Request.Context())
		c.String(http.StatusOK, "pong")
	})

	w := doReq(r, "", "")

	header := w.Header().Get("X-Request-ID")
	assert.NotEmpty(t, header)
	assert.Equal(t, header, fromCtx, "响应头和 context 里必须是同一个值")
}

func TestRequestID_IsUniquePerRequest(t *testing.T) {
	r := newMiddlewareRouter(RequestID())

	first := doReq(r, "", "").Header().Get("X-Request-ID")
	second := doReq(r, "", "").Header().Get("X-Request-ID")

	assert.NotEqual(t, first, second)
}

// 没有经过中间件的 context 取不到 id，要返回空串而不是 panic。
// 后台任务、测试里构造的 context 都是这种情况。
func TestRequestIDFrom_EmptyContext(t *testing.T) {
	assert.Empty(t, RequestIDFrom(t.Context()))
}
