// RFC 7807 错误响应的唯一实现。
//
// 【为什么在 platform 而不是 api】错误体有两个产者：api 的 handler（业务错误）
// 和 platform 自己的中间件（OriginCheck / Recovery——请求还没进业务就被拒）。
// 中间件不能 import api（方向反了，api 依赖 platform），两边各写一份的话，
// media type、status 字段、Cache-Control 三项里漏掉任何一项都不会编译失败，
// 只会让某一条路径上的错误体和契约不一致（issue #112）。
package platform

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Problem 是契约里所有错误响应共用的形状（contracts/openapi.yaml 的 Problem）。
//
// 与 apps/api/internal/api/generated.go 里那份 oapi-codegen 生成的类型一一对应，
// 改契约时两边要一起变。这里用值而不是指针：中间件产出的错误体四个字段都是
// 已知的，不需要区分"存在但为空"和"字段缺席"。
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ProblemContentType 是契约里所有错误响应声明的媒体类型。
const ProblemContentType = "application/problem+json; charset=utf-8"

// InternalErrorDetail 是 5xx 对外的统一 detail。
//
// 5xx 的 err.Error() 里可能有 SQLSTATE、表名、连接串——真实错误写日志，
// 用 request_id 关联，返回给调用方的只有这一句。REST 的 fail() 和 SSE 的
// SafeDetail 用同一个常量，两侧不会各写各的文案。
const InternalErrorDetail = "服务内部错误"

// WriteProblem 按 RFC 7807 写一个错误响应。
//
// 只负责写，不负责 abort——要不要终止中间件链是调用方自己的语义
// （中间件用 abortWithProblem，handler 写完就返回）。
//
// 【为什么不用 c.JSON】gin 的 c.JSON 固定写 application/json，
// 而契约里所有错误声明的是 application/problem+json。用 c.Data 才能指定。
func WriteProblem(c *gin.Context, p Problem) {
	body, err := json.Marshal(p)
	if err != nil {
		// Problem 是几个字符串和一个 int，序列化不会失败；真失败了也别再套一层错误。
		c.Status(p.Status)
		return
	}

	// 【为什么每个错误响应都要带 no-store】404 和 503 属于启发式可缓存的
	// 状态码，而拼错端点返回的正是 404（spa.go 的那条 API 前缀分支）。
	// 不显式关掉的话，浏览器可能把「这个端点不存在」记下来，之后端点补上了
	// 还是 404；503 同理——服务恢复了，用户还在看旧的「暂不可用」。
	c.Header("Cache-Control", "no-store")
	c.Data(p.Status, ProblemContentType, body)
}

// abortWithProblem 写一个 Problem 并终止中间件链。
//
// 【为什么要 abort】gin 的 Context.Next 是一个循环：中间件直接写响应体
// 不会让后面的 handler 停下（被拒的请求仍然会执行、甚至改数据，只是响应
// 被覆盖掉）。Recovery 里更要紧——panic 被 recover 之后，外层那个循环
// 会接着往下跑。
func abortWithProblem(c *gin.Context, p Problem) {
	c.Abort()
	WriteProblem(c, p)
}

// Classify 把 error 映射成 (HTTP 状态码, Problem.type, Problem.title)。
//
// 这是全项目唯一一张"错误 → 客户端看到什么"的表：REST 的 Problem 和 SSE 的
// error 帧都从它取值（SSEErrorType 只取 type 那一列）。docs/sse-protocol.md
// 写明两者的枚举是同一套，各维护一份的结果是同一个错误在实时帧里是
// conflict、在断线重放里变成 internal_error（issue #112）。
//
// 【为什么用 errors.Is 而不是 ==】错误一路上来被 %w 包了好几层，
// errors.Is 会顺着包装往里找。
//
// 【顺序从具体到笼统】放反了会先被笼统的那条接走：重复键是冲突的一种特例，
// 必须排在 ErrConflict 前面，前端才拿得到 conflict_duplicate_key。
//
// 【认不出来的一律 500】不要为了"让日志好看"把未知错误映射成 4xx——
// 那会把服务端故障说成调用方的问题，排查方向从第一句话起就是错的。
//
// 【恢复被拒绝的档位为什么这么细】那几种原因下用户能做的下一步完全不同：
// 快照版本不兼容（重新发起一次运行）与副作用已经生效（不能自动重放，
// 需要人工确认）。笼统给一句 conflict 或 internal_error 会让前端只能显示
// "服务内部错误"——issue #34 修掉的正是这一类误报。
func Classify(err error) (status int, typ, title string) {
	switch {
	case errors.Is(err, ErrStateSchemaVersionMismatch):
		return http.StatusConflict, "state_schema_version_mismatch", "这条运行的快照版本与当前代码不兼容"

	case errors.Is(err, ErrToolEffectApplied):
		return http.StatusConflict, "tool_effect_already_applied", "该步骤的副作用可能已经生效，不能自动重放"

	case errors.Is(err, ErrReplayUnsafe):
		return http.StatusConflict, "replay_unsafe", "这一步的工具不允许被自动重放"

	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest, "invalid_argument", "参数不合法"

	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found", "资源不存在"

	case errors.Is(err, ErrDuplicateKey):
		return http.StatusConflict, "conflict_duplicate_key", "资源已存在"

	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "conflict", "状态冲突"

	case errors.Is(err, ErrForeignKey):
		// 引用的父行不存在，对客户端等价于"你要的东西找不到"。
		return http.StatusNotFound, "not_found", "引用的资源不存在"

	case errors.Is(err, ErrUpstream):
		// 502 而不是 500：错的不是我们，是上游模型服务。
		// 这个区别决定了排查方向——502 看 API Key 和配额，500 看自己的代码。
		return http.StatusBadGateway, "upstream_llm_error", "上游模型服务出错"

	default:
		return http.StatusInternalServerError, "internal_error", InternalErrorDetail
	}
}

// SafeDetail 返回一个错误里【可以给客户端看】的那部分说明。
//
// 判据只有一条：4xx 透传，5xx 一律换成统一文案。
//
// 【status 为什么由调用方给，而不是在这里从 err 现算】因为本包认不出
// 别的包的私有 sentinel：ctxmgr.ErrOverflow 是 400、llm.ErrEmbeddingResetRequired
// 是 409，而那两个包都依赖本包（反向 import 会成环）。现算的话它们会落进
// default 被当成 500，"上下文超出窗口"的原因被抹成一句"服务内部错误"——
// 同一个错误在 REST 和 SSE 下说不同的话，正是 issue #112 要消灭的那种不一致。
// 调用方手里本来就有那个状态码（判据在本包和调用方之间只有一份：Classify）。
//
// 【为什么需要它】SSE 的 error 帧会被前端原样渲染成 toast 的第二行，
// 而它和 REST 的 fail() 是两条代码路径。5xx 不返回原文这条策略此前只在
// REST 侧、以及 SSE 的【兜底帧】上成立——兜底帧只在主帧发不出去时才跑
// （数据库整体不可用），所以 sse_test 那条断言是假绿：只要 event_id 分配
// 成功、后续任何一步以内部错误失败，客户端就会看到 SQLSTATE / 表名 /
// 连接串。主帧路径也走这个函数，这条策略才真正成立（issue #112）。
func SafeDetail(status int, err error) string {
	if status >= http.StatusInternalServerError {
		return InternalErrorDetail
	}
	return InnermostMessage(err)
}

// InnermostMessage 取错误包装链最内层的那句话。
//
// fmt.Errorf("a: %w", fmt.Errorf("b: %w", base)) 的 Error() 是 "a: b: base"，
// 逐层 errors.Unwrap 到最后一个还带有自己文案的错误，得到 "b: base"。
//
// 【为什么不 unwrap 到底】最底下是 sentinel（platform.ErrNotFound，文案就是
// "not found"），只剩它的话 detail 变成光秃秃的 "not found"，比 type 字段还少信息。
// 所以停在【倒数第二层】——那一层是离业务最近、又不含调用路径的一句。
//
// 【为什么导出】REST 的 4xx detail（api 的 fail，经它自己的 innermostMessage）
// 和 SSE 的 SafeDetail 必须是同一份提取逻辑，否则同一个错误在两种协议下的
// 文案不一样。
func InnermostMessage(err error) string {
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
