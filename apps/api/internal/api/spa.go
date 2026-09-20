package api

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// apiPrefixes 是不该被前端兜底接管的路径前缀。
//
// 要和 web/vite.config.ts 的 proxy 列表保持一致——开发期由 Vite 转发这两类前缀，
// 交付期由这里排除。两边不一致的话，开发期和交付期对同一个请求的行为会不同。
var apiPrefixes = []string{"/api/", "/healthz"}

func isAPIPath(p string) bool {
	for _, prefix := range apiPrefixes {
		if p == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// MountSPA 把内嵌的前端产物挂到路由上。
//
// go:embed 只是把文件塞进二进制，不负责发出去。浏览器来要 / 或 /assets/xxx.js 时，
// 需要有人从内嵌的文件系统里读出来返回，这个函数就是那个人。
//
// 挂在 NoRoute 上：API 路由和 /healthz 已经注册过了，NoRoute 只在前面所有路由
// 都没匹配上时触发——正好是静态文件和前端路由的入口。
//
// 参数类型是 *gin.Engine 而不是 gin.IRouter：NoRoute 只定义在 *gin.Engine 上。
//
// webFS 声明成 fs.FS 而不是 embed.FS——这里只用到 fs.Sub 和 fs.Stat，
// 依赖接口让测试能塞 fstest.MapFS 进来，不必构造真的 embed.FS。
func MountSPA(r *gin.Engine, webFS fs.FS) error {
	// main.go 里写的是 //go:embed all:web，内嵌文件系统的最外层是 "web/"。
	// fs.Sub 把根切到 web/ 里，之后 Open("index.html") 拿到的就是 web/index.html。
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("open embedded web dir: %w", err)
	}

	// 前端有没有构建过。没构建时不阻止启动——开发期常用 pnpm dev 单独跑前端，
	// 后端不需要内嵌产物——但请求打进来要给出明确提示，而不是白屏。
	_, err = fs.Stat(sub, "index.html")
	built := err == nil

	files := http.FileServer(http.FS(sub))

	r.NoRoute(func(c *gin.Context) {
		// 【先排除 API 前缀】
		// NoRoute 对所有未匹配路径生效，包括 /api/ 下拼错的端点、用错的方法。
		// 不排除的话它们会拿到 200 + index.html——调用方无法区分"成功"和
		// "没这个接口"，而且是 HTML 不是 JSON，客户端解析时会抛语法错误。
		if isAPIPath(c.Request.URL.Path) {
			typ, title := "not_found", "接口不存在"
			detail := c.Request.Method + " " + c.Request.URL.Path + " 没有对应的接口"
			writeProblem(c, http.StatusNotFound, Problem{
				Type: typ, Status: http.StatusNotFound, Title: &title, Detail: &detail,
			})
			return
		}

		if !built {
			c.String(http.StatusServiceUnavailable,
				"前端尚未构建。\n\n在仓库根目录执行：\n\n    make build-web\n\n"+
					"或者用 `pnpm --filter web dev` 起 Vite 开发服务器（:5173）。\n")
			return
		}

		name := strings.Trim(c.Request.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}

		// 【必须确认是"文件"而不是"目录"】
		// Stat 对目录也返回 nil，光判断 err 会把 /assets/ 这类路径交给 FileServer，
		// 而它对没有 index.html 的目录会渲染目录列表，把内嵌产物的文件名全列出来。
		if info, statErr := fs.Stat(sub, name); statErr == nil && !info.IsDir() {
			files.ServeHTTP(c.Writer, c.Request)
			return
		}

		// 不是文件，按前端路由处理，返回 index.html。
		//
		// 单页应用的路径（如 /knowledge-bases）在服务器上没有对应文件，
		// 由浏览器里的 React Router 处理。用户在某个页面刷新时，
		// 服务器必须返回 index.html，前端才能接管并渲染。
		// 直接返回 404 的话，刷新就是白屏。
		c.Request.URL.Path = "/"
		files.ServeHTTP(c.Writer, c.Request)
	})

	return nil
}
