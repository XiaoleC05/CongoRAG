package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
)

// TestSSEHeartbeat_WritesCommentWhenIdle 验证"长时间没有事件时连接不会被
// 反代/浏览器掐掉"：真实 HTTP 流上必须收到以 ":" 开头的 SSE 注释帧。
//
// 【为什么用 httptest.Server 而不是 ResponseRecorder】心跳是 goroutine 在写，
// 用 Recorder 读 Body 会和写入竞争（bytes.Buffer 不是并发安全的）。
// 走真实 HTTP 客户端按行读，既没有数据竞争，测的也确实是线路上发生的字节。
func TestSSEHeartbeat_WritesCommentWhenIdle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := sseHeartbeatInterval
	sseHeartbeatInterval = 20 * time.Millisecond
	defer func() { sseHeartbeatInterval = old }()

	router := gin.New()
	router.GET("/events", func(c *gin.Context) {
		sink := newSSESink(c)
		defer sink.Close()
		<-c.Request.Context().Done()
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	done := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		done <- line
	}()
	select {
	case line := <-done:
		require.Contains(t, line, ": keep-alive", "空闲连接应收到心跳注释帧")
	case <-time.After(3 * time.Second):
		t.Fatal("3 秒内没有收到心跳帧：长时间空闲的连接会被反向代理掐断")
	}
}

// TestSSEEmit_UpdatesLastWrite 验证"有数据在流时不发心跳"的判据：
// Emit 之后 lastWrite 必须前移，心跳因此跳过。
func TestSSEEmit_UpdatesLastWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/events", nil)

	sink := newSSESink(c)
	defer sink.Close()

	sink.mu.Lock()
	before := sink.lastWrite
	sink.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	require.NoError(t, sink.Emit(conversation.Event{
		ID: 7, Type: "token", Payload: []byte("{\"text\":\"hi\"}"),
	}))

	sink.mu.Lock()
	after := sink.lastWrite
	sink.mu.Unlock()
	require.True(t, after.After(before), "写入事件后 lastWrite 必须前移")

	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, "id: 7\nevent: token\ndata: "), "事件帧格式不能变：%q", body)
}

// TestSSESink_ConcurrentEmitAndHeartbeat 是对那把写锁的最直接证明：
// 心跳与事件同时写时，输出必须是完整帧的串接（不会出现半截帧交错）。
func TestSSESink_ConcurrentEmitAndHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := sseHeartbeatInterval
	sseHeartbeatInterval = time.Millisecond
	defer func() { sseHeartbeatInterval = old }()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/events", nil)
	sink := newSSESink(c)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = sink.Emit(conversation.Event{ID: int64(n), Type: "token", Payload: []byte("{\"n\":1}")})
		}(i)
	}
	wg.Wait()
	time.Sleep(20 * time.Millisecond)
	sink.Close()

	sink.mu.Lock()
	body := rec.Body.String()
	sink.mu.Unlock()

	// 每个 data 行都必须恰好是 {"n":1}：交错写会把它切坏。
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			require.Equal(t, "data: {\"n\":1}", line)
		}
	}
	require.Equal(t, 20, strings.Count(body, "event: token"))
}
