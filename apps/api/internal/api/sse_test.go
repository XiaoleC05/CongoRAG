package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// 这一组测的是"切到 SSE 之后，失败还能不能传到客户端"。
//
// 【为什么要走真实的 conversation.Usecase】这个 handler 的失败语义完全
// 由 Send 决定——Send 的 error 事件先持久化再发送，所以"数据库不可用"
// 恰好是那条帧发不出去的唯一一类失败。把 Send 换成假的就测不到这件事了，
// 这里用一个所有查询都失败的 Repo 来制造它。

// dbDownRepo 模拟"数据库整个不可用"：Send 走到第一步就失败，随后 Send
// 自己那条 error 事件也因为没有 event_id 而发不出去。
//
// 【为什么要嵌一个 nil 的 conversation.Repo】只为了满足接口——本测试的
// 流程在 GetConversation 和 NextEventID 两处就结束了，其余方法永远不该
// 被调用（真被调用会 panic，这正是想要的：说明流程走偏了）。
type dbDownRepo struct {
	conversation.Repo
}

var errDBDown = errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")

func (dbDownRepo) GetConversation(ctx context.Context, q platform.Querier, id uuid.UUID) (*conversation.Conversation, error) {
	return nil, errDBDown
}

func (dbDownRepo) NextEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	return 0, errDBDown
}

// newDBDownRouter 装一个 Conversation 指向"数据库不可用"的真实 Usecase。
// 其余依赖（registry、llmRepo、ctxmgr…）传 nil：流程到不了它们。
func newDBDownRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Conversation: conversation.NewUsecase(dbDownRepo{}, nil, nil, nil, nil, nil, nil, nil),
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

// 数据库不可用时，POST /conversations/{id}/messages 必须让客户端看到失败。
//
// 【回归的是"静默的空流"】这个端点先写 200 + text/event-stream 再调 Send，
// 而 Send 的 error 事件要先落库才能发——数据库不可用时它发不出来，客户端
// 拿到的是 200 + 零字节 body。前端把这种响应当成一个正常但空的流：不报错、
// 不提示，用户只看到"回答是空的"，其实连消息都没落库。修法是在 handler
// 这一层补一条不碰数据库的兜底 error 帧。
func TestSendMessage_DatabaseDown_EmitsFallbackErrorFrame(t *testing.T) {
	r := newDBDownRouter()

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/conversations/"+uuid.NewString()+"/messages",
		strings.NewReader(`{"text":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// 状态码仍然是 200：newSSESink 在调 Send 之前就已经写下了它，
	// 这是这个端点的既有契约（docs/sse-protocol.md），不是这次要改的事。
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")

	body := w.Body.String()
	require.NotEmpty(t, body, "失败必须有帧发出来，不能是零字节的 body")

	name, data, ok := lastSSEFrame(body)
	require.True(t, ok, "body 里应该有至少一帧完整的 SSE 帧：%q", body)
	assert.Equal(t, "error", name)
	assert.Contains(t, data, `"type":"internal_error"`)
	assert.NotContains(t, data, "connection refused",
		"5xx 不能把内部错误原文（这里是一条真实的数据库报错）漏给客户端")
}

// 兜底帧不该带 id 字段：它在 conversation_events 里没有行，而 SSE 规范里
// 带 id 的帧会推进客户端的 lastEventId——写个 0 会把续传游标退回到起点。
func TestSendMessage_FallbackErrorFrameHasNoEventID(t *testing.T) {
	r := newDBDownRouter()

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/conversations/"+uuid.NewString()+"/messages",
		strings.NewReader(`{"text":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	require.NotEmpty(t, body)
	assert.NotContains(t, body, "id: ",
		"兜底帧没有对应的 event_id，不能编一个出来：%q", body)
}

// Send 已经把 error 帧投递出去过时，兜底不能再补一条——两条 error 事件
// 会让客户端把同一个失败提示两遍。
func TestWriteFallbackError_DoesNotDuplicateDeliveredErrorFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/conversations/x/messages", nil)
	sink := newSSESink(c)

	// 模拟 Send 自己那条 error 帧（它走的是 Emit，带真实 event_id）。
	require.NoError(t, sink.Emit(conversation.Event{
		ID:      7,
		Type:    eventErrorName,
		Payload: []byte(`{"type":"error","data":{"type":"upstream_llm_error","detail":"boom"}}`),
	}))

	(&Server{}).writeFallbackError(sink, errors.New("上游连接断了"))

	assert.Equal(t, 1, strings.Count(w.Body.String(), "event: "+eventErrorName+"\n"),
		"已经发过 error 帧时不该再补一条：%q", w.Body.String())
}

// lastSSEFrame 从响应体里取出最后一帧的 event 名和 data 行。
// 只够这一组测试用——完整的帧解析器在 web/src/lib/streamChat.ts。
func lastSSEFrame(body string) (name, data string, ok bool) {
	for _, f := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(f) == "" {
			continue
		}
		name, data, ok = "", "", true
		for _, line := range strings.Split(f, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
	}
	return name, data, ok
}
