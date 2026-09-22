package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 内嵌产物在二进制里的形状：最外层是 "web/"。
//
// 【文件名照抄真实产物】assets/ 下是 Vite 生成的内容哈希名（哈希一变文件名
// 就变，所以它们可以长期缓存），favicon.svg 来自 web/public/、被原样拷到
// 产物根，名字里没有哈希——缓存判据必须能把这两类分开。
func fakeWebFS() fs.FS {
	return fstest.MapFS{
		"web/index.html":                                    {Data: []byte("<!doctype html><title>ConGoRAG</title>")},
		"web/assets/index-BjyFRYsa.js":                      {Data: []byte("console.log(1)")},
		"web/assets/index-DEB1x6pO.css":                     {Data: []byte("body{}")},
		"web/assets/geist-latin-wght-normal-BgDaEnEv.woff2": {Data: []byte("font-bytes")},
		"web/favicon.svg":                                   {Data: []byte("<svg/>")},
	}
}

// realWebTree 造一棵**真实磁盘上**的目录树，并返回根 FS 与哨兵内容：
//
//	<root>/sentinel.txt                        ← 被服务根【之外】的哨兵
//	<root>/web/index.html
//	<root>/web/assets/index-BjyFRYsa.js
//
// 【为什么不能用 fstest.MapFS 测穿越】MapFS 里根本不存在哨兵文件，
// "它没出现在响应里"因此恒真——断言的否定面没有可失败的对象，
// 把实现换成"直接读仓库根"它照样绿。os.DirFS 背后是真实磁盘，哨兵真的
// 在那儿，绕过路径校验就会把它发出去。
//
// 【为什么根目录是 t.TempDir()，不是仓库根】同一件事不能依赖工作目录：
// 测试从哪个目录跑、跑在谁的机器上，都不该改变结果。
func realWebTree(t *testing.T) (fs.FS, string) {
	t.Helper()
	root := t.TempDir()
	const sentinel = "SENTINEL-MUST-NOT-BE-SERVED"

	require.NoError(t, os.WriteFile(filepath.Join(root, "sentinel.txt"), []byte(sentinel), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "web", "assets"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "web", "index.html"),
		[]byte("<!doctype html><title>ConGoRAG</title>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "web", "assets", "index-BjyFRYsa.js"),
		[]byte("console.log(1)"), 0o600))

	return os.DirFS(root), sentinel
}

// doWithHeader 返回整个 recorder。
//
// server_test.go 的 do() 只给状态码和 body，拿不到响应头；测缓存头必须是
// recorder。写法照本文件已有的 TestSPA_ProblemUsesProblemJSONContentType。
func doWithHeader(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
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
		{"/assets/index-BjyFRYsa.js", "console.log(1)"},
		{"/assets/index-DEB1x6pO.css", "body{}"},
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
	assert.NotContains(t, body, "index-BjyFRYsa.js", "目录内容不该被列出来")
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

// 路径里带 ".." 的请求读不到被服务根之外的文件。
//
// 【这条以前是自证的】原版用的是 fakeWebFS（只含 web/ 几个文件的 MapFS），
// 断言 "响应体里没有 go.mod 的内容"——而那个 FS 里根本没有 go.mod，
// 背后也没有真实磁盘。把 fs.Sub 换成对仓库根的原始读取，它照样绿。
//
// 【为什么现在不是】哨兵真的写在磁盘上（realWebTree），而且实测过：把被
// 服务根从 web/ 放宽成它的父目录（也就是"fs.Sub(webFS,"web") 换成读仓库根"
// 那个回归），同一个请求会把 sentinel.txt 的内容发出来。MapFS 里没有这个
// 对象，那种 fixture 看不到这件事。
//
// 【它钉住的是哪一道】实际生效的是 MountSPA 里那次 fs.Stat：路径含 ".." 时
// fs.Sub 的路径校验让 Stat 返回 ErrInvalid，于是落回 SPA 兜底页。
// http.FileServer 自己也会 clean 路径（"/../sentinel.txt" 规整成
// "/sentinel.txt"），所以那几个形态本来就安全——留着是为了防止有人把守卫删掉。
func TestSPA_PathTraversalIsRefused(t *testing.T) {
	webFS, sentinel := realWebTree(t)
	r := newSPARouter(t, webFS)

	// 【对照组：同一棵真实树里，被服务根【之内】的文件要照常发出来】
	// 没有这一条，"哨兵没出现"也可能是因为静态文件这条路整个坏了——
	// 那样它也必然不出现，断言同样是自证的。
	code, body := do(r, http.MethodGet, "/assets/index-BjyFRYsa.js", "")
	require.Equal(t, http.StatusOK, code, body)
	require.Contains(t, body, "console.log(1)", "被服务根之内的文件必须发得出来")

	for _, p := range []string{
		"/../sentinel.txt",
		"/../../sentinel.txt",
		"/assets/../../sentinel.txt",
		"/..%2fsentinel.txt",
		// 反斜杠：fs.ValidPath 只按 "/" 切分，所以这两个路径能过路径校验，
		// 落到 fs.Stat 那一层才被拒——它们是唯一可能绕过"按 / 切分"这类
		// 校验的输入形态，值得单独留着（Windows 上 filepath.Join 会把 \ 当
		// 分隔符，实测没有逃出去）。
		`/..\sentinel.txt`,
		`/assets/..\..\sentinel.txt`,
	} {
		code, body := do(r, http.MethodGet, p, "")
		assert.NotContains(t, body, sentinel, "%s 读到了被服务根之外的文件", p)
		assert.Equal(t, http.StatusOK, code, "%s 应该落到 SPA 兜底页", p)
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

// ────────────────────────────────────────────────────────────────
// 静态资源的缓存策略
//
// 【这 8 条在加缓存头之前全部是红的】当时 spa.go 对三类文件一视同仁，
// 没有任何 Cache-Control；512 KB 的 JS 每次打开页面都全量重下。
// ────────────────────────────────────────────────────────────────

func TestCacheControlFor_DistinguishesHashedAssetsFromEverythingElse(t *testing.T) {
	// 判据是纯函数，单独钉一遍：它错了的话下面几条断言的失败信息会
	// 指向错误的地方。
	for _, tc := range []struct{ name, want string }{
		{"assets/index-BjyFRYsa.js", cacheImmutable},
		{"assets/index-DEB1x6pO.css", cacheImmutable},
		{"assets/geist-latin-wght-normal-BgDaEnEv.woff2", cacheImmutable},
		{"index.html", cacheRevalidate},
		{"favicon.svg", cacheRevalidate},
		{"", cacheRevalidate},
	} {
		assert.Equal(t, tc.want, cacheControlFor(tc.name), tc.name)
	}
}

func TestSPA_HashedAssetsAreImmutable(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	for _, p := range []string{
		"/assets/index-BjyFRYsa.js",
		"/assets/index-DEB1x6pO.css",
		"/assets/geist-latin-wght-normal-BgDaEnEv.woff2",
	} {
		w := doWithHeader(r, http.MethodGet, p)
		require.Equal(t, http.StatusOK, w.Code, p)
		assert.Equal(t, cacheImmutable, w.Header().Get("Cache-Control"), p)
	}
}

// index.html 一旦被长效缓存，发新版后浏览器会拿着旧 HTML 去要已经被
// emptyOutDir 删掉的哈希文件——白屏，且刷新也没用。三条路径都要钉住：
// / 走的是文件分支，另外两条走的是兜底分支，漏测一条就会漏掉一个分支没设头。
func TestSPA_IndexHTMLIsNeverCached(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	for _, p := range []string{"/", "/knowledge-bases", "/agents/42"} {
		w := doWithHeader(r, http.MethodGet, p)
		require.Equal(t, http.StatusOK, w.Code, p)

		got := w.Header().Get("Cache-Control")
		assert.Equal(t, cacheRevalidate, got, p)
		assert.NotContains(t, got, "immutable", p)
	}
}

func TestSPA_NonHashedPublicFileIsNotImmutable(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	w := doWithHeader(r, http.MethodGet, "/favicon.svg")

	require.Equal(t, http.StatusOK, w.Code)
	got := w.Header().Get("Cache-Control")
	assert.Equal(t, cacheRevalidate, got)
	assert.NotContains(t, got, "immutable", "favicon 的名字里没有内容哈希，不能按不可变缓存")
}

// FileServer 对 /index.html 回的是 301（规范化到 /）。301 本身也会被浏览器
// 长期记住，所以它同样不能带 immutable。
func TestSPA_IndexHTMLRedirectIsNotImmutable(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	w := doWithHeader(r, http.MethodGet, "/index.html")

	require.Equal(t, http.StatusMovedPermanently, w.Code)
	assert.Equal(t, cacheRevalidate, w.Header().Get("Cache-Control"))
}

// 最容易写错的一处：/assets/ 是目录，会走兜底分支返回 index.html。
// 如果兜底那行用了算出来的 name（"assets/"）而不是字面量 "index.html"，
// index.html 就会带上 immutable——发布后白屏。
//
// 【断言必须写成 Equal(cacheRevalidate) 而不是 NotContains("immutable")】
// 后者在「一个头都没设」时也是真的（空字符串不含 "immutable"），
// 于是这条用例在修复前也会通过，白占一个回归位。写成 Equal 才能真正钉住
// 「兜底分支必须自己设 no-cache」这件事。
func TestSPA_AssetsDirectoryRequestIsNotImmutable(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	w := doWithHeader(r, http.MethodGet, "/assets/")

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ConGoRAG")

	got := w.Header().Get("Cache-Control")
	assert.Equal(t, cacheRevalidate, got,
		"兜底返回的是 index.html，它的头必须按 index.html 算，不能按 assets/ 算")
	assert.NotContains(t, got, "immutable", "index.html 一旦被长效缓存，发版后就是白屏")
}

func TestSPA_MissingBuildIsNotCached(t *testing.T) {
	r := newSPARouter(t, fstest.MapFS{"web/.gitkeep": {Data: []byte{}}})

	w := doWithHeader(r, http.MethodGet, "/")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, cacheNoStore, w.Header().Get("Cache-Control"),
		"用户按提示跑完 make build-web 之后刷新就该好，不能让 503 留在缓存里")
}

// 拼错的端点返回 404，而 404 是启发式可缓存的——不显式关掉的话，
// 端点补上之后浏览器可能还是拿旧的 404。
func TestSPA_UnknownAPIPathIsNotCached(t *testing.T) {
	r := newSPARouter(t, fakeWebFS())

	w := doWithHeader(r, http.MethodGet, "/api/v1/typo")

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, cacheNoStore, w.Header().Get("Cache-Control"))
}
