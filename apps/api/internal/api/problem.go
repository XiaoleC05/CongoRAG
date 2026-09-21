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
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// problemContentType 是契约里所有错误响应声明的媒体类型。
const problemContentType = "application/problem+json; charset=utf-8"

// writeProblem 按 RFC 7807 写出错误响应。
//
// 【为什么不用 c.JSON】gin 的 c.JSON 固定写 application/json，
// 而契约里所有错误声明的是 application/problem+json。用 c.Data 才能指定。
func writeProblem(c *gin.Context, status int, p Problem) {
	body, err := json.Marshal(p)
	if err != nil {
		// Problem 是几个字符串和一个 int，序列化不会失败；真失败了也别再套一层错误。
		c.Status(status)
		return
	}

	// 【为什么每个错误响应都要带 no-store】404 和 503 属于启发式可缓存的
	// 状态码，而拼错端点返回的正是 404（spa.go 的那条 API 前缀分支）。
	// 不显式关掉的话，浏览器可能把「这个端点不存在」记下来，之后端点补上了
	// 还是 404；503 同理——服务恢复了，用户还在看旧的「暂不可用」。
	c.Header("Cache-Control", cacheNoStore)
	c.Data(status, problemContentType, body)
}

// fail 把业务层的错误翻译成 HTTP 响应并写出去。
//
// 这是全项目唯一把 error 变成 HTTP 的地方。其他层只用 %w 包错误往上传递，
// 它们不 import net/http，也不知道 404 和 500 的区别。
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
		detail := "服务内部错误"
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
	detail := innermostMessage(err)

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
// fmt.Errorf("a: %w", fmt.Errorf("b: %w", base)) 的 Error() 是 "a: b: base"，
// 逐层 errors.Unwrap 到最后一个还带有自己文案的错误，得到 "b: base"。
//
// 【为什么不 unwrap 到底】最底下是 sentinel（platform.ErrNotFound，文案就是
// "not found"），只剩它的话 detail 变成光秃秃的 "not found"，比 type 字段还少信息。
// 所以停在【倒数第二层】——那一层是离业务最近、又不含调用路径的一句。
func innermostMessage(err error) string {
	if err == nil {
		return ""
	}
	prev := err
	for {
		next := errors.Unwrap(prev)
		// next 为 nil：prev 是链的末端（sentinel），返回上一层。
		// next.Error() == prev.Error()：这一层只做了 %w 没加文案，继续往下。
		if next == nil {
			return prev.Error()
		}
		if errors.Unwrap(next) == nil {
			// next 是末端 sentinel，prev 是倒数第二层——正是要的那一层。
			return prev.Error()
		}
		prev = next
	}
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

// classify 把 sentinel 错误映射成 HTTP 状态码。
//
// 用 errors.Is 而不是 ==：错误一路上来被 %w 包了好几层，
// errors.Is 会顺着包装往里找。
//
// 顺序从具体到笼统，放反了会先被笼统的那条接走。
func classify(err error) (status int, typ, title string) {
	switch {
	// ctxmgr.ErrOverflow 排在最前面：它是本包（api）唯一引用的、
	// platform 之外的 sentinel——技术方案 §九点名"Context 超预算返回
	// context_overflow 错误类型"，这个类型属于 ctxmgr 的领域概念
	//（代码架构设计 §9.1：「ctxmgr.ErrOverflow——这个属于 ctxmgr，
	// 因为是它的领域概念」），不适合定义在 platform 里和别的 sentinel 混在一起。
	case errors.Is(err, ctxmgr.ErrOverflow):
		return http.StatusBadRequest, "context_overflow", "上下文超出了模型窗口"

	case errors.Is(err, platform.ErrInvalid):
		return http.StatusBadRequest, "invalid_argument", "参数不合法"

	case errors.Is(err, platform.ErrNotFound):
		return http.StatusNotFound, "not_found", "资源不存在"

	case errors.Is(err, platform.ErrDuplicateKey):
		return http.StatusConflict, "conflict_duplicate_key", "资源已存在"

	// ErrConflict 是更笼统的冲突（状态机不允许的转换、并发修改），
	// 必须排在 ErrDuplicateKey 之后：重复键是冲突的一种特例，
	// 放前面的话它会先被这条接走，前端拿不到 conflict_duplicate_key。
	case errors.Is(err, platform.ErrConflict):
		return http.StatusConflict, "conflict", "状态冲突"

	case errors.Is(err, platform.ErrForeignKey):
		// 引用的父行不存在，对客户端等价于"你要的东西找不到"。
		return http.StatusNotFound, "not_found", "引用的资源不存在"

	case errors.Is(err, platform.ErrUpstream):
		// 502 而不是 500：错的不是我们，是上游模型服务。
		// 这个区别决定了排查方向——502 看 API Key 和配额，500 看自己的代码。
		return http.StatusBadGateway, "upstream_llm_error", "上游模型服务出错"

	default:
		return http.StatusInternalServerError, "internal_error", "服务内部错误"
	}
}
