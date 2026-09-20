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
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
)

var _ conversation.EventSink = (*sseSink)(nil)

// sseSink 包着一个 *gin.Context，把 conversation.Event 写成 SSE 帧。
type sseSink struct {
	c    *gin.Context
	done <-chan struct{}
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

	return &sseSink{c: c, done: c.Request.Context().Done()}
}

// Emit 按 docs/sse-protocol.md 定义的帧格式写一条事件。
//
// 【为什么不用 gin 自带的 c.SSEvent】那个辅助方法把 data 字段用 JSON
// 编码器现写一遍，会导致 payload 已经是 []byte 时被二次编码成一个
// JSON 字符串（多一层转义）。这里的 Event.Payload 已经是
// conversation.Usecase 组装好的完整 JSON,直接原样写出去。
func (s *sseSink) Emit(ev conversation.Event) error {
	_, err := fmt.Fprintf(s.c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.Payload)
	if err != nil {
		return fmt.Errorf("write sse frame: %w", err)
	}
	return nil
}

func (s *sseSink) Flush() error {
	s.c.Writer.Flush()
	return nil
}

func (s *sseSink) Done() <-chan struct{} {
	return s.done
}
