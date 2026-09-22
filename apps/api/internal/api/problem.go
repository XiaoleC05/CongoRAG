// RFC 7807 错误响应。
//
//	{
//	  "type":   "not_found",          ← 前端按它分支
//	  "title":  "资源不存在",          ← 给人看的短句
//	  "status": 404,
//	  "detail": "knowledge base ... not found"
//	}
//
// type 是契约的一部分，文案可以改。
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// problemContentType 是契约里所有错误响应声明的媒体类型。
// 取值来自 platform——中间件写的是同一种媒体类型，定义只有一处。
const problemContentType = platform.ProblemContentType

// writeProblem 按 RFC 7807 写出错误响应。
//
// 【为什么是个薄适配层】实现是 platform.WriteProblem：中间件的错误体
// （OriginCheck / Recovery）也要按契约写，而它不能 import 本包（方向反了，
// 本包依赖 platform）。两边各写一份的话，媒体类型、status 字段、
// Cache-Control 漏在某一条路径上不会编译失败，只会让客户端在那条路径上
// 拿到形状不同的错误体（issue #112）。
func writeProblem(c *gin.Context, status int, p Problem) {
	// Problem 是生成器产出的类型（Title / Detail 是 *string），换成 platform
	// 那份值类型——nil 和空串在 JSON 里同样是被省略。
	pp := platform.Problem{Type: p.Type, Status: status}
	if p.Title != nil {
		pp.Title = *p.Title
	}
	if p.Detail != nil {
		pp.Detail = *p.Detail
	}
	platform.WriteProblem(c, pp)
}

// fail 把业务层的错误翻译成 HTTP 响应并写出去。
//
// 业务错误只有这一条出口。其他层只用 %w 包错误往上传递，它们不 import
// net/http，也不知道 404 和 500 的区别。（中间件那条路径不走这里——
// 请求还没进业务就被拒，写法见 platform.abortWithProblem。）
func (s *Server) fail(c *gin.Context, err error) {
	status, typ, title := classify(err)

	// 5xx 用统一文案，不把内部错误原文返回给前端：
	// err.Error() 里可能有 SQL 片段、表名、文件路径。
	// 真实错误写进日志，用 request_id 关联。
	if status >= http.StatusInternalServerError {
		s.deps.Logger.Error("request failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"error", err,
		)
		detail := platform.InternalErrorDetail
		writeProblem(c, status, Problem{
			Type: typ, Status: status, Title: &title, Detail: &detail,
		})
		return
	}

	// 4xx 走这里。detail 是给【调用方】看的，只取最内层那句。
	//
	// 【为什么不用 err.Error()】错误一路上来被 %w 包了好几层，
	// err.Error() 是所有层拼起来的整串：
	//
	//	"rename knowledge base <id>: knowledge base <id>: not found"
	//
	// 那是服务端的内部调用路径，id 出现两次，对客户端没有意义。
	// innermostMessage 只取最内层，得到 "knowledge base <id>: not found"。
	//
	// 经 platform.SafeDetail 而不是直接调它会多一次判断（这里 status 必然
	// < 500，走的就是透传那支），换来的是和 SSE 的 error 帧同一份判据——
	// 两条协议不会各写一套"5xx 不说原文"。
	detail := platform.SafeDetail(status, err)

	// 4xx 也要记一条日志——否则"客户端说报 400 了"这类反馈无从查起。
	// 用 Warn 不是 Error：4xx 是调用方的问题，不是服务的故障。
	s.deps.Logger.Warn("request rejected",
		"request_id", platform.RequestIDFrom(c.Request.Context()),
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"status", status,
		"error", err, // 日志里留完整的包装链，排查时要靠它
	)

	// Problem 的 Title / Detail 在契约里不是必填字段，生成的是 *string。
	writeProblem(c, status, Problem{
		Type:   typ,
		Status: status,
		Title:  &title,
		Detail: &detail,
	})
}

// innermostMessage 取错误包装链最内层的那句话。
//
// 实现是 platform.InnermostMessage：4xx 的 detail 和 SSE 主帧的
// platform.SafeDetail 必须是同一份提取逻辑，否则同一个错误在 REST 和流式
// 两种协议下的文案会不一样。这个名字留在本包是因为 server.go 的
// writeFallbackError 也调用它。
func innermostMessage(err error) string {
	return platform.InnermostMessage(err)
}

// BindErrorHandler 处理【生成器包装层】的参数绑定失败。
//
// 什么时候会走到这里：路径参数格式不对（如 /knowledge-bases/not-a-uuid），
// 在进入 handler 之前就被 generated.go 的包装层拦下。
//
// 【为什么必须传它】不传的话会落到 oapi-codegen 的默认分支：
//
//	c.JSON(statusCode, gin.H{"msg": err.Error()})
//
// 那有两个问题：媒体类型是 application/json 而契约声明的是
// application/problem+json；结构是 {"msg": "..."} 而契约声明的是 Problem。
// 调用方会拿到两种互不兼容的错误体，按 type 分支的前端代码在这里会失效。
//
// 用法见 apps/api/internal/app/app.go 的 RegisterHandlersWithOptions。
func BindErrorHandler(c *gin.Context, err error, statusCode int) {
	typ, title := "invalid_argument", "参数不合法"
	if statusCode >= http.StatusInternalServerError {
		typ, title = "internal_error", "服务内部错误"
	}

	// 【不能直接用 err.Error()】生成器拼出来的原文长这样：
	//
	//	Invalid format for parameter id: parsing value: invalid UUID length: 10
	//
	// 有时还会带上 Go 的类型名（如 *uuid.UUID）。参数名是客户端该知道的，
	// Go 的内部类型名不是——它既帮不上调用方，又暴露了实现细节。
	// bindErrorDetail 只保留参数名那一段。
	detail := bindErrorDetail(err)
	writeProblem(c, statusCode, Problem{
		Type:   typ,
		Status: statusCode,
		Title:  &title,
		Detail: &detail,
	})
}

// bindErrorMsgPrefix 是 oapi-codegen 生成的绑定错误的固定前缀，
// 见 generated.go 里的 fmt.Errorf("Invalid format for parameter %s: %w", ...)。
// 生成器换版本时这段文案可能变——变了的话下面的测试会红，不会静默失效。
const bindErrorMsgPrefix = "Invalid format for parameter "

// bindErrorDetail 从生成器的绑定错误里提取出客户端该看到的那部分。
//
// 输入 "Invalid format for parameter id: parsing value: invalid UUID length: 10"
// 输出 "参数 id 的格式不正确"。
//
// 认不出格式时返回一句通用的，不回退到 err.Error()——回退就等于这个函数白写。
func bindErrorDetail(err error) string {
	if err == nil {
		return "参数不合法"
	}
	msg := err.Error()
	rest, ok := strings.CutPrefix(msg, bindErrorMsgPrefix)
	if !ok {
		return "参数不合法"
	}
	// rest 形如 "id: parsing value: ..."，取第一个冒号之前的参数名。
	name, _, found := strings.Cut(rest, ":")
	if !found || name == "" {
		return "参数不合法"
	}
	return "参数 " + name + " 的格式不正确"
}

// classify 把 error 翻译成 (HTTP 状态码, Problem.type, Problem.title)。
//
// 只有两档在这里特判，其余全部走 platform.Classify——REST 的 Problem.type
// 和 SSE error 帧的 type 共用那一张表，同一个错误不会在实时帧里是 conflict、
// 在断线重放里变成 internal_error（issue #112）。
//
// 【为什么这两档不能挪到 platform】ctxmgr.ErrOverflow 和
// llm.ErrEmbeddingResetRequired 是那两个包自己的领域概念，而它们都依赖
// platform（反向 import 会成环），platform.Classify 认不了——和 conversation
// 的 eventErrorType 特判 context_overflow 是同一个理由。两档都必须排在前面：
// platform 那边有更宽的匹配（ErrConflict），顺序反了会被先接走。
func classify(err error) (status int, typ, title string) {
	switch {
	// 技术方案 §九点名"Context 超预算返回 context_overflow 错误类型"，
	// 这个类型属于 ctxmgr 的领域概念（代码架构设计 §9.1），不适合定义在
	// platform 里和别的 sentinel 混在一起。
	case errors.Is(err, ctxmgr.ErrOverflow):
		return http.StatusBadRequest, "context_overflow", "上下文超出了模型窗口"

	// llm.ErrEmbeddingResetRequired 是同一个模式（issue #39）。它必须是独立的
	// type——前端要按它弹"确认清空并重建"的对话框，混进笼统的 conflict 里
	// 就分不出来。
	case errors.Is(err, llm.ErrEmbeddingResetRequired):
		return http.StatusConflict, "embedding_change_requires_reindex", "换 embedding 模型需要先确认清空重建"

	default:
		return platform.Classify(err)
	}
}
