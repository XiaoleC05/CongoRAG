package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 重新索引端点的 HTTP 边界（issue #39）
// ════════════════════════════════════════════════════════════════

// recordingDocRepo 在 noopDocRepo 之上只覆盖重新索引这条路径会走到的方法。
//
// 【为什么内嵌哑实现】这个文件测的是 HTTP 边界（状态码、响应体形状），
// 不是业务语义——业务语义在 internal/knowledge/reindex_test.go 里。
// 内嵌之后只需要写真正相关的那几个方法。
type recordingDocRepo struct {
	noopDocRepo

	doc      *knowledge.Document
	markErr  error
	kbDocIDs []uuid.UUID
}

func (r *recordingDocRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*knowledge.Document, error) {
	if r.doc == nil {
		return nil, platform.ErrNotFound
	}
	return r.doc, nil
}

func (r *recordingDocRepo) MarkForReindex(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	return r.markErr
}

func (r *recordingDocRepo) MarkKnowledgeBaseForReindex(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]uuid.UUID, error) {
	return r.kbDocIDs, nil
}

// newReindexRouter 组一个只挂了重新索引那两条路由的最小环境。
func newReindexRouter(kbRepo knowledge.KBRepo, docRepo knowledge.DocRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Knowledge: knowledge.NewUsecase(
			kbRepo, docRepo, noopFileStore{}, noopEnqueuer{}, noopChunkIndexer{},
			noopFileCleaner{}, noopScheduler{}, passthroughTxManager{}, nil,
		),
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

func postJSON(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 202 + 响应体里的状态是回读出来的真实值（queued），不是拼出来的。
func TestReindexDocument_AcceptedWithQueuedStatus(t *testing.T) {
	docID := uuid.New()
	docRepo := &recordingDocRepo{doc: &knowledge.Document{
		ID: docID, Filename: "a.md", Status: knowledge.StatusQueued, ByteSize: 10,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	r := newReindexRouter(&fakeKBRepo{}, docRepo)

	w := postJSON(r, "/api/v1/documents/"+docID.String()+"/reindex")

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var got Document
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, string(knowledge.StatusQueued), string(got.Status))
}

// 已经在排队里再点一次 → 409 conflict。静默成功会让用户以为排了新的。
func TestReindexDocument_ConflictBecomes409(t *testing.T) {
	docID := uuid.New()
	docRepo := &recordingDocRepo{
		markErr: platform.ErrConflict,
	}
	r := newReindexRouter(&fakeKBRepo{}, docRepo)

	w := postJSON(r, "/api/v1/documents/"+docID.String()+"/reindex")

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "conflict")
	assert.Contains(t, w.Header().Get("Content-Type"), "application/problem+json")
}

// 响应体必须是 {enqueued: N}，不能是 null——和 toAPIKBList 那条
// 「空切片不能是 null」是同一个约定。
func TestReindexKnowledgeBase_ReturnsEnqueuedCount(t *testing.T) {
	kbRepo := &fakeKBRepo{}
	kb := &knowledge.KB{ID: uuid.New(), Name: "库"}
	require.NoError(t, kbRepo.Insert(context.Background(), nil, kb))

	docRepo := &recordingDocRepo{kbDocIDs: []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}}
	r := newReindexRouter(kbRepo, docRepo)

	w := postJSON(r, "/api/v1/knowledge-bases/"+kb.ID.String()+"/reindex")

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var got ReindexAccepted
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 3, got.Enqueued)
}

// 零份文档时也必须是 {enqueued: 0} 而不是 null。
func TestReindexKnowledgeBase_ZeroIsNotJSONNull(t *testing.T) {
	kbRepo := &fakeKBRepo{}
	kb := &knowledge.KB{ID: uuid.New(), Name: "空库"}
	require.NoError(t, kbRepo.Insert(context.Background(), nil, kb))
	r := newReindexRouter(kbRepo, &recordingDocRepo{})

	w := postJSON(r, "/api/v1/knowledge-bases/"+kb.ID.String()+"/reindex")

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"enqueued":0`)
	assert.NotContains(t, w.Body.String(), "null")
}

// ── 契约与实现的锁步 ──────────────────────────────────────────────

// 两个新端点必须声明它们真的会返回的状态码。
//
// 【为什么必须有这条】契约漏一个状态码不会有任何东西报错，只会让按声明
// 状态集写代码的调用方少处理一个分支——和 TestContractDeclares404WhenParentRowIsMissing
// 拦的是同一类问题。
func TestContractDeclaresReindexResponses(t *testing.T) {
	for _, tt := range []struct {
		operationID string
		mustHave    []string
	}{
		{"reindexDocument", []string{"202", "404", "409", "500"}},
		{"reindexKnowledgeBase", []string{"202", "404", "500"}},
	} {
		t.Run(tt.operationID, func(t *testing.T) {
			codes := contractOperationResponses(t, tt.operationID)
			require.NotEmpty(t, codes, "契约里找不到 operationId %s 的 responses", tt.operationID)
			for _, code := range tt.mustHave {
				assert.True(t, codes[code], "%s 必须声明 %s", tt.operationID, code)
			}
		})
	}
}

// createProvider 的 409 必须指向那条专门的响应，而不是笼统的 Conflict——
// 前端要按 type 弹确认框。
func TestContractCreateProvider409PointsAtReindexResponse(t *testing.T) {
	raw, err := os.ReadFile(contractPath(t))
	require.NoError(t, err)

	// 缩进解析器读不到 $ref 指向哪个响应，这里直接对原文做一次定位：
	// createProvider 那段里 409 下面紧跟的必须是对 EmbeddingChangeRequiresReindex
	// 的引用。
	idx := strings.Index(string(raw), "operationId: createProvider")
	require.Positive(t, idx, "契约里找不到 createProvider")

	section := string(raw)[idx:]
	// 到下一个 operationId 为止（createProvider 之后的下一个操作）。
	if next := strings.Index(section[1:], "operationId:"); next > 0 {
		section = section[:next+1]
	}

	got409 := strings.Index(section, "'409':")
	require.Positive(t, got409, "createProvider 没有声明 409")
	after409 := section[got409:]

	assert.Contains(t, after409, "EmbeddingChangeRequiresReindex",
		"换 embedding 模型的 409 必须指向那条专门的响应，前端靠 type 弹确认框")
}
