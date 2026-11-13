package api

// 这组 benchmark 覆盖 SSE 写帧的常数成本：单帧一次、以及多 goroutine 争用
// 同一把写锁时的量级。它们只依赖 gin 的测试上下文与一个丢弃型 ResponseWriter，
// 不碰网络、不碰数据库、不需要模型——所以能放进 make bench / CI，不会变成
// "只有某个人的机器能跑"的基准。
//
// 【为什么要测并发那条】v6.4.0 起同一个 sseSink 上多了一个心跳 goroutine，
// 所有写入口共用一把 sync.Mutex（见 sse.go 的注释）。单帧 benchmark 量的是
// 格式化+锁成本；并发 benchmark 量的是"锁竞争把成本放大了多少"——两条一起看，
// 才能判断将来往 sink 上加更多写者（例如分片批量发送）是否值得。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
)

// benchResponseWriter 是只数不存的 ResponseWriter。
//
// 【为什么不能用 httptest.ResponseRecorder】Recorder 会把每一帧都留在内存里，
// b.N 上百万时测出来的会是"内存分配与拷贝"，而不是"写一帧"的常数。这里只
// 累加长度；Flush 提供空实现是为了满足 http.Flusher——sseSink 的写入口会调它。
type benchResponseWriter struct {
	bytes int64
}

func (w *benchResponseWriter) Header() http.Header { return http.Header{} }

func (w *benchResponseWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	return len(p), nil
}

func (w *benchResponseWriter) WriteHeader(int) {}

func (w *benchResponseWriter) Flush() {}

// newBenchSink 造一个可写、可关闭的 sink。
//
// 【为什么把心跳关掉】心跳 goroutine 会周期性往同一个 writer 写注释帧；
// 虽然它只在空闲超过一个间隔时才写（默认 15s），但 benchmark 的 -benchtime
// 可以被人调到 10s 以上，那时噪声就会进测量。这里显式把间隔设成 0
// （startHeartbeat 对 <=0 直接返回），基准只测事件帧路径。
func newBenchSink(b *testing.B) (*sseSink, *benchResponseWriter) {
	b.Helper()
	gin.SetMode(gin.TestMode)

	oldInterval := sseHeartbeatInterval
	sseHeartbeatInterval = 0
	b.Cleanup(func() { sseHeartbeatInterval = oldInterval })

	w := &benchResponseWriter{}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/bench/sse", nil)

	sink := newSSESink(c)
	b.Cleanup(sink.Close)
	return sink, w
}

// benchEvent 是一个贴近真实线路的 token 帧：它的 payload 与 conversation
// 包 emitEvent 写出去的形状一致（docs/sse-protocol.md「事件类型」）。
func benchEvent() conversation.Event {
	return conversation.Event{
		ID:      42,
		Type:    "token",
		Payload: []byte(`{"type":"token","data":{"text":"检索到的证据片段"}}`),
	}
}

// BenchmarkSSEEmit 量"写一帧"的常数：拼接 id/event/data、拿锁、写 writer。
//
// 它回答的问题是：给 SSE 加字段、改帧格式时，每帧的固定成本有没有被改坏。
func BenchmarkSSEEmit(b *testing.B) {
	sink, w := newBenchSink(b)
	ev := benchEvent()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Emit(ev); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
	b.StopTimer()
	if w.bytes == 0 {
		b.Fatal("writer 没有收到任何字节：Emit 没有写到响应")
	}
}

// BenchmarkSSESinkConcurrent 量 8 / 32 个 goroutine 同时 Emit 时的锁竞争量级。
//
// 【怎么读结果】ns/op 是"总耗时 / b.N"，所以它天然含并发度；两条子基准的
// ns/op 差距就是"锁竞争把每帧成本放大了多少"。allocs/op 应该与单帧基准同量级
// （Emit 路径不该因为并发多分配）。
func BenchmarkSSESinkConcurrent(b *testing.B) {
	ev := benchEvent()
	for _, workers := range []int{8, 32} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			sink, w := newBenchSink(b)

			b.ReportAllocs()
			b.ResetTimer()
			var wg sync.WaitGroup
			// 把 b.N 均分给 workers；余数分给前几个 goroutine，保证总次数恰好 b.N。
			per := b.N / workers
			extra := b.N % workers
			for wkr := 0; wkr < workers; wkr++ {
				n := per
				if wkr < extra {
					n++
				}
				if n == 0 {
					continue
				}
				wg.Add(1)
				go func(n int) {
					defer wg.Done()
					for i := 0; i < n; i++ {
						if err := sink.Emit(ev); err != nil {
							b.Errorf("emit: %v", err)
							return
						}
					}
				}(n)
			}
			wg.Wait()
			b.StopTimer()
			if w.bytes == 0 {
				b.Fatal("writer 没有收到任何字节：并发 Emit 没有写到响应")
			}
		})
	}
}
