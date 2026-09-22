package platform

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
)

// SSEErrorType 把 error 映射成 SSE error 事件的 type 字段。
//
// 【为什么在 platform】docs/sse-protocol.md 规定 SSE 错误的 type 和 REST
// 的 Problem.type 是同一套枚举，那就只能有一处定义——聊天的流式回答
// （conversation）和 Agent 的运行流（agent）都要用它，各自维护一份迟早
// 会漂移。放在这里是因为 sentinel 全项目只在 sentinel.go 声明一次，
// 而这张表认的就是这些 sentinel。
//
// 【枚举本体是 Classify 那张表】本函数只取它的 type 那一列——SSE 帧里没有
// 状态码的位置，但"哪个 error 算哪一档"必须和 REST 完全一致。此前这里是个
// 自己的 switch，缺了 conflict / conflict_duplicate_key / not_found 三档，
// 于是同一个 ErrConflict 在实时帧里是 conflict、在断线重放里变成
// internal_error，用户重连后看到的第一句话和当时看到的对不上（issue #112）。
//
// 【context_overflow 不在这里】它属于 ctxmgr 的私有语义，而本包不能
// import ctxmgr（llm 依赖本包，ctxmgr 依赖 llm，反向 import 会成环），
// 所以那一档由 conversation 在调用本函数之前先判，其余档位共用。
func SSEErrorType(err error) string {
	_, typ, _ := Classify(err)
	return typ
}

// sseEventError 是 docs/sse-protocol.md 里那个终态错误事件的名字。
const sseEventError = "error"

// sseErrorData 是 error 事件的 data 字段，形状必须和 conversation 包
// emitEvent 里那个 errorPayload 一致（docs/sse-protocol.md「事件类型」：
// data: {"type": string, "detail": string}）。
type sseErrorData struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// writeSSEErrorFrame 往一个【已经开始】的 SSE 流里补一条 error 帧。
//
// 【为什么没有 id 字段】这一帧没有对应的持久化事件，编不出真实 event_id。
// SSE 规范里不带 id 的帧不会更新客户端的 lastEventId，续传游标因此停在
// 最后一个真实事件上；写 `id: 0` 反而会把游标退回起点，下次续传要把整个
// 会话重放一遍。apps/api/internal/api/sse.go 的写失败兜底分支是同一个理由，
// 两处都不能编 id 出来。
//
// 【为什么手工拼帧，而不是复用 api 的 sink】这个函数要在 panic 恢复里跑，
// 而 platform 不认识 conversation.EventSink。线路格式由
// docs/sse-protocol.md 定义，两边都照它写。
func writeSSEErrorFrame(c *gin.Context, typ, detail string) {
	body, err := json.Marshal(struct {
		Type string       `json:"type"`
		Data sseErrorData `json:"data"`
	}{Type: sseEventError, Data: sseErrorData{Type: typ, Detail: detail}})
	if err != nil {
		// 两个字符串的序列化不会失败；真失败也没有别的办法把话说出去。
		return
	}

	// 【为什么必须整帧一次写完并 Flush】半截帧对客户端等于没有帧：
	// 它的解析器按 \n\n 切帧，切不出来的部分留在缓冲区里等下一条——
	// 而这条流已经不会再有任何字节了。
	fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", sseEventError, body)
	c.Writer.Flush()
}
