package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// apiDrainTimeout 是 api 排空在途请求的预算。
//
// 【为什么是 30 秒，而不是 worker 那个 30 分钟】30 分钟是单份文档处理的
// 上限（internal/knowledge/river.go 的 documentProcessingTimeout），那是
// worker 的事，跟 api 无关：api 关停时要做的收尾只有一次 messages UPDATE、
// 一次 conversation_events INSERT 和一次 socket 写，在回环地址上是毫秒级。
// 30 秒是「兜底上限」，不是「预计耗时」。
//
// 【与 SSE 心跳的关系】docs/sse-protocol.md 声明服务端每 15 秒发一条
// 注释行保活，但那个定时器在 apps/api/internal/api/sse.go 里并不存在
// （全仓没有任何 time.Ticker）。30 秒正好是该间隔的两倍，量级上够用；
// 将来真把心跳实现出来，这个值也不需要跟着动。
//
// 【不放进 platform.Config】它不是部署配置，是进程语义——换一台机器、
// 换一个部署形态都不该改变它。
const apiDrainTimeout = 30 * time.Second

// drain 编排一次 api 关停：先拒新请求，再取消在途请求，最后在超时后强关连接。
//
// 返回非 nil 表示优雅排空没有在 timeout 内完成，连接已被强制关闭。
//
// 【抽成函数是为了能被测】关停是全进程唯一一处「跑错了不报错、只在
// 退出时表现为卡住」的逻辑，没法靠读代码验收。
func drain(httpSrv *http.Server, cancelRoot context.CancelFunc, timeout time.Duration, logger *slog.Logger) error {
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), timeout)
	defer cancelDrain()

	// 先在后台关掉监听（拒新请求），紧接着取消在途请求的 ctx。
	//
	// 【顺序有意为之】Shutdown 立刻关闭 listener，但会一直等活跃 handler
	// 返回。如果反过来先 cancelRoot，这两步之间那个微秒级窗口里被接受的
	// 请求会拿到一个已经取消的 ctx（表现为 500，而不是连接被拒）。
	// 窗口极小，但技术方案 §三 把「拒新请求」排在「排空 SSE」之前，照做。
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- httpSrv.Shutdown(drainCtx) }()

	// 【这一步就是 SSE 的排空机制，sse.go 一行都不用改】
	// sseSink.Done() 返回的正是 c.Request.Context().Done()，而装配根给
	// http.Server 设了 BaseContext 把每个请求的 ctx 都挂在 rootCtx 上，
	// 所以 cancelRoot() 会让所有在途 handler 立刻观察到取消。
	//
	// 【收尾写入不会因此丢失】conversation 的 failMessage（把已生成的内容
	// 落成 failed 终态）和 Send 外层补发 error 帧都用 context.Background()
	// 写库；agent 侧所有写库走 detachedWriteCtx。所以取消请求 ctx 不会把
	// 半截回答永久留在 streaming 状态。
	cancelRoot()

	if err := <-shutdownDone; err != nil {
		// 【为什么必须有 Close 兜底】sse.go 的 Emit 是直接往 ResponseWriter
		// 写（fmt.Fprintf）。客户端的 TCP 接收窗口满的时候，这次写会阻塞，
		// 而且**不受 ctx 取消影响**——handler 因此不会返回，Shutdown 只能
		// 干等到 drainCtx 到期。没有 Close 的话，关停会从「30 秒」变成
		// 「永不返回」，用户只能强杀进程。
		//
		// 同一处兜底也覆盖 Eino 那条链：Recv() 不接受 ctx，它能否被唤醒
		// 取决于上游 HTTP 客户端是否响应取消，本仓看不到。
		_ = httpSrv.Close()
		return err
	}
	return nil
}
