package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 安全响应头同样属于"少了不会报错"的防御：没有测试就只会在评审时
// 靠肉眼翻 curl 输出。这里钉死三件事——头值、panic 响应也带、CSP 的关键指令。

func TestSecurityHeaders_SetsBaselineHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, w.Code)

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Content-Security-Policy":    contentSecurityPolicy,
	}
	for name, value := range want {
		assert.Equal(t, value, w.Header().Get(name), "响应头 %s 必须按约定设置", name)
	}

	// 已废弃的头不要出现：写了会让人误以为还有 XSS Auditor 这一层防护。
	assert.Empty(t, w.Header().Get("X-XSS-Protection"),
		"X-XSS-Protection 已废弃，不能出现在响应里")
}

// 【这条是顺序断言】SecurityHeaders 必须排在 Recovery 之前：panic 之后
// Recovery 写出的 500 Problem 也要带安全头。把两个中间件顺序调换，这条会红。
func TestSecurityHeaders_PanicResponseStillCarriesHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := gin.New()
	r.Use(SecurityHeaders(), Recovery(logger))
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"),
		"panic 的 500 响应也必须带安全头")
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.NotEmpty(t, w.Header().Get("Content-Security-Policy"))
}

// CSP 的具体指令是这次交付里最容易被"顺手放松"的地方（比如为某个内联
// 脚本加 script-src 'unsafe-inline'）。逐条钉住关键约束。
func TestContentSecurityPolicy_HardensScriptAndFraming(t *testing.T) {
	csp := contentSecurityPolicy

	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	} {
		assert.Contains(t, csp, directive, "CSP 缺少指令：%s", directive)
	}

	// script-src 不允许内联：landing 的内联 script 只有 JSON-LD（不执行），
	// 一旦这里放开 'unsafe-inline'，CSP 对 XSS 的拦截基本就失效了。
	scriptSrc := directiveValue(t, csp, "script-src")
	assert.NotContains(t, scriptSrc, "unsafe-inline", "script-src 不能放开内联脚本")
	assert.NotContains(t, scriptSrc, "unsafe-eval", "script-src 不能放开 eval")

	// frame-ancestors 与 X-Frame-Options 双保险：前者是现代浏览器，后者
	// 给不认识 CSP 的旧浏览器。
	assert.Contains(t, csp, "frame-ancestors 'none'")
}

// directiveValue 取出 CSP 里某一条指令的值（不含指令名本身）。
func directiveValue(t *testing.T, csp, directive string) string {
	t.Helper()
	for _, part := range strings.Split(csp, ";") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) > 0 && fields[0] == directive {
			return strings.Join(fields[1:], " ")
		}
	}
	t.Fatalf("CSP 里找不到指令 %s", directive)
	return ""
}

// TestContentSecurityPolicy_AllowsInlineThemeBootstrap 是 CSP hash 的防漂移测试。
//
// web/index.html 里有一段同步主题引导脚本（在首帧前应用 localStorage 里的
// 主题，避免闪一下深色），CSP 用 sha256 精确放行它。脚本内容一改、hash
// 没同步，线上表现是"主题偏好失效 + 控制台 CSP 报错"，而普通的 Go 测试
// 完全不会红——除非有这条。
//
// 【为什么同时检查构建产物】Vite 目前把 index.html 原样复制过去，内容是
// 一致的；但那属于构建管线的事实、不是契约。本地/CI 产物存在时顺带校验，
// 不存在就跳过（CI 的 go job 里 apps/api/web/ 只有 .gitkeep）。
func TestContentSecurityPolicy_AllowsInlineThemeBootstrap(t *testing.T) {
	root := platformTestRepoRoot(t)
	checked := 0
	for _, rel := range []string{
		filepath.Join("web", "index.html"),
		filepath.Join("apps", "api", "web", "index.html"),
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			if rel == filepath.Join("apps", "api", "web", "index.html") {
				t.Logf("跳过 %s：前端产物未构建（CI 的 go job 里这是正常的）", rel)
				continue
			}
			t.Fatalf("读取 %s 失败：%v", rel, err)
		}

		script := bareInlineScript(t, string(data))
		// HTML 输入流预处理会把 CRLF 归一化成 LF（HTML 标准 13.2.3.5），
		// CSP 匹配的是归一化之后的脚本文本——用磁盘上的 CRLF 原文算出的
		// hash 在 Chromium 里匹配不上（已实测）。
		normalized := strings.ReplaceAll(script, "\r\n", "\n")
		sum := sha256.Sum256([]byte(normalized))
		source := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		assert.Contains(t, contentSecurityPolicy, "'"+source+"'",
			"%s 的内联脚本 hash 与 CSP 不一致：改了那段脚本就要同步 contentSecurityPolicy", rel)
		checked++
	}
	require.Positive(t, checked, "至少要校验到 web/index.html 这一份")
}

// bareInlineScript 取出第一个不带属性的 <script> 标签的正文。
func bareInlineScript(t *testing.T, html string) string {
	t.Helper()
	const openTag = "<script>"
	const closeTag = "</script>"
	start := strings.Index(html, openTag)
	if start < 0 {
		t.Fatalf("页面里没有裸 <script>：如果主题引导脚本已被挪成外部文件，请删掉 CSP 的 sha256 并同步本测试")
	}
	start += len(openTag)
	end := strings.Index(html[start:], closeTag)
	if end < 0 {
		t.Fatalf("裸 <script> 缺少闭合标签")
	}
	return html[start : start+end]
}

// platformTestRepoRoot 用本文件路径反推仓库根：go test 的 cwd 是包目录，
// 不能假设调用方在哪一层。
func platformTestRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller 不可用，无法定位仓库根：跳过基于 web/index.html 的 CSP 一致性检查")
	}
	// internal/platform/security_test.go -> <repo>
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
