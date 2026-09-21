package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// Token 用量读端点（issue #47）
// ════════════════════════════════════════════════════════════════

type fakeUsageRepo struct {
	rows  []*llm.UsageByModel
	err   error
	since *time.Time
	until *time.Time
}

func (f *fakeUsageRepo) InsertUsage(context.Context, platform.Querier, *llm.Usage) error {
	return errors.New("fakeUsageRepo.InsertUsage: 读端点不该写入")
}

func (f *fakeUsageRepo) UsageSummary(_ context.Context, _ platform.Querier, since, until *time.Time) ([]*llm.UsageByModel, error) {
	f.since, f.until = since, until
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func newUsageRouter(repo llm.UsageRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := NewServer(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LLM:    llm.NewUsecase(nil, nil, nil, nil, nil, nil, repo),
	})
	RegisterHandlersWithOptions(r, srv, GinServerOptions{ErrorHandler: BindErrorHandler})
	return r
}

func usageRow(name string, kind llm.Kind, calls, prompt, completion int64) *llm.UsageByModel {
	return &llm.UsageByModel{
		ProviderID: uuid.New(), ModelID: uuid.New(), ModelName: name, Kind: kind,
		Calls: calls, PromptTokens: prompt, CompletionTokens: completion,
	}
}

func TestGetUsageSummary_AggregatesTotalsAndPerModel(t *testing.T) {
	repo := &fakeUsageRepo{rows: []*llm.UsageByModel{
		usageRow("gpt-4o-mini", llm.KindChat, 3, 100, 40),
		usageRow("bge-m3", llm.KindEmbedding, 5, 200, 0),
	}}
	w := getPath(newUsageRouter(repo), "/api/v1/usage")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got UsageSummary
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

	assert.EqualValues(t, 300, got.TotalPromptTokens, "合计是各模型之和")
	assert.EqualValues(t, 40, got.TotalCompletionTokens)
	require.Len(t, got.ByModel, 2)
	// 【给用户看的是模型名不是 uuid】这是这一条接口存在的意义。
	assert.Equal(t, "gpt-4o-mini", got.ByModel[0].ModelName)
	assert.Equal(t, UsageByModelKindEmbedding, got.ByModel[1].Kind)
}

// 没有任何用量时 byModel 必须是 []，不能是 null——前端拿到 null 做 .map() 会崩。
func TestGetUsageSummary_EmptyIsArrayNotNull(t *testing.T) {
	repo := &fakeUsageRepo{}
	w := getPath(newUsageRouter(repo), "/api/v1/usage")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"byModel":[]`)
	assert.NotContains(t, w.Body.String(), "null")
}

// since / until 要被原样透传给 repo（左闭右开的语义在 SQL 里）。
func TestGetUsageSummary_PassesTimeWindow(t *testing.T) {
	repo := &fakeUsageRepo{}
	w := getPath(newUsageRouter(repo), "/api/v1/usage?since=2026-09-01T00:00:00Z&until=2026-10-01T00:00:00Z")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, repo.since)
	require.NotNil(t, repo.until)
	assert.Equal(t, 2026, repo.since.Year())
	assert.Equal(t, time.September, repo.since.Month())
}

// 时间参数格式不对 → 400，且不进 repo。
func TestGetUsageSummary_BadTimeIs400(t *testing.T) {
	repo := &fakeUsageRepo{}
	w := getPath(newUsageRouter(repo), "/api/v1/usage?since=not-a-time")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid_argument")
	assert.Nil(t, repo.since, "参数不合法时不该触达 repo")
}

// repo 报错 → 500 且 detail 不泄漏 SQL。
func TestGetUsageSummary_RepoErrorIs500WithoutLeakingSQL(t *testing.T) {
	repo := &fakeUsageRepo{err: errors.New("ERROR: relation \"token_usage\" does not exist (SQLSTATE 42P01)")}
	w := getPath(newUsageRouter(repo), "/api/v1/usage")

	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "internal_error")
	assert.NotContains(t, w.Body.String(), "SQLSTATE", "5xx 的 detail 必须换成统一文案")
}
