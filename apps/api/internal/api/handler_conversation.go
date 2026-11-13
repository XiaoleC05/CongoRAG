// 会话、消息与 SSE 端点：建/删/列会话、发消息（流式）、补发事件。
//
// 【SSE 的边界在本文件】写帧、断开后的续传、幂等重放都在这条路径上；
// 失败时必须用 writeFallbackError 写一个合法的 error 帧收场，不能直接
// AbortWithStatusJSON——响应头已经提交，追加一段 JSON 会把流写坏。
package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// CreateConversation 是唯一一个"看起来空"的 body 也能通过的端点：
// CreateConversationRequest 的字段都不是 required（契约里 title /
// knowledgeBaseId 都可选），所以空的 JSON 对象 `{}` 能绑成功。
// 前端 useCreateConversation 发的正是这种 body。
//
// 【这条为什么写在这里，不写进 bindJSON】它是这个请求类型自己的性质，
// 不是绑定策略的一部分——bindJSON 对十个端点做的事完全一样（body 完全为空、
// Content-Length: 0 时 json.Decode 返回 io.EOF，一律 400）。写进 bindJSON
// 的注释会变成隐藏行为：让人以为别处也允许空 body。
func (s *Server) CreateConversation(c *gin.Context) {
	var req CreateConversationRequest
	if !s.bindJSON(c, &req) {
		return
	}

	title := ""
	if req.Title != nil {
		title = *req.Title
	}

	conv, err := s.deps.Conversation.CreateConversation(c.Request.Context(), title, req.KnowledgeBaseId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAPIConversation(conv))
}

func (s *Server) DeleteConversation(c *gin.Context, id openapi_types.UUID) {
	if err := s.deps.Conversation.DeleteConversation(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) ListConversations(c *gin.Context, params ListConversationsParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	convs, next, err := s.deps.Conversation.ListConversations(c.Request.Context(), cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	// 空切片不是 nil：契约里 items 是数组，nil 会序列化成 null，
	// 前端拿到 null 去 .map() 直接崩（和 toAPIKBList 那条同一个约定）。
	out := make([]Conversation, 0, len(convs))
	for _, cv := range convs {
		out = append(out, toAPIConversation(cv))
	}
	c.JSON(http.StatusOK, ConversationPage{Items: out, NextCursor: nullableCursor(next)})
}

func (s *Server) ListConversationMessages(c *gin.Context, id openapi_types.UUID, params ListConversationMessagesParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	msgs, next, err := s.deps.Conversation.ListMessages(c.Request.Context(), id, cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, MessagePage{
		Items:      toAPIMessageList(msgs),
		NextCursor: nullableCursor(next),
	})
}

// SendMessage 是全项目唯一一个"成功路径不调用 s.fail"的 handler——
// 一旦请求体解析通过，响应就切换成 SSE 模式（newSSESink 会立刻写
// 200 + 响应头），之后 conversation.Usecase.Send 内部的任何错误都通过
// error 事件传给客户端（docs/sse-protocol.md），不再改变 HTTP 状态码。
//
// 【为什么请求体解析失败还能用 s.fail】那一步发生在 newSSESink 之前，
// 响应还没有被提交成 200，这时候返回一个正常的 400 Problem 是安全的。
//
// 【幂等键为什么在这里读，而不是做成中间件】这个键的作用域要包含会话 id
// （endpoint 字符串是 "POST /api/v1/conversations/<id>/messages"），而中间件
// 拿不到路由参数之外的东西；更要紧的是真正的裁决发生在
// WithConversationLock 的事务里（见 conversation.Usecase 的注释），中间件
// 两头都够不着。handler 只负责把头读出来并做长度校验。
func (s *Server) SendMessage(c *gin.Context, id openapi_types.UUID, params SendMessageParams) {
	var req SendMessageRequest
	if !s.bindJSON(c, &req) {
		return
	}

	idempotencyKey := ""
	if params.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(*params.IdempotencyKey)
	}
	// 【校验必须在 newSSESink 之前】sink 一构造出来，响应就切进 SSE 模式，
	// 那时再想返回 400 problem+json 已经来不及了。超长键属于"请求本身不合法"，
	// 用正常的 400 拒绝比在流里推一条 error 帧更准确。
	// 【按字符数算，不能按字节】契约写的是 maxLength: 255，那是字符数。
	// len(string) 是字节数——一个纯中文的键会在 86 个字符时就被拒掉，
	// 而它完全合法。仓库里其余长度校验（cleanTitle / maxNameLen 之类）
	// 也都是按 rune 数算的。
	if utf8.RuneCountInString(idempotencyKey) > conversation.MaxIdempotencyKeyLen {
		s.fail(c, fmt.Errorf("Idempotency-Key must not exceed %d characters: %w",
			conversation.MaxIdempotencyKeyLen, platform.ErrInvalid))
		return
	}

	sink := newSSESink(c)
	defer sink.Close()
	// Send 的返回值只用于日志和兜底——一旦切到 SSE 模式，错误已经通过
	// error 事件传给客户端了（conversation.Usecase.Send 自己保证这一点），
	// 这里不需要、也不能再对 c 做任何响应相关的操作。
	if err := s.deps.Conversation.Send(c.Request.Context(), id, req.Text, idempotencyKey, sink); err != nil {
		s.deps.Logger.Warn("send message failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"conversation_id", id, "error", err)
		s.writeFallbackError(sink, err)
	}
}

// writeFallbackError 是"失败必须对客户端可见"的最后一道保障。
//
// 【为什么必须有它】Send 的 error 事件是先持久化再发送的——emitEvent 要用
// NextEventID 分配 event_id，分配不到就提前返回。于是"数据库整个不可用"
// 这一类失败恰好会把那条 error 帧也堵在库里：客户端拿到的是 200 +
// text/event-stream + 零字节 body，和一个空的成功流完全同形——前端
// streamChat 正常 resolve、onError 从不触发，用户看到自己发的消息、
// 没有回答、也没有任何错误提示。同一时刻所有非 SSE 端点都在返回 500
// Problem，只有聊天这条路径是静默的。
//
// 【为什么按"还没发过 error 帧"判断，而不是"一个帧都没发过"】数据库在
// 流到一半时才坏掉的情况，客户端已经收到若干 token 却再也等不到
// error/done，连接就那么挂着——补一帧同样是对的。
func (s *Server) writeFallbackError(sink *sseSink, cause error) {
	if sink.errorFrameSent {
		return
	}

	status, typ, _ := classify(cause)
	detail := innermostMessage(cause)
	if status >= http.StatusInternalServerError {
		// 和 fail 对 5xx 的处理一致：不把内部错误原文（可能带 SQL 片段、
		// 表名、连接串）漏给客户端。
		detail = "服务内部错误"
	}

	// 这一帧不查库也不写库，是唯一能在数据库不可用时送达的通道——
	// 所以写失败除了让连接自己坏掉，没有别的补救。
	_ = sink.writeFallbackError(sseErrorData{Type: typ, Detail: detail})
}

func (s *Server) SubscribeConversationEvents(c *gin.Context, id openapi_types.UUID, params SubscribeConversationEventsParams) {
	var after int64
	if params.AfterEventId != nil {
		after = *params.AfterEventId
	}

	events, err := s.deps.Conversation.EventsAfter(c.Request.Context(), id, after)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	defer sink.Close()
	for _, ev := range events {
		if err := sink.Emit(conversation.Event{ID: ev.ID, Type: ev.Type, Payload: ev.Payload}); err != nil {
			// 客户端在补发历史事件的过程中断开了——没有更多可做的，
			// 停止继续写,让 handler 正常返回。
			return
		}
	}
	_ = sink.Flush()

	// 【补发完历史事件后直接结束这次响应,不保持连接等待新事件】
	// 真正的"持续推送新事件"只发生在 SendMessage 那条连接里——
	// 这个端点是断线续传的两段式设计里的"补发"那一段：客户端收完
	// 历史事件后,如果原来的生成还没结束,需要的是重新发起一次
	// SendMessage（幂等键会命中，见开发文档 M4-B）或者简单地再连一次
	// 这个端点看有没有新事件。M2 阶段生成的整个过程都在一次 SendMessage
	// 请求的生命周期内完成（没有后台任务在生成完之后继续产生事件），
	// 所以这个端点长期持有连接、等待新事件并没有实际意义——这一点
	// 到 M4-A Agent 引入之后（生成可能跨越更长时间、更需要断线续传）
	// 需要重新评估要不要在这里加"继续等待"的逻辑。
}
