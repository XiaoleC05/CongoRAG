// Agent 端点：配置 CRUD、工具目录、运行启动/取消/恢复、步骤与事件流。
//
// 【执行不在这里】一次 run 的落库与执行都发生在 internal/agent；HTTP 层只负责
// 校验参数、把请求交给 usecase、把取消/恢复转成业务调用、以及把已记录的事件流
// 补发给中途接入的客户端。这里不做重试、不做工具调用——那会让"接口语义"和
// "执行语义"混在一层，恢复时无法判定哪些步骤已经执行过。
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

func (s *Server) ListToolCatalog(c *gin.Context) {
	entries, err := s.deps.Agent.ListToolCatalog(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIToolCatalog(entries))
}

func (s *Server) ListAgents(c *gin.Context) {
	agents, err := s.deps.Agent.ListAgents(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentList(agents))
}

func (s *Server) CreateAgent(c *gin.Context) {
	var req CreateAgentRequest
	if !s.bindJSON(c, &req) {
		return
	}

	description, instruction := "", ""
	if req.Description != nil {
		description = *req.Description
	}
	if req.Instruction != nil {
		instruction = *req.Instruction
	}
	var toolNames []string
	if req.ToolNames != nil {
		toolNames = *req.ToolNames
	}

	a, err := s.deps.Agent.CreateAgent(c.Request.Context(), req.Name, description, instruction, toolNames)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAPIAgent(a))
}

func (s *Server) GetAgent(c *gin.Context, id openapi_types.UUID) {
	a, err := s.deps.Agent.GetAgent(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgent(a))
}

// UpdateAgent 改一个 Agent 的配置（issue #81）。
//
// 【和 CreateAgent 一样是 JSON 端点，不是 SSE】改配置没有流可推。
// 校验（名称长度、工具已注册、工具能力门控）全在 usecase 里，与创建共用
// 同一份——见 agent.Usecase.validateAgentFields 的注释。
func (s *Server) UpdateAgent(c *gin.Context, id openapi_types.UUID) {
	var req UpdateAgentRequest
	if !s.bindJSON(c, &req) {
		return
	}

	a, err := s.deps.Agent.UpdateAgent(c.Request.Context(), id,
		req.Name, req.Description, req.Instruction, req.ToolNames)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgent(a))
}

func (s *Server) ListAgentRuns(c *gin.Context, id openapi_types.UUID, params ListAgentRunsParams) {
	limit, cursor, ok := s.parseListParams(c, params.Limit, params.Cursor)
	if !ok {
		return
	}

	runs, next, err := s.deps.Agent.ListRuns(c.Request.Context(), id, cursor, limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, AgentRunPage{
		Items:      toAPIAgentRunList(runs),
		NextCursor: nullableCursor(next),
	})
}

// StartAgentRun 和 SendMessage 同一个模式：请求体解析失败还能用
// s.fail（还没切到 SSE 模式),之后 agent.Usecase.Start 内部的任何
// 错误都通过 error 事件传给客户端,不再改变 HTTP 状态码。
func (s *Server) StartAgentRun(c *gin.Context, id openapi_types.UUID, params StartAgentRunParams) {
	var req StartAgentRunRequest
	if !s.bindJSON(c, &req) {
		return
	}

	idempotencyKey := ""
	if params.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(*params.IdempotencyKey)
	}
	// 和 SendMessage 同一条判据：校验必须在 newSSESink 之前，超长键是
	// "请求本身不合法"，用 400 拒绝比在流里推一帧更准确；长度按字符数算
	// （契约写的是 maxLength: 255，那是字符数）。
	if utf8.RuneCountInString(idempotencyKey) > conversation.MaxIdempotencyKeyLen {
		s.fail(c, fmt.Errorf("Idempotency-Key must not exceed %d characters: %w",
			conversation.MaxIdempotencyKeyLen, platform.ErrInvalid))
		return
	}

	sink := newSSESink(c)
	defer sink.Close()
	if _, err := s.deps.Agent.Start(c.Request.Context(), id, req.Input, idempotencyKey, sink); err != nil {
		s.deps.Logger.Warn("agent run failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"agent_id", id, "error", err)
		// 【和 SendMessage 一样要有兜底帧】失败事件是先持久化再发的，
		// 所以"数据库整个不可用"会把那条 error 帧也堵在库里——客户端
		// 拿到的是 200 + 空 body，和一个空的成功流完全同形（那条注释
		// 在 writeFallbackError 上写得更细）。
		s.writeFallbackError(sink, err)
	}
}

func (s *Server) ListRunSteps(c *gin.Context, runId openapi_types.UUID) {
	steps, err := s.deps.Agent.ListSteps(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentRunStepList(steps))
}

// SubscribeRunEvents 和会话维度那条 SubscribeConversationEvents 逐条同形：
// 补发完已有的历史就结束响应，不持有连接等新事件。
func (s *Server) SubscribeRunEvents(c *gin.Context, runId openapi_types.UUID, params SubscribeRunEventsParams) {
	var after int64
	if params.AfterEventId != nil {
		after = *params.AfterEventId
	}

	// 【为什么这里先查一次 run】它让"这条 run 不存在"在切进 SSE 模式之前
	// 就以 404 Problem 返回，而不是给客户端一条 200 + 空流——后者与
	// "这条 run 没有任何事件"完全同形。
	events, err := s.deps.Agent.RunEvents(c.Request.Context(), runId, after)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	defer sink.Close()
	for _, ev := range events {
		if err := sink.Emit(conversation.Event{ID: ev.ID, Type: ev.Type, Payload: ev.Payload}); err != nil {
			// 客户端在补发过程中断开了——没有更多可做的，停止继续写。
			return
		}
	}
	_ = sink.Flush()
}

// CancelAgentRun 取消一次在途运行（issue #55）。
//
// 【它是普通 JSON 端点，不是 SSE】取消的结果是"这条 run 现在是什么状态"，
// 一次性就答完了；响应里带的是**取消生效之后**的记录（Usecase.Cancel 会
// 等收尾写入落库），所以前端不需要再轮询一次。
func (s *Server) CancelAgentRun(c *gin.Context, runId openapi_types.UUID) {
	run, err := s.deps.Agent.Cancel(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toAPIAgentRun(run))
}

// ResumeAgentRun 从断点恢复一次被中断的运行（issue #64）。
//
// 【为什么先 PrepareResume 再开流】恢复会被拒绝的理由有三种，而它们都该
// 是正常的 409 Problem（前端按 type 给出不同的下一步提示）。一旦打开 SSE，
// 状态码就锁死在 200 上，那时只能推一帧笼统的 error——这正是 issue #34
// 修过的那类误报。所以校验和推流分成两步。
func (s *Server) ResumeAgentRun(c *gin.Context, runId openapi_types.UUID) {
	plan, err := s.deps.Agent.PrepareResume(c.Request.Context(), runId)
	if err != nil {
		s.fail(c, err)
		return
	}

	sink := newSSESink(c)
	defer sink.Close()
	if _, err := s.deps.Agent.Resume(c.Request.Context(), plan, sink); err != nil {
		s.deps.Logger.Warn("agent run resume failed",
			"request_id", platform.RequestIDFrom(c.Request.Context()),
			"run_id", runId, "error", err)
		s.writeFallbackError(sink, err)
	}
}
