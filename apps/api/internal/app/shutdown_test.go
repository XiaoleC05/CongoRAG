package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newDrainTestServer 造一个和装配根同构的 server：BaseContext 把请求 ctx
// 挂在 rootCtx 上，于是 cancelRoot() 就等于「让所有在途 handler 观察到取消」。
//
// 用 httptest.NewUnstartedServer 而不是自己 Listen + Serve：它的 Config
// 字段在 Start() 之前可以改，正是为了能塞进 BaseContext。
func newDrainTestServer(t *testing.T, rootCtx context.Context, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.BaseContext = func(net.Listener) context.Context { return rootCtx }
	ts.Start()
	return ts
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// 在途 handler 只认 ctx 取消（这正是 SSE handler 的形状：sseSink.Done()
// 返回 c.Request.Context().Done()）。drain 必须靠 cancelRoot() 叫醒它，
// 而不是干等到超时。
//
// 【这条测试真的会红】把 drain 里的 cancelRoot() 注释掉：handler 会一直
// 阻塞到自己的兜底计时器，Shutdown 只能等满 drainCtx，drain 返回
// DeadlineExceeded，下面第一个断言就失败。
//
// handler 里的兜底计时器不是为了让测试通过——它保证回归时这条用例是
// **干净地失败**（0.2 秒）而不是把整个测试二值挂死到 go test 的超时。
func TestDrain_CancelsInFlightHandlerAndReturnsBeforeTimeout(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	const safetyRelease = 20 * time.Second

	handlerEntered := make(chan struct{})
	cancelled := make(chan struct{})

	ts := newDrainTestServer(t, rootCtx, func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(safetyRelease):
		}
	})
	defer ts.Close()

	go func() {
		resp, err := http.Get(ts.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-handlerEntered

	start := time.Now()
	err := drain(ts.Config, cancelRoot, 200*time.Millisecond, discardLogger())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("drain 返回了错误，说明在途 handler 不是被取消叫醒的: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("drain 花了 %v（几乎等于超时值），它是在等 drainCtx 到期，不是在排空", elapsed)
	}

	select {
	case <-cancelled:
	default:
		t.Fatal("drain 已经返回，但在途 handler 还没观察到取消")
	}
}

// handler 故意无视 ctx（模拟 Emit 往一个接收窗口已满的 socket 写：那次写
// 会阻塞，而且不受 ctx 取消影响）。这时 Shutdown 到点也等不到 handler
// 返回，drain 必须走 Close() 把连接掐掉——否则关停就是「永不返回」。
//
// 【这条测试钉的是 Close，不是超时返回】Shutdown 自己到点就会返回
// DeadlineExceeded，所以只断言 drain 报错是钉不住 Close 的：把
// `_ = httpSrv.Close()` 注释掉，drain 依然报同一个错。真正能区分的是
// **客户端的连接有没有被掐断**——下面那条断言就是为此写的。
func TestDrain_ForceClosesConnectionWhenHandlerIgnoresCancellation(t *testing.T) {
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	release := make(chan struct{})
	handlerEntered := make(chan struct{})

	ts := newDrainTestServer(t, rootCtx, func(w http.ResponseWriter, r *http.Request) {
		close(handlerEntered)
		<-release // 无视 r.Context().Done()
	})
	// 【defer 顺序要紧】LIFO：close(release) 先跑，ts.Close() 后跑。
	// httptest 的 Close 会等所有在途请求结束，反过来的话这里会死锁。
	defer ts.Close()
	defer close(release)

	clientResult := make(chan error, 1)
	go func() {
		resp, err := http.Get(ts.URL)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		clientResult <- err
	}()

	<-handlerEntered

	err := drain(ts.Config, cancelRoot, 200*time.Millisecond, discardLogger())
	if err == nil {
		t.Fatal("handler 无视取消时 drain 应返回超时错误，实际返回 nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 DeadlineExceeded（来自 Shutdown 的 drainCtx），实际 %v", err)
	}

	// handler 还阻塞在 release 上、什么响应都没写；客户端能结束，只可能
	// 是服务端把连接关了。
	select {
	case cerr := <-clientResult:
		if cerr == nil {
			t.Fatal("客户端请求正常结束了（拿到了完整响应），说明连接没有被强制关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain 已经返回，但客户端连接还开着——Close 兜底没有生效")
	}
}
