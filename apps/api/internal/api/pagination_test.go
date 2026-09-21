package api

import (
	"context"
	"encoding/json"
	"net/http"
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
// 分页端点的 HTTP 边界（issue #45）
// ════════════════════════════════════════════════════════════════

// pagingDocRepo 只服务分页那几个用例：记录收到的游标与 limit，返回预置的一页。
type pagingDocRepo struct {
	noopDocRepo

	page      []*knowledge.Document
	hasMore   bool
	gotCursor *platform.ListCursor
	gotLimit  int
}

func (r *pagingDocRepo) ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID, cur *platform.ListCursor, limit int) ([]*knowledge.Document, bool, error) {
	r.gotCursor = cur
	r.gotLimit = limit
	return r.page, r.hasMore, nil
}

func newPagingRouter(repo *pagingDocRepo) *gin.Engine {
	return newReindexRouter(&fakeKBRepo{}, repo)
}

func seedPage(n int) []*knowledge.Document {
	out := make([]*knowledge.Document, 0, n)
	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out = append(out, &knowledge.Document{
			ID: uuid.New(), Filename: "a.md", Status: knowledge.StatusReady,
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

// 成功时响应体是信封而不是裸数组——裸数组客户端拿不到游标。
func TestListDocuments_ReturnsEnvelopeNotBareArray(t *testing.T) {
	repo := &pagingDocRepo{page: seedPage(2), hasMore: true}
	r := newPagingRouter(repo)

	w := getPath(r, "/api/v1/knowledge-bases/"+uuid.NewString()+"/documents")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got DocumentPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), "响应必须能解成 {items, nextCursor}")
	assert.Len(t, got.Items, 2)
	require.NotNil(t, got.NextCursor, "还有下一页时必须给游标")
	assert.NotEmpty(t, *got.NextCursor)
	assert.Equal(t, platform.DefaultListLimit, repo.gotLimit, "没传 limit 时用默认值")
}

// 没有下一页时 nextCursor 缺席（等于 null），前端据此收起「加载更多」。
func TestListDocuments_LastPageOmitsCursor(t *testing.T) {
	repo := &pagingDocRepo{page: seedPage(1), hasMore: false}
	r := newPagingRouter(repo)

	w := getPath(r, "/api/v1/knowledge-bases/"+uuid.NewString()+"/documents")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	_, present := raw["nextCursor"]
	assert.False(t, present, "没有更多时不该给游标")
}

// 越界/非法参数一律 400 invalid_argument，且**不进 usecase**。
func TestListDocuments_InvalidParamsBecome400(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"limit 超过上限", "?limit=100000"},
		{"limit 是负数", "?limit=-1"},
		{"limit 不是数字", "?limit=abc"},
		{"游标是垃圾", "?cursor=!!!not-a-cursor!!!"},
		{"游标版本不认识", "?cursor=v9:AAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pagingDocRepo{page: seedPage(1)}
			r := newPagingRouter(repo)

			w := getPath(r, "/api/v1/knowledge-bases/"+uuid.NewString()+"/documents"+tc.query)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "invalid_argument")

			// 【这一条比状态码更重要】参数不合法时不能触达 repo——否则
			// 一次坏请求会带着无意义的查询打到数据库上。
			assert.Zero(t, repo.gotLimit, "参数校验失败时不该调用 repo")
		})
	}
}

// 合法游标要被解出来并原样传进 repo。
func TestListDocuments_ValidCursorReachesTheRepo(t *testing.T) {
	repo := &pagingDocRepo{page: seedPage(1)}
	r := newPagingRouter(repo)
	cursor := platform.EncodeCursor("2026-09-21T10:00:00Z", uuid.NewString())

	w := getPath(r, "/api/v1/knowledge-bases/"+uuid.NewString()+"/documents?limit=7&cursor="+cursor)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 7, repo.gotLimit)
	require.NotNil(t, repo.gotCursor)
	assert.Equal(t, "2026-09-21T10:00:00Z", repo.gotCursor.SortKey)
}

// 契约里三个分页端点都必须声明 400——漏了调用方就不知道要处理它。
func TestContractDeclaresPaginationResponses(t *testing.T) {
	for _, op := range []string{"listDocuments", "listConversationMessages", "listAgentRuns"} {
		t.Run(op, func(t *testing.T) {
			codes := contractOperationResponses(t, op)
			require.NotEmpty(t, codes, "契约里找不到 operationId %s", op)
			assert.True(t, codes["400"], "%s 会返回 400（参数越界/坏游标），契约必须声明", op)
		})
	}
}
