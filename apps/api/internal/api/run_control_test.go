package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/agent"
	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
	"github.com/XiaoleC05/CongoRAG/internal/retrieval"
)

// 这一组测的是 v4.0 新增的三类端点在 **HTTP 边界**上的行为：
// run 级重订阅、取消、恢复，以及检索调试。
//
// 【假实现为什么可以只实现一两个方法】它们都内嵌了对应的 port 接口，
// 没实现的方法被调用时会在 nil 接口上 panic——测试写错了会立刻炸，
// 比静默返回零值好。这也是本仓库既有的做法（见 server_test.go 顶部的
// fakeKBRepo 注释）。

// ── run 维度：重订阅 / 取消 / 恢复 ──────────────────────────────

type fakeAgentRepo struct {
	agent.Repo

	run   *agent.Run
	steps []*agent.Step

	// events 是这条 run 已经落库的事件（按 event_id 升序）。
	events []agent.RunEvent

	// lastStatusUpdate 记下最后一次 CAS 的目标状态。
	lastStatusUpdate agent.RunStatus
	casErr           error
}

func (f *fakeAgentRepo) GetRun(ctx context.Context, q platform.Querier, id uuid.UUID) (*agent.Run, error) {
	if f.run == nil || f.run.ID != id {
		return nil, errBoom2{notFound: true}
	}
	return f.run, nil
}

func (f *fakeAgentRepo) StepsByRun(ctx context.Context, q platform.Querier, runID uuid.UUID) ([]*agent.Step, error) {
	return f.steps, nil
}

func (f *fakeAgentRepo) RunEventsAfter(ctx context.Context, q platform.Querier, runID uuid.UUID, after int64) ([]agent.RunEvent, error) {
	out := make([]agent.RunEvent, 0, len(f.events))
	for _, ev := range f.events {
		if ev.ID > after {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (f *fakeAgentRepo) UpdateRunStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to agent.RunStatus) error {
	if f.casErr != nil {
		return f.casErr
	}
	f.lastStatusUpdate = to
	f.run.Status = to
	return nil
}

// errBoom2 让 GetRun 返回一个 **platform.ErrNotFound 的直接包装**。
//
// 【不能返回裸 sentinel】这正是 server_test.go 里 fakeKBRepo 那段注释的口径：
// 生产路径上到达 handler 的错误总是被 usecase 包过一层，
// api 层所有关于 detail 的断言测的都是那个形状。裸 sentinel 会让
// innermostMessage 取不到"倒数第二层"。
type errBoom2 struct{ notFound bool }

func (e errBoom2) Error() string {
	if e.notFound {
		return "run: not found"
	}
	return "boom"
}
func (e errBoom2) Unwrap() error { return platform.ErrNotFound }

func newAgentTestUC(repo *fakeAgentRepo) *agent.Usecase {
	return agent.NewUsecase(
		repo, nil, nil, nil, nil, passthroughTxManager{},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)
}

func newRunControlRouter(repo *fakeAgentRepo, search *retrieval.Usecase, conv *conversation.Usecase) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agent:        newAgentTestUC(repo),
		Retrieval:    search,
		Conversation: conv,
		Knowledge: knowledge.NewUsecase(
			&fakeKBRepo{}, noopDocRepo{}, noopFileStore{}, noopEnqueuer{}, noopChunkIndexer{},
			noopFileCleaner{}, noopScheduler{}, passthroughTxManager{}, nil,
		),
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

func newRunningRun() *agent.Run {
	now := time.Now()
	return &agent.Run{
		ID: uuid.New(), AgentID: uuid.New(), Status: agent.RunRunning,
		Input: "算一下", StateSchemaVersion: agent.CurrentStateSchemaVersion,
		CreatedAt: now, UpdatedAt: now,
	}
}

// 补发事件：帧的 id / event / data 都要原样出来，而且**补完就结束响应**
// （不持有连接等新事件——与会话维度那条同一条语义）。
func TestSubscribeRunEvents_ReplaysPersistedFrames(t *testing.T) {
	run := newRunningRun()
	repo := &fakeAgentRepo{run: run, events: []agent.RunEvent{
		{ID: 1, Type: "run_started", Payload: json.RawMessage(`{"type":"run_started","data":{"runId":"x"}}`)},
		{ID: 2, Type: "token", Payload: json.RawMessage(`{"type":"token","data":{"text":"你好"}}`)},
	}}
	r := newRunControlRouter(repo, nil, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run.ID.String()+"/events", nil))

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "id: 1\nevent: run_started\n")
	assert.Contains(t, body, "id: 2\nevent: token\n")
	assert.Contains(t, body, `"text":"你好"`)
	// 【响应必须在这里结束】不会挂在那里等新事件——那正是"补发"与"实时推送"
	// 的区别，客户端不该指望这个端点帮它等后续内容。
	assert.Contains(t, w.Header().Get("Content-Type"), "text/event-stream")
}

// after_event_id 必须真的被透传（否则断线续传会把整条 run 重放一遍）。
func TestSubscribeRunEvents_HonoursAfterEventID(t *testing.T) {
	run := newRunningRun()
	repo := &fakeAgentRepo{run: run, events: []agent.RunEvent{
		{ID: 1, Type: "run_started", Payload: json.RawMessage(`{}`)},
		{ID: 2, Type: "token", Payload: json.RawMessage(`{}`)},
	}}
	r := newRunControlRouter(repo, nil, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/runs/"+run.ID.String()+"/events?after_event_id=1", nil))

	assert.NotContains(t, w.Body.String(), "id: 1\n", "游标之前的帧不该再发一遍")
	assert.Contains(t, w.Body.String(), "id: 2\n")
}

// run 不存在时必须是 404 Problem，而不是 200 + 空流——后者和"这条 run
// 没有任何事件"完全同形。
func TestSubscribeRunEvents_UnknownRun_Is404(t *testing.T) {
	r := newRunControlRouter(&fakeAgentRepo{}, nil, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+uuid.NewString()+"/events", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// 取消成功时返回的是**取消之后**的状态（不是取消前的快照）——
// 否则前端只能靠轮询判断"到底停下来没有"。
func TestCancelAgentRun_ReturnsPostCancelState(t *testing.T) {
	run := newRunningRun()
	repo := &fakeAgentRepo{run: run}
	r := newRunControlRouter(repo, nil, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID.String()+"/cancel", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, agent.RunCancelled, repo.lastStatusUpdate)

	var got AgentRun
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, AgentRunStatus("cancelled"), got.Status)
}

// 终态的 run 不可取消：状态机是权威判据（issue #59），映射成 409。
func TestCancelAgentRun_TerminalRun_Is409(t *testing.T) {
	run := newRunningRun()
	run.Status = agent.RunCompleted
	r := newRunControlRouter(&fakeAgentRepo{run: run}, nil, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID.String()+"/cancel", nil))

	require.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"type":"conflict"`)
}

// 【恢复的三道拒绝必须是正常的 409 Problem，不是流里一帧】
// 这一条钉的是"先 PrepareResume 再开流"这个分两步的设计：一旦打开 SSE，
// 状态码就锁死在 200 上，客户端再也没法按 type 分支。
func TestResumeAgentRun_RefusalsAreProblemsNotFrames(t *testing.T) {
	cases := []struct {
		name     string
		run      func() *agent.Run
		wantType string
		steps    []*agent.Step
	}{
		{
			name: "终态不可恢复",
			run: func() *agent.Run {
				r := newRunningRun()
				r.Status = agent.RunCompleted
				return r
			},
			wantType: "conflict",
		},
		{
			name: "快照版本不兼容",
			run: func() *agent.Run {
				r := newRunningRun()
				r.Status = agent.RunInterrupted
				r.StateSchemaVersion = agent.CurrentStateSchemaVersion + 1
				return r
			},
			wantType: "state_schema_version_mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := tc.run()
			r := newRunControlRouter(&fakeAgentRepo{run: run, steps: tc.steps}, nil, nil)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID.String()+"/resume", nil))

			require.Equal(t, http.StatusConflict, w.Code,
				"拒绝必须发生在切进 SSE 模式之前——进了流就只能推一帧笼统的 error")
			assert.Contains(t, w.Body.String(), `"type":"`+tc.wantType+`"`)
			assert.Contains(t, w.Header().Get("Content-Type"), "application/problem+json")
		})
	}
}

// ── 检索调试（issue #77）───────────────────────────────────────

// fakeChunkRepo 只实现 Search（其余方法内嵌的接口会 panic）。
type fakeChunkRepo struct {
	retrieval.Repo
	chunks []domain.Chunk
}

func (f *fakeChunkRepo) Search(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
	return f.chunks, nil
}

// fakeEmbedConfigRepo / fakeEmbedderRegistry / fakeEmbedder 让检索那条路径
// 走完"把 query 变成向量"，而不需要联网、也不需要真的配一个 provider。
//
// 【三个都内嵌了接口】只实现这条路真正会用到的那几个方法，其余被调用时
// 会在 nil 接口上 panic——测试写错会立刻炸。
type fakeEmbedConfigRepo struct {
	llm.ConfigRepo
	model *llm.Model
}

func (f *fakeEmbedConfigRepo) ListModels(ctx context.Context, q platform.Querier) ([]*llm.Model, error) {
	return []*llm.Model{f.model}, nil
}

type fakeEmbedderRegistry struct {
	llm.Registry
}

func (f *fakeEmbedderRegistry) Embedder(ctx context.Context, modelID string) (llm.Embedder, error) {
	return fakeEmbedder{}, nil
}

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (fakeEmbedder) Dim() int { return 3 }

// newSearchUsecase 造一个"能真的算出一个向量"的检索 Usecase。
//
// 【为什么必须给它一个 embedding 模型】Usecase.Search 的第一步就是把 query
// 变成向量——那条路径走不到的话，测的就不是检索端点，而只是一次参数校验。
func newSearchUsecase(chunks []domain.Chunk) *retrieval.Usecase {
	embedModel := &llm.Model{ID: uuid.New(), ModelID: "test-embed", Kind: llm.KindEmbedding}
	return retrieval.NewUsecase(
		&fakeChunkRepo{chunks: chunks},
		&fakeEmbedderRegistry{},
		&fakeEmbedConfigRepo{model: embedModel},
		nil,
	)
}

// 命中的字段必须与 SSE citation 事件逐个对应（chunkId/documentId/filename/
// snippet/score），前端才能复用同一个展示组件。
func TestSearchKnowledgeBase_MapsHits(t *testing.T) {
	kb := &knowledge.KB{ID: uuid.New(), Name: "库"}
	docID, chunkID := uuid.New(), uuid.New()
	search := newSearchUsecase([]domain.Chunk{{
		ID: chunkID, DocumentID: docID, Filename: "a.pdf", Content: "片段", Score: 0.87,
	}})
	// Knowledge.Get 要先通过，才轮得到检索。
	r := newSearchRouter(&fakeKBRepo{kbs: []*knowledge.KB{kb}}, search)

	body := strings.NewReader(`{"query":"向量检索","topK":3}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+kb.ID.String()+"/search", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got KnowledgeSearchResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Hits, 1)
	assert.Equal(t, chunkID, got.Hits[0].ChunkId)
	assert.Equal(t, docID, got.Hits[0].DocumentId)
	assert.Equal(t, "a.pdf", got.Hits[0].Filename)
	assert.Equal(t, "片段", got.Hits[0].Snippet)
	assert.InDelta(t, 0.87, got.Hits[0].Score, 0.0001)
}

// 没有命中时必须是**空数组**不是 null：契约里 hits 是数组，
// 前端拿到 null 去 .map() 直接崩（和 toAPIKBList 那条同一个约定）。
func TestSearchKnowledgeBase_NoHits_IsEmptyArrayNotNull(t *testing.T) {
	kb := &knowledge.KB{ID: uuid.New(), Name: "库"}
	r := newSearchRouter(&fakeKBRepo{kbs: []*knowledge.KB{kb}}, newSearchUsecase(nil))

	body := strings.NewReader(`{"query":"不存在的东西"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+kb.ID.String()+"/search", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"hits":[]`)
	assert.NotContains(t, w.Body.String(), `"hits":null`)
}

// topK 越界返回 400，不静默夹取——调试视图上"只命中这么多"与
// "被服务端截断"是两件必须分得开的事。
func TestSearchKnowledgeBase_TopKOutOfRange_Is400(t *testing.T) {
	kb := &knowledge.KB{ID: uuid.New(), Name: "库"}
	r := newSearchRouter(&fakeKBRepo{kbs: []*knowledge.KB{kb}}, newSearchUsecase(nil))

	for _, topK := range []int{0, 51} {
		body := strings.NewReader(`{"query":"x","topK":` + itoa(topK) + `}`)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+kb.ID.String()+"/search", body)
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code, "topK=%d 应当被拒", topK)
		assert.Contains(t, w.Body.String(), `"type":"invalid_argument"`)
	}
}

// 知识库不存在时是 404，不是一个空结果。
func TestSearchKnowledgeBase_UnknownKB_Is404(t *testing.T) {
	r := newSearchRouter(&fakeKBRepo{}, newSearchUsecase(nil))

	body := strings.NewReader(`{"query":"x"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+uuid.NewString()+"/search", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newSearchRouter(kb knowledge.KBRepo, search *retrieval.Usecase) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Knowledge: knowledge.NewUsecase(
			kb, noopDocRepo{}, noopFileStore{}, noopEnqueuer{}, noopChunkIndexer{},
			noopFileCleaner{}, noopScheduler{}, passthroughTxManager{}, nil,
		),
		Retrieval: search,
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

// ── 会话列表（issue #78）───────────────────────────────────────

// fakeConvRepo 只实现 ListConversations，其余方法由内嵌的接口兜住。
type fakeConvRepo struct {
	conversation.Repo
	convs []*conversation.Conversation
}

func (f *fakeConvRepo) ListConversations(ctx context.Context, q platform.Querier, cur *platform.ListCursor, limit int) ([]*conversation.Conversation, bool, error) {
	return f.convs, false, nil
}

// 列表端点必须沿用 v3.0 那条分页信封（items + nextCursor），不是裸数组——
// 两种形状并存会让前端多一套解析。
func TestListConversations_UsesPageEnvelope(t *testing.T) {
	conv := &conversation.Conversation{
		ID: uuid.New(), Title: "关于向量检索", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	uc := conversation.NewUsecase(&fakeConvRepo{convs: []*conversation.Conversation{conv}}, nil, nil, nil, nil, nil, nil, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Conversation: uc})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var got ConversationPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Items, 1)
	assert.Equal(t, "关于向量检索", got.Items[0].Title)
	assert.Nil(t, got.NextCursor, "没有下一页时是 null，前端据此收起「加载更多」")
}

// 没有任何会话时 items 必须是空数组不是 null——同一条约定。
func TestListConversations_Empty_IsEmptyArray(t *testing.T) {
	uc := conversation.NewUsecase(&fakeConvRepo{}, nil, nil, nil, nil, nil, nil, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Conversation: uc})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/conversations", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"items":[]`)
}
