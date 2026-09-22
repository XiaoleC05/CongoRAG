package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// 这一组测试覆盖 HTTP 边界：路由 → 参数绑定 → handler → usecase → 响应。
// 全程不连数据库——knowledge.KBRepo 是导出的接口，在测试包里实现它就行。

// fakeKBRepo 是 KBRepo 的内存实现。
//
// 【"找不到"必须和 PgRepo 包一样的层数】
// 真实的 postgres.go 返回的是 fmt.Errorf("knowledge base %s: %w", id, ErrNotFound)，
// 不是裸的 sentinel。usecase 在外面再包一层，所以生产环境里到达 handler 的是【两层】。
// 这里要是返回裸 sentinel，链就只有一层——api 层所有关于 detail 的断言测的
// 就不是真实形状，problem.go 里取哪一层的逻辑在测试里和生产里表现会不一样。
type fakeKBRepo struct {
	kbs []*knowledge.KB

	// 让指定方法返回错误，用来测错误路径
	failOn string
	err    error
}

// notFound 复刻 postgres.go 里"查不到"时的包装形状。
func notFound(id uuid.UUID) error {
	return fmt.Errorf("knowledge base %s: %w", id, platform.ErrNotFound)
}

func (f *fakeKBRepo) Insert(ctx context.Context, q platform.Querier, kb *knowledge.KB) error {
	if f.failOn == "Insert" {
		return f.err
	}
	f.kbs = append(f.kbs, kb)
	return nil
}

func (f *fakeKBRepo) List(ctx context.Context, q platform.Querier) ([]*knowledge.KB, error) {
	if f.failOn == "List" {
		return nil, f.err
	}
	return f.kbs, nil
}

func (f *fakeKBRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*knowledge.KB, error) {
	if f.failOn == "ByID" {
		return nil, f.err
	}
	for _, kb := range f.kbs {
		if kb.ID == id {
			return kb, nil
		}
	}
	return nil, notFound(id)
}

func (f *fakeKBRepo) Rename(ctx context.Context, q platform.Querier, id uuid.UUID, newName string, updatedAt time.Time) error {
	if f.failOn == "Rename" {
		return f.err
	}
	for _, kb := range f.kbs {
		if kb.ID == id {
			kb.Name = newName
			kb.UpdatedAt = updatedAt
			return nil
		}
	}
	return notFound(id)
}

func (f *fakeKBRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	if f.failOn == "Delete" {
		return f.err
	}
	for i, kb := range f.kbs {
		if kb.ID == id {
			f.kbs = append(f.kbs[:i], f.kbs[i+1:]...)
			return nil
		}
	}
	return nil
}

// 下面这一组 noop 类型只是为了满足 knowledge.NewUsecase 现在的九个参数——
// 这个测试文件只覆盖知识库 CRUD 的 HTTP 行为，从不触发文档相关的方法。
// 真正测试文档上传/处理逻辑的假实现在 internal/knowledge/usecase_test.go，
// 那边的假实现是"活的"（记录调用、模拟失败），这里的都是纯粹的哑实现。

type noopDocRepo struct{}

func (noopDocRepo) Insert(ctx context.Context, q platform.Querier, d *knowledge.Document) error {
	return nil
}
func (noopDocRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*knowledge.Document, error) {
	return nil, platform.ErrNotFound
}
func (noopDocRepo) UpdateStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to knowledge.Status) error {
	return nil
}
func (noopDocRepo) ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID, cur *platform.ListCursor, limit int) ([]*knowledge.Document, bool, error) {
	return nil, false, nil
}
func (noopDocRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) (string, error) {
	return "", platform.ErrNotFound
}
func (noopDocRepo) DeleteByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]string, error) {
	return nil, nil
}
// 下面三个是重新索引（issue #39）要的方法。这个替身只服务"api 层能不能
// 装配起来"这件事，业务语义由 knowledge 包自己的测试覆盖。
func (noopDocRepo) MarkForReindex(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	return nil
}

func (noopDocRepo) MarkKnowledgeBaseForReindex(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (noopDocRepo) MarkAllForReindex(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	return nil, nil
}

func (noopDocRepo) ExistingStorageKeys(ctx context.Context, q platform.Querier, keys []string) (map[string]bool, error) {
	return nil, nil
}

type noopFileStore struct{}

func (noopFileStore) WriteTemp(ctx context.Context, r io.Reader) (string, int64, error) {
	return "", 0, nil
}
func (noopFileStore) Commit(ctx context.Context, tmpPath, storageKey string) error {
	return nil
}
func (noopFileStore) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	return nil, platform.ErrNotFound
}
func (noopFileStore) RemoveTemp(ctx context.Context, tmpPath string) error   { return nil }
func (noopFileStore) Remove(ctx context.Context, storageKey string) error    { return nil }
func (noopFileStore) List(ctx context.Context) ([]knowledge.FileInfo, error) { return nil, nil }

func (noopFileStore) SweepTemp(ctx context.Context, olderThan time.Time) error { return nil }

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueProcessing(ctx context.Context, q platform.Querier, documentID uuid.UUID) error {
	return nil
}

func (noopEnqueuer) EnqueueProcessingBatch(ctx context.Context, q platform.Querier, documentIDs []uuid.UUID) error {
	return nil
}

type noopChunkIndexer struct{}

func (noopChunkIndexer) IndexDocument(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) error {
	return nil
}
func (noopChunkIndexer) DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error {
	return nil
}

type noopFileCleaner struct{}

func (noopFileCleaner) Schedule(storageKeys []string) {}

type noopScheduler struct{}

func (noopScheduler) RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error) {
}

// passthroughTxManager 直接调用 fn，不模拟真正的事务——这个文件测的是
// HTTP 边界，不是事务边界，DeleteKnowledgeBase 那条测试只关心状态码对不对。
type passthroughTxManager struct{}

func (passthroughTxManager) InTx(ctx context.Context, fn func(q platform.Querier) error) error {
	return fn(nil)
}

// newTestRouter 挂上由契约生成的路由，但【不挂中间件】——
// 这里测的是 handler 行为，中间件由别的测试覆盖。
func newTestRouter(repo knowledge.KBRepo) *gin.Engine {
	return newTestRouterWithUploadLimit(repo, 0)
}

// newTestRouterWithUploadLimit 和 newTestRouter 是同一套接线，只是把
// 上传大小上限设成指定的值。上限本来来自 platform.Config（由装配根填进
// Deps），测试里给一个小值比真的构造 32MB 请求体快得多。
//
// 传 0 表示不限——和 Deps.MaxUploadBytes 的约定一致。
func newTestRouterWithUploadLimit(repo knowledge.KBRepo, maxUploadBytes int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Knowledge: knowledge.NewUsecase(
			repo, noopDocRepo{}, noopFileStore{}, noopEnqueuer{}, noopChunkIndexer{},
			noopFileCleaner{}, noopScheduler{}, passthroughTxManager{}, nil,
		),
		MaxUploadBytes: maxUploadBytes,
	})
	// 【和生产一样的 options】app.go 里传的是 RegisterHandlersWithOptions +
	// BindErrorHandler。测试里用 RegisterHandlers 的话，参数绑定失败会走
	// oapi-codegen 的默认分支（{"msg":...}），测的不是真实接线。
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

// multipartUpload 拼一个只含一个 file 字段的 multipart 请求体，
// 返回 body 和 Content-Type。
func multipartUpload(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf, w.FormDataContentType()
}

// uploadDocument 发一次上传请求，返回状态码和响应体。
func uploadDocument(r *gin.Engine, kbID string, body *bytes.Buffer, contentType string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-bases/"+kbID+"/documents", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// do 发一个请求，返回状态码和响应体。
func do(r *gin.Engine, method, path, body string) (int, string) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// ────────────────────────────────────────────────────────────────
// 正常路径
// ────────────────────────────────────────────────────────────────

func TestHealthz(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodGet, "/healthz", "")

	require.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"status":"ok"}`, body)
}

func TestList_EmptyReturnsArrayNotNull(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodGet, "/api/v1/knowledge-bases", "")

	require.Equal(t, http.StatusOK, code)
	// 必须是 []，不能是 null——前端拿到 null 去做 .map() 会崩
	assert.Equal(t, "[]", strings.TrimSpace(body))
}

func TestCreateAndGetAndRenameAndDelete(t *testing.T) {
	repo := &fakeKBRepo{}
	r := newTestRouter(repo)

	// 新建
	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases", `{"name":"我的笔记"}`)
	require.Equal(t, http.StatusCreated, code, body)

	var created KnowledgeBase
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	assert.Equal(t, "我的笔记", created.Name)
	assert.NotEqual(t, uuid.Nil, created.Id)

	// 取一个
	code, body = do(r, http.MethodGet, "/api/v1/knowledge-bases/"+created.Id.String(), "")
	require.Equal(t, http.StatusOK, code, body)

	// 改名
	code, body = do(r, http.MethodPatch, "/api/v1/knowledge-bases/"+created.Id.String(), `{"name":"学习笔记"}`)
	require.Equal(t, http.StatusNoContent, code, body)
	assert.Empty(t, body, "204 不能带响应体")

	code, body = do(r, http.MethodGet, "/api/v1/knowledge-bases/"+created.Id.String(), "")
	require.Equal(t, http.StatusOK, code)
	var renamed KnowledgeBase
	require.NoError(t, json.Unmarshal([]byte(body), &renamed))
	assert.Equal(t, "学习笔记", renamed.Name)

	// 删除
	code, _ = do(r, http.MethodDelete, "/api/v1/knowledge-bases/"+created.Id.String(), "")
	require.Equal(t, http.StatusNoContent, code)

	code, _ = do(r, http.MethodGet, "/api/v1/knowledge-bases/"+created.Id.String(), "")
	assert.Equal(t, http.StatusNotFound, code)
}

// ────────────────────────────────────────────────────────────────
// 参数与错误路径
// ────────────────────────────────────────────────────────────────

func TestCreate_InvalidJSONReturns400(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases", `{不是 JSON`)

	require.Equal(t, http.StatusBadRequest, code, body)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "invalid_argument", p.Type)
	assert.Equal(t, http.StatusBadRequest, p.Status)
}

func TestCreate_EmptyNameReturns400(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases", `{"name":"   "}`)

	require.Equal(t, http.StatusBadRequest, code, body)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "invalid_argument", p.Type)
}

func TestCreate_NameTooLongReturns400(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	// 契约里声明了 maxLength: 200。数据库列是 text（无限制），
	// 应用层不设上限的话客户端能存进几十万字符的名字，列表接口跟着膨胀。
	tooLong := strings.Repeat("a", 201)
	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases",
		`{"name":"`+tooLong+`"}`)

	require.Equal(t, http.StatusBadRequest, code)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "invalid_argument", p.Type)
}

func TestCreate_NameAtLimitIsAccepted(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	// 按字符数算，不是字节数——中文一个字三字节，按字节算限制会随语言变化
	atLimit := strings.Repeat("知", 200)
	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases",
		`{"name":"`+atLimit+`"}`)

	assert.Equal(t, http.StatusCreated, code, body)
}

func TestGet_NotFoundReturns404(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodGet, "/api/v1/knowledge-bases/"+uuid.NewString(), "")

	require.Equal(t, http.StatusNotFound, code, body)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "not_found", p.Type)
}

func TestGet_MalformedUUIDIsRejectedBeforeHandler(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodGet, "/api/v1/knowledge-bases/not-a-uuid", "")

	// 由生成器的包装层拦下，不是走到 handler 才报错
	require.Equal(t, http.StatusBadRequest, code, body)

	// 【必须看 body】只看状态码的话，包装层返回 {"msg":...} 也测不出来——
	// 那和契约声明的 Problem 是两种互不兼容的结构。
	// 回归测试，见 BindErrorHandler 的注释。
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	assert.Equal(t, "invalid_argument", p.Type)
	assert.Equal(t, http.StatusBadRequest, p.Status)
	assert.NotContains(t, body, `"msg"`, "不该是 oapi-codegen 的默认结构")
}

func TestBindingErrorUsesProblemContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(&fakeKBRepo{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/problem+json")
}

func TestRename_MissingBodyReturns400(t *testing.T) {
	repo := &fakeKBRepo{}
	r := newTestRouter(repo)

	code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases", `{"name":"x"}`)
	require.Equal(t, http.StatusCreated, code)
	var kb KnowledgeBase
	require.NoError(t, json.Unmarshal([]byte(body), &kb))

	code, _ = do(r, http.MethodPatch, "/api/v1/knowledge-bases/"+kb.Id.String(), "")
	assert.Equal(t, http.StatusBadRequest, code)
}

// ────────────────────────────────────────────────────────────────
// repo 出错时的映射
// ────────────────────────────────────────────────────────────────

func TestRepoErrorMapsToStatus(t *testing.T) {
	tests := []struct {
		name       string
		repoErr    error
		wantStatus int
		wantType   string
	}{
		{"参数不合法", platform.ErrInvalid, http.StatusBadRequest, "invalid_argument"},
		{"唯一约束冲突", platform.ErrDuplicateKey, http.StatusConflict, "conflict_duplicate_key"},
		{"状态冲突", platform.ErrConflict, http.StatusConflict, "conflict"},
		{"外键违反", platform.ErrForeignKey, http.StatusNotFound, "not_found"},
		{"上游出错", platform.ErrUpstream, http.StatusBadGateway, "upstream_llm_error"},
		{"未分类错误", errBoom{}, http.StatusInternalServerError, "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRouter(&fakeKBRepo{failOn: "Insert", err: tt.repoErr})

			code, body := do(r, http.MethodPost, "/api/v1/knowledge-bases", `{"name":"x"}`)

			require.Equal(t, tt.wantStatus, code, body)
			var p Problem
			require.NoError(t, json.Unmarshal([]byte(body), &p))
			assert.Equal(t, tt.wantType, p.Type)
		})
	}
}

func TestInternalErrorHidesDetail(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{failOn: "List", err: errBoom{}})

	code, body := do(r, http.MethodGet, "/api/v1/knowledge-bases", "")

	require.Equal(t, http.StatusInternalServerError, code)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	// 5xx 不能把内部错误原文漏给前端
	require.NotNil(t, p.Detail)
	assert.Equal(t, "服务内部错误", *p.Detail)
	assert.NotContains(t, body, "boom")
}

// 【防退化】platform 里每个 sentinel 都必须在 classify 里有明确映射。
//
// 漏掉一个不会编译失败、不会 panic——它会落到 default 分支变成 500，
// 而正确答案可能是 409。ErrConflict 就曾经漏在外面。
//
// 新增 sentinel 时：先在这张表里加一行，再去 classify 里加分支。
// 表加了、分支没加的话这条测试会红。
func TestEverySentinelHasAnExplicitMapping(t *testing.T) {
	// ErrIdempotentHit 故意不在这里：它不是错误路径，
	// usecase 捕获后改为"返回已创建的资源"，永远到不了 classify。见 sentinel.go。
	cases := []struct {
		err      error
		wantCode int
		wantType string
	}{
		{platform.ErrNotFound, http.StatusNotFound, "not_found"},
		{platform.ErrConflict, http.StatusConflict, "conflict"},
		{platform.ErrInvalid, http.StatusBadRequest, "invalid_argument"},
		{platform.ErrDuplicateKey, http.StatusConflict, "conflict_duplicate_key"},
		{platform.ErrForeignKey, http.StatusNotFound, "not_found"},
		{platform.ErrUpstream, http.StatusBadGateway, "upstream_llm_error"},
		{ctxmgr.ErrOverflow, http.StatusBadRequest, "context_overflow"},
		// 恢复被拒绝的两条（issue #65 / #63）。它们必须是独立 type：
		// 前端按它给出"重新发起一次"或"这一步不能自动重放"这两种不同的
		// 下一步动作，落到笼统的 conflict 里就分不出来了。
		{platform.ErrStateSchemaVersionMismatch, http.StatusConflict, "state_schema_version_mismatch"},
		{platform.ErrToolEffectApplied, http.StatusConflict, "tool_effect_already_applied"},
		{platform.ErrReplayUnsafe, http.StatusConflict, "replay_unsafe"},
		// 【包自己的 sentinel 也要进这张表】漏掉它不会编译失败、也不会红，
		// 只会让换 embedding 模型那条路径静默变成 500——而正确答案是 409，
		// 且前端要靠这个 type 弹确认框（issue #39）。
		{llm.ErrEmbeddingResetRequired, http.StatusConflict, "embedding_change_requires_reindex"},
	}

	for _, tt := range cases {
		t.Run(tt.err.Error(), func(t *testing.T) {
			code, typ, title := classify(tt.err)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantType, typ)
			assert.NotEmpty(t, title)
			assert.NotEqual(t, http.StatusInternalServerError, code,
				"落到 default 分支了——classify 里缺这个 sentinel 的分支")
		})
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom: 内部细节不该泄漏" }

// ────────────────────────────────────────────────────────────────
// 4xx 的 detail 里该有什么、不该有什么
//
// 这两条测的不是"能不能跑"，是【契约边界】：detail 是给调用方看的，
// 不该带服务端的内部调用路径，也不该带 Go 的类型名。
// 两处都是不报错的问题——漏了只是响应体变难看、多暴露一点实现。
// ────────────────────────────────────────────────────────────────

// usecase 用 %w 一层层往上包，err.Error() 会是整条链：
//
//	"rename knowledge base <id>: knowledge base <id>: not found"
//
// id 出现两次，前半段是服务端的内部调用路径。fail 只该取最内层那句。
func TestNotFoundDetailDoesNotRepeatID(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	id := uuid.NewString()
	code, body := do(r, http.MethodPatch, "/api/v1/knowledge-bases/"+id, `{"name":"新名字"}`)

	require.Equal(t, http.StatusNotFound, code, body)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	require.NotNil(t, p.Detail)

	assert.Equal(t, 1, strings.Count(*p.Detail, id), "id 只该出现一次：%q", *p.Detail)
	assert.NotContains(t, *p.Detail, "rename knowledge base", "不该暴露内部调用路径")
	assert.Contains(t, *p.Detail, "not found")
}

// 生成器包装层的原始报错带 Go 的内部类型名和实现细节：
//
//	"Invalid format for parameter id: parsing value: invalid UUID length: 10"
//
// 参数名（id）是客户端该知道的，其余不是。
func TestBindErrorDetailHidesGoInternals(t *testing.T) {
	r := newTestRouter(&fakeKBRepo{})

	code, body := do(r, http.MethodGet, "/api/v1/knowledge-bases/not-a-uuid", "")

	require.Equal(t, http.StatusBadRequest, code, body)
	var p Problem
	require.NoError(t, json.Unmarshal([]byte(body), &p))
	require.NotNil(t, p.Detail)

	assert.Contains(t, *p.Detail, "id", "要告诉调用方是哪个参数错了")
	assert.NotContains(t, *p.Detail, "uuid.UUID", "不该出现 Go 的类型名")
	assert.NotContains(t, *p.Detail, "invalid UUID length", "不该出现库的内部报错")
	assert.NotContains(t, *p.Detail, "Invalid format for parameter", "不该是生成器的原文")
}

// bindErrorDetail 的单元测试。
//
// 最后一条是【防退化】的：oapi-codegen 换版本后前缀文案可能变，
// 那时 CutPrefix 匹配不上，会走兜底分支——这条测试保证兜底不会退回
// err.Error()（退回去就等于这个函数白写）。
func TestBindErrorDetail(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want string
	}{
		{
			"标准格式",
			errors.New("Invalid format for parameter id: parsing value: invalid UUID length: 10"),
			"参数 id 的格式不正确",
		},
		{
			"带 Go 类型名",
			errors.New("Invalid format for parameter id: unmarshaling into *uuid.UUID: bad"),
			"参数 id 的格式不正确",
		},
		{"nil", nil, "参数不合法"},
		{"认不出的格式（生成器换了文案）", errors.New("something else entirely"), "参数不合法"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bindErrorDetail(tt.in))
		})
	}
}

// innermostMessage 的单元测试。
func TestInnermostMessage(t *testing.T) {
	base := platform.ErrNotFound

	tests := []struct {
		name string
		in   error
		want string
	}{
		{"没有包装", base, "not found"},
		{
			"包一层",
			fmt.Errorf("knowledge base abc: %w", base),
			"knowledge base abc: not found",
		},
		{
			"包两层：只要最内层那句",
			fmt.Errorf("rename knowledge base abc: %w", fmt.Errorf("knowledge base abc: %w", base)),
			"knowledge base abc: not found",
		},
		{
			"包三层",
			fmt.Errorf("handler: %w",
				fmt.Errorf("rename knowledge base abc: %w",
					fmt.Errorf("knowledge base abc: %w", base))),
			"knowledge base abc: not found",
		},
		{"nil", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, innermostMessage(tt.in))
		})
	}
}

// ────────────────────────────────────────────────────────────────
// 上传大小上限
// ────────────────────────────────────────────────────────────────

// 【这条是#31的回归测试】上限必须在 FormFile 之前生效：FormFile 内部走
// ParseMultipartForm(32<<20)，超出的部分会被完整读进来、溢写到 os.TempDir，
// 之后才轮得到业务代码拒绝。没有 MaxBytesReader 时这条请求会一路走到
// 业务层（返回 202），几 GB 的请求体在任何拒绝点存在之前就已经被吃完了。
func TestUploadDocument_OversizeRejected(t *testing.T) {
	const limit = 1024
	r := newTestRouterWithUploadLimit(&fakeKBRepo{}, limit)
	body, contentType := multipartUpload(t, "big.txt", bytes.Repeat([]byte("x"), limit*4))

	code, respBody := uploadDocument(r, uuid.NewString(), body, contentType)

	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, respBody, "invalid_argument")

	var p Problem
	require.NoError(t, json.Unmarshal([]byte(respBody), &p))
	assert.Equal(t, "invalid_argument", p.Type)
	require.NotNil(t, p.Detail)
	assert.Contains(t, *p.Detail, "1024", "报错要带上真实的上限值，方便用户知道该切多小")
}

// 上限之内的上传照常受理——上面那条测试的 400 必须是"太大"造成的，
// 不是这个路由本来就坏。
func TestUploadDocument_WithinLimitAccepted(t *testing.T) {
	r := newTestRouterWithUploadLimit(&fakeKBRepo{}, 4096)
	body, contentType := multipartUpload(t, "notes.txt", []byte("一小段内容"))

	code, respBody := uploadDocument(r, uuid.NewString(), body, contentType)

	require.Equal(t, http.StatusAccepted, code, respBody)
	assert.Contains(t, respBody, "notes.txt")
}

// 上限为 0 表示不限（测试里的默认接线就是这样），此时超大请求体不会被
// 这个中间层拦——生产装配填的一定是正的配置值，见 platform.Config。
func TestUploadDocument_ZeroLimitMeansUnlimited(t *testing.T) {
	r := newTestRouterWithUploadLimit(&fakeKBRepo{}, 0)
	body, contentType := multipartUpload(t, "big.txt", bytes.Repeat([]byte("x"), 8192))

	code, _ := uploadDocument(r, uuid.NewString(), body, contentType)

	assert.Equal(t, http.StatusAccepted, code)
}
