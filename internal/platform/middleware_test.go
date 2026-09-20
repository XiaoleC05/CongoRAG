package platform

import (
	"net/http"
	"net/http/httptest"
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
