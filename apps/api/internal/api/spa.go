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

// 静态产物的缓存策略。
//
// 【判据用目录前缀，不用文件名里的哈希】assets/ 是 Vite 的 assetsDir
// （web/vite.config.ts 没有改过它，用的是默认值），这个目录 100% 由构建
// 生成，文件名里的哈希也是 Vite 的默认行为。而 web/public/ 下的文件
// （favicon 等）是被原样拷到产物**根**的，不进这一层——所以「路径以
// assets/ 开头」正好把两类分开。
//
// 不用「文件名匹配 -[A-Za-z0-9_-]{8}\.」那种正则：正则得跟着 Vite 的
// 哈希长度和 assetsDir 配置一起改，而改错了不报错——只是缓存悄悄失效
// （性能无声退化），或者缓存了不该缓存的东西（白屏）。
const (
	hashedAssetsPrefix = "assets/"

	// 内容哈希资源不可变：文件名带哈希，内容一变文件名就变，
	// 同一个 URL 的内容因此永远不会变，可以放心缓存一年。
	cacheImmutable = "public, max-age=31536000, immutable"

	// index.html 以及 favicon 这类非哈希资源：每次用之前回源校验。
	//
	// 【为什么 index.html 绝对不能长效缓存】构建是 emptyOutDir
	// （web/vite.config.ts），发新版之后旧的 index-<hash>.js 会被删掉。
	// 而浏览器里缓存着的旧 index.html 正指向那些已经不存在的文件——
	// 表现是白屏，而且刷新也没用（刷新拿到的是缓存里的旧 HTML）。
	//
	// 【no-cache 不等于 no-store】no-cache 是「可以存，但每次用之前回源
	// 确认」，回源就是本机 Go 进程，代价一次 RTT，换来的是还能省下 body。
	//
	// 【但这里拿不到 304】go:embed 的文件 ModTime() 恒为零值，net/http
	// 在零值时既不写 Last-Modified 也不处理 If-Modified-Since，所以没有
	// 可用的校验器——no-cache 的实际效果就是每次重下那 1.2 KB。
	// 把这一点写死在这里，免得下一个人以为 no-cache 已经省下了往返、
	// 进而把 index.html 也改成 immutable（那正是白屏的触发方式）。
	cacheRevalidate = "no-cache"

	// 503（前端未构建）显式关掉缓存：用户按提示跑完 make build-web 之后
	// 刷新就该好，而不是继续看到这条提示。
	cacheNoStore = "no-store"
)

// cacheControlFor 返回一个静态资源该带的 Cache-Control 值。
//
// 【调用方注意】兜底分支（返回 index.html 的那个）必须传字面量
// "index.html"，不能传它自己算出来的 name——那个 name 可能是
// "assets/xxx"（目录请求 /assets/ 就会走到兜底分支），传错的话
// index.html 会被以 immutable 发出去，正是白屏的触发器。
func cacheControlFor(name string) string {
	if strings.HasPrefix(name, hashedAssetsPrefix) {
		return cacheImmutable
	}
	return cacheRevalidate
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
			c.Header("Cache-Control", cacheNoStore)
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
			// 【必须在 ServeHTTP 之前设】FileServer 自己不写 Cache-Control，
			// 而它内部的 ServeContent 会保留调用方已经设好的头。
			c.Header("Cache-Control", cacheControlFor(name))
			files.ServeHTTP(c.Writer, c.Request)
			return
		}

		// 不是文件，按前端路由处理，返回 index.html。
		//
		// 单页应用的路径（如 /knowledge-bases）在服务器上没有对应文件，
		// 由浏览器里的 React Router 处理。用户在某个页面刷新时，
		// 服务器必须返回 index.html，前端才能接管并渲染。
		// 直接返回 404 的话，刷新就是白屏。
		//
		// 【这里必须写字面量，不能用上面的 name】走到这个分支时 name 可能
		// 是 "assets/xxx"（例如请求 /assets/ 这个目录），而这一行发出去的
		// 是 index.html——用 cacheControlFor(name) 会让 index.html 带上
		// immutable，正是「发布后白屏」的触发器。
		c.Header("Cache-Control", cacheControlFor("index.html"))
		c.Request.URL.Path = "/"
		files.ServeHTTP(c.Writer, c.Request)
	})

	return nil
}
