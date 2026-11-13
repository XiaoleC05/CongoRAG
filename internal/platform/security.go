package platform

import (
	"github.com/gin-gonic/gin"
)

// 安全响应头。
//
// 【为什么放在 Go 中间件而不是 nginx】部署形态是本地单机（ADR-001），
// 开发期用 make dev 直接跑 Go 进程、交付期前面也可能没有反代——中间件是
// 唯一能在"所有入口"一致生效的位置。nginx 的配置可以补，但只在部署期
// 存在的那一份配置挡不住 go run 的本机路径，而本机路径恰恰是评审最容易
// 看到的那条。
const (
	// contentSecurityPolicy 是公开站与内嵌单页应用共用的一份 CSP。
	//
	// 【style-src 为什么需要 'unsafe-inline'】landing/ 的静态页把样式内联
	// 在 <style> 里（没有外链 CSS），而内嵌前端的组件库（Radix）在运行期
	// 用 style 属性做定位。CSP2 起 style-src 同时约束 <style> 元素与
	// style 属性，不放 'unsafe-inline' 这两类都会被拦掉；样式内联本身
	// 不构成脚本执行，脚本侧由下面的 script-src 收紧（'self' + 一个 hash）。
	//
	// 【script-src 为什么是 'self' + 一个 sha256，而不是 'unsafe-inline'】
	// 可执行脚本只有两类：
	//   1) 打包产物（同源 /assets/*.js）——'self' 已经覆盖；
	//   2) web/index.html 里那段同步主题引导脚本（localStorage 记的主题
	//      要在首帧前生效，放进 React 会闪一下深色再变浅色）。它必须内联，
	//      因为构建产物没有 SSR，这是唯一的首帧前执行点。
	// 对内联脚本来说，hash 比 'unsafe-inline' 严格得多：只有内容完全等于
	// 这一段的脚本可以执行，XSS 注入的任意内联脚本仍然会被拦。
	// landing/ 的内联 <script> 只有 JSON-LD（浏览器不执行，也不受
	// script-src 约束），所以 landing 不需要额外放行。
	//
	// 【hash 是 LF 归一化之后算的】HTML 解析器在分词前会把 CRLF 归一化成
	// LF（HTML 标准 13.2.3.5 的输入流预处理），CSP 匹配的是归一化后的
	// 脚本文本。用磁盘上的 CRLF 原文算出来的 hash 在 Chromium 里匹配不上，
	// 这一点已用 Playwright 实测过（security_test.go 会现算 hash 比对）。
	//
	// 【维护约束】web/index.html 里这段脚本一改，这个 hash 必须同步更新；
	// security_test.go 会读 web/index.html 现算 hash 并与这里比对，漏改
	// 会红。更好的终态是把引导脚本挪成同源外部文件（R4 前端范围），
	// 然后删掉这里的 hash 与对应测试。
	//
	// 【worker-src 'self' blob:】PDF.js 的 worker 由 Vite 以 ?worker 打进
	// 产物（见 web/src/pages/ReaderPage.tsx 顶部注释），但它初始化时会用
	// blob: URL 建 Worker；缺这条会让阅读器在 CSP 下静默无法渲染。
	//
	// 【connect-src 'self'】前端所有请求都是同源（SSE 也是 fetch 流），
	// 不需要放行第三方域名。以后真要接外部服务，必须在这里显式加白。
	contentSecurityPolicy = "default-src 'self'; " +
		"script-src 'self' 'sha256-Sgw2g2ILIfIIJiVVjLlGNXY2WWw9i78gTwabTEuaAco='; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self' data:; " +
		"connect-src 'self'; " +
		"worker-src 'self' blob:; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'"
)

// SecurityHeaders 给每个响应加上一组防御性响应头。
//
// 【必须排在 Recovery 之前】gin 的响应头是在第一次 WriteHeader 时从
// Header map 刷出去的；这里在 c.Next() 之前把值写进 map，后面无论哪个
// handler 先写响应（包括 Recovery 捕获 panic 后写出的 500 Problem），
// 刷出去的那份都带着这些头。顺序反过来——Recovery 在前——panic 响应
// 就没有安全头，而那正是最需要确定性错误响应的一条路径。
//
// 【为什么不设 X-XSS-Protection】它是旧 IE/Chrome 的 XSS Auditor 开关，
// 现代浏览器已经移除；保留它既没有防护效果，又会让人误以为这一层已经
// 处理了 XSS。真正的防线是 CSP + 输出转义，写 0 或干脆不写都比写 1 好。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.Writer.Header()

		// 禁止浏览器猜 MIME：上传文件下载端点返回 octet-stream 时，
		// 没有它浏览器可能把内容嗅探成 HTML 并执行。
		header.Set("X-Content-Type-Options", "nosniff")
		// 禁止被 iframe 嵌入：本地应用没有任何合法的嵌入场景，
		// DENY 比 SAMEORIGIN 更符合"单机单用户"的边界。
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		// 本地应用不需要摄像头/麦克风/定位；显式声明可以让被注入的页面
		// 也无法申请这些权限（Permissions-Policy 是 iframe 场景的主防线）。
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// 隔离 opener：跨源弹窗拿不到本页的 window 引用，减小 XSS 后的
		// 横向利用面；对单页应用本身没有副作用。
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		// CSP 里同时用 frame-ancestors 'none'，与 X-Frame-Options 形成
		// 新老浏览器各认一条的双保险（老浏览器不认识 CSP 指令）。
		header.Set("Content-Security-Policy", contentSecurityPolicy)

		c.Next()
	}
}
