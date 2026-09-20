package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 内嵌产物在二进制里的形状：最外层是 "web/"。
func fakeWebFS() fs.FS {
	return fstest.MapFS{
		"web/index.html":     {Data: []byte("<!doctype html><title>ConGoRAG</title>")},
		"web/assets/app.js":  {Data: []byte("console.log(1)")},
		"web/assets/app.css": {Data: []byte("body{}")},
		"web/favicon.svg":    {Data: []byte("<svg/>")},
	}
}

func newSPARouter(t *testing.T, webFS fs.FS) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 【和 app.go 保持一致】生产里这一行是关掉的，测试里也必须关，
	// 否则测的不是真实接线。见下面 TestSPA_TrailingSlashOnAPIPathIsProblemNot301。
	r.RedirectTrailingSlash = false
	// 注册一个 API 路由，用来验证它不会被 SPA 兜底抢走
	r.GET("/api/v1/things", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	require.NoError(t, MountSPA(r, webFS))
	return r
}

// ────────────────────────────────────────────────────────────────
// 静态文件
// ────────────────────────────────────────────────────────────────

func TestSPA_ServesRealFiles(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	for _, tc := range []struct{ path, wantBody string }{
		{"/", "ConGoRAG"},
		{"/assets/app.js", "console.log(1)"},
		{"/assets/app.css", "body{}"},
		{"/favicon.svg", "<svg/>"},
	} {
		code, body := do(r, http.MethodGet, tc.path, "")
		assert.Equal(t, http.StatusOK, code, tc.path)
		assert.Contains(t, body, tc.wantBody, tc.path)
	}

	// /index.html 会被 http.FileServer 规范化重定向到 /，这是它自带的行为。
	// 不特殊处理，但记下来免得以后有人当 bug 查。
	assert.Equal(t, http.StatusMovedPermanently, do2(r, http.MethodGet, "/index.html"))
}

// do2 只要状态码。
func do2(r *gin.Engine, method, path string) int {
	code, _ := do(r, method, path, "")
	return code
}

func TestSPA_FallsBackToIndexForUnknownPaths(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	// 前端路由的路径在服务器上没有对应文件，必须返回 index.html，
	// 否则用户在这些页面刷新就是白屏。
	for _, p := range []string{"/knowledge-bases", "/agents/42", "/anything/deep/path"} {
		code, body := do(r, http.MethodGet, p, "")
		assert.Equal(t, http.StatusOK, code, p)
		assert.Contains(t, body, "ConGoRAG", p)
	}
}

// ────────────────────────────────────────────────────────────────
// 边界：这些是审查发现、修掉之后补的回归测试
// ────────────────────────────────────────────────────────────────

func TestSPA_DoesNotListDirectories(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	// /assets/ 是个目录。交给 http.FileServer 的话它会渲染目录列表，
	// 把内嵌产物的文件名全列出来。必须走 fallback 返回 index.html。
	code, body := do(r, http.MethodGet, "/assets/", "")

	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, body, "app.js", "目录内容不该被列出来")
	assert.Contains(t, body, "ConGoRAG")
}

func TestSPA_UnknownAPIPathReturnsProblemNotHTML(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	// 不排除 /api 前缀的话，端点拼错会拿到 200 + HTML——
	// 调用方无法区分"成功"和"没这个接口"，而且是 HTML 不是 JSON，
	// openapi-fetch 会去解析 HTML 并抛语法错误。
	code, body := do(r, http.MethodGet, "/api/v1/typo", "")

	require.Equal(t, http.StatusNotFound, code, body)
	assert.Contains(t, body, "not_found")
	assert.NotContains(t, body, "<!doctype html>")
}

func TestSPA_WrongMethodOnAPIPathIsNotHTML(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	code, body := do(r, http.MethodDelete, "/api/v1/things", "")

	require.Equal(t, http.StatusNotFound, code, body)
	assert.NotContains(t, body, "<!doctype html>")
}

func TestSPA_RegisteredAPIRouteStillWorks(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	code, body := do(r, http.MethodGet, "/api/v1/things", "")

	require.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"ok":true}`, body)
}

// 回归测试：带尾斜杠的 API 路径不能回 301 + HTML。
//
// gin 默认 RedirectTrailingSlash = true，那个 301 由路由层直接发出，
// 【跑在中间件之前】——所以它既没有 X-Request-Id（日志里追不到这次请求），
// 响应体又是 HTML（契约声明这个前缀下只有 JSON / problem+json）。
// app.go 里把它关掉了，这里锁住这个行为。
func TestSPA_TrailingSlashOnAPIPathIsProblemNot301(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	code, body := do(r, http.MethodGet, "/api/v1/things/", "")

	require.Equal(t, http.StatusNotFound, code, body)
	assert.Contains(t, body, "not_found")
	assert.NotContains(t, body, "Moved Permanently", "不该是路由层的 301")
	assert.NotContains(t, body, "<!doctype html>")
}

// 与上一条配对：前端路由带尾斜杠仍然要拿到 index.html。
// 关掉 RedirectTrailingSlash 不能把单页应用的刷新一起弄坏。
func TestSPA_TrailingSlashOnFrontendPathStillServesIndex(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	code, body := do(r, http.MethodGet, "/knowledge-bases/", "")

	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, "ConGoRAG")
}

func TestSPA_MissingBuildGivesInstructionsNotBlankPage(t *testing.T) {
	// 全新克隆时 apps/api/web/ 里只有 .gitkeep，没有 index.html。
	r := newSPARouter(t, fstest.MapFS{"web/.gitkeep": {Data: []byte{}}})

	code, body := do(r, http.MethodGet, "/", "")

	require.Equal(t, http.StatusServiceUnavailable, code, body)
	assert.Contains(t, body, "make build-web", "要告诉人怎么修，而不是给一个白屏")
}

func TestSPA_PathTraversalIsRefused(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	// fs.Sub 会拒绝含 ".." 的路径，落回 index.html
	for _, p := range []string{"/../go.mod", "/../../etc/passwd"} {
		code, body := do(r, http.MethodGet, p, "")
		assert.NotContains(t, body, "module github.com", p)
		assert.Equal(t, http.StatusOK, code, p)
	}
}

func TestSPA_ProblemUsesProblemJSONContentType(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/typo", strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	// 契约里所有错误声明的都是 application/problem+json。
	// gin 的 c.JSON 只会写 application/json，所以这里必须用 c.Data。
	assert.Contains(t, w.Header().Get("Content-Type"), "application/problem+json")
}
