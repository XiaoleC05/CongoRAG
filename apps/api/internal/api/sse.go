// sse.go 是 conversation.EventSink 唯一的生产实现——把持久化好的事件
// 写成 docs/sse-protocol.md 定义的线路格式，发给浏览器。
//
// 【为什么这个类型在 api 包，不在 conversation 包】conversation 包
// 不认识 gin、不认识 HTTP（代码架构设计 §3 的表：业务包里没有
// handler.go）。EventSink 接口在 conversation/port.go 声明（消费方
// 声明它需要什么），这里是它的 HTTP 实现——和 knowledge.FileCleaner
// 由 internal/knowledge/river.go 实现是同一个"接口在消费方、实现在
// 别处"的模式，只是这次实现方恰好是 HTTP 层而不是另一个 internal 包。
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
)

// sseHeartbeatInterval 是"多久没写数据就补一条注释帧"的间隔。
//
// 【为什么要发心跳】浏览器与反向代理都会掐掉长时间没有任何字节的连接。
// 一次 Agent run 在等模型/工具时可能几分钟不产生 token；没有心跳的话，
// 用户看到的是"回答到一半突然没了"，而服务端日志里一切正常。注释帧
// （以 ":" 开头）在 SSE 规范里被客户端忽略，不推进 lastEventId，是最安全的
// keep-alive 形式。
//
// 15 秒来自 nginx 默认 proxy_read_timeout 60s / 常见云负载均衡 60s 的经验值：
// 取四分之一留足抖动余量。测试会把变量改小，所以它是 var 不是 const。
var sseHeartbeatInterval = 15 * time.Second

var _ conversation.EventSink = (*sseSink)(nil)

// eventErrorName 是 docs/sse-protocol.md 里那个终态错误事件的名字。
// conversation 包发它时用的就是这个字符串字面量（emitEvent 的 eventType
// 参数），这里需要它来判断"客户端到底有没有收到过失败通知"。
const eventErrorName = "error"

// sseErrorData 是 error 事件的 data 字段，形状必须和 conversation 包
// emitEvent 里那个 errorPayload 一致（docs/sse-protocol.md「事件类型」：
// data: {"type": string, "detail": string}）。那边没有导出这个类型，
// 这里照协议再声明一份——规范来源是那份文档，不是对方的代码。
type sseErrorData struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// sseSink 包着一个 *gin.Context，把 conversation.Event 写成 SSE 帧。
type sseSink struct {
	c    *gin.Context
	done <-chan struct{}
	stop chan struct{}
	once sync.Once

	// mu 保护对 ResponseWriter 的所有写入。
	//
	// 【为什么需要它】v6.4.0 起有一个心跳 goroutine 会周期性往同一条流里写
	// 注释帧；handler 自己也在写事件帧。两个 goroutine 同时写 ResponseWriter
	// 会把帧交错（SSE 是文本协议，交错就是坏帧），所以所有写入口
	// （Emit / writeFallbackError / Flush / 心跳）共用这一把锁。
	mu        sync.Mutex
	lastWrite time.Time

	// errorFrameSent 记录 error 帧是不是已经成功写出去过——SendMessage 在
	// Send 返回错误时靠它决定要不要补一条兜底帧（见 writeFallbackError）。
	errorFrameSent bool
}

// newSSESink 把响应头设成 SSE 要求的样子，返回一个可以直接传给
// conversation.Usecase.Send 的 sink。
//
// 【为什么头在这里设，不在 handler 里设】设置响应头必须在写任何响应体
// 之前完成——把这一步和"构造 sink"绑在一起，调用方不会有机会在设置头
// 之前不小心先写了数据（那样头会被 gin 提前用默认值 flush 掉，之后
// 再设置 Content-Type 不会生效）。
func newSSESink(c *gin.Context) *sseSink {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// 立刻写一次状态码 + 已设置的头，确保客户端马上能建立起连接，
	// 不用等到第一条真实事件才收到响应头。
	c.Status(http.StatusOK)

	sink := &sseSink{
		c:         c,
		done:      c.Request.Context().Done(),
		stop:      make(chan struct{}),
		lastWrite: time.Now(),
	}
	sink.startHeartbeat(sseHeartbeatInterval)
	return sink
}

// startHeartbeat 起一个只在"确实空闲"时补注释帧的 goroutine。
//
// 【为什么要看 lastWrite 而不是无脑写】有数据在流的时候不需要心跳；无脑写
// 会让每条正常回答里夹一堆注释帧（多耗带宽，日志也变吵）。这里每 tick 检查
// 一次：距上次写入不足一个间隔就跳过。
func (s *sseSink) startHeartbeat(interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-s.stop:
				return
			case <-ticker.C:
				s.mu.Lock()
				if time.Since(s.lastWrite) < interval {
					s.mu.Unlock()
					continue
				}
				_, err := fmt.Fprint(s.c.Writer, ": keep-alive\n\n")
				if err == nil {
					s.c.Writer.Flush()
				}
				s.lastWrite = time.Now()
				s.mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
}

// Close 停掉心跳 goroutine。幂等，handler 用 defer 调用。
func (s *sseSink) Close() {
	s.once.Do(func() { close(s.stop) })
}

// Emit 按 docs/sse-protocol.md 定义的帧格式写一条事件。
//
// 【为什么不用 gin 自带的 c.SSEvent】那个辅助方法把 data 字段用 JSON
// 编码器现写一遍，会导致 payload 已经是 []byte 时被二次编码成一个
// JSON 字符串（多一层转义）。这里的 Event.Payload 已经是
// conversation.Usecase 组装好的完整 JSON,直接原样写出去。
//
// 【ID 为 0 时整行省掉 id:，不是写 id: 0】0 是"这一帧没有对应的持久化
// 事件"的约定值——agent.Usecase.emitUnpersisted 就用它发"run 根本没开始"
// 那几类失败（两个计数器表的 next_event_id 都从 1 开始，0 不是任何真实
// 事件的号）。SSE 规范里只有**带 id 字段**的帧才会更新客户端的 lastEventId，
// 写成 id: 0 会把续传游标退回到起点，下次续传把整个会话重放一遍。
// docs/sse-protocol.md「线路层怎么区分这两种帧」写的就是这一行，
// writeFallbackError（下面那个）走的是同一条判据——两条路必须一致。
func (s *sseSink) Emit(ev conversation.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 拼一个可以为空的前缀，而不是写两条 Fprintf：帧格式只有一处可看，
	// 加字段时不会漏改其中一条分支。
	id := ""
	if ev.ID != 0 {
		id = fmt.Sprintf("id: %d\n", ev.ID)
	}
	_, err := fmt.Fprintf(s.c.Writer, "%sevent: %s\ndata: %s\n\n", id, ev.Type, ev.Payload)
	s.lastWrite = time.Now()
	if err != nil {
		return fmt.Errorf("write sse frame: %w", err)
	}
	// 只在写成功之后才记：写失败说明这一帧没能出去（连接已经坏了），
	// 补写兜底帧也是白写；反过来漏记会让调用方把同一条错误发两遍。
	if ev.Type == eventErrorName {
		s.errorFrameSent = true
	}
	return nil
}

// writeFallbackError 绕过持久化，直接往连接上写一条 error 帧。
//
// 【为什么不能用 Emit】这一帧在 conversation_events 里没有对应的行，
// 编不出真实 event_id。SSE 规范里**不带 id 字段**的帧不会更新客户端的
// lastEventId，续传游标因此停在最后一个真实事件上；写 `id: 0` 反而会把
// 游标退回到起点，下次续传要把整个会话重放一遍。
func (s *sseSink) writeFallbackError(data sseErrorData) error {
	body, err := json.Marshal(struct {
		Type string       `json:"type"`
		Data sseErrorData `json:"data"`
	}{Type: eventErrorName, Data: data})
	if err != nil {
		// 两个字符串的序列化不会失败；真失败也没有别的办法把话说出去。
		return fmt.Errorf("marshal fallback error frame: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.c.Writer, "event: %s\ndata: %s\n\n", eventErrorName, body); err != nil {
		return fmt.Errorf("write fallback error frame: %w", err)
	}
	s.lastWrite = time.Now()
	s.c.Writer.Flush()
	return nil
}

func (s *sseSink) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Writer.Flush()
	return nil
}

func (s *sseSink) Done() <-chan struct{} {
	return s.done
}
