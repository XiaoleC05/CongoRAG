package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// 设置页的模型管理（issue #83）：改与删。

func newAdminFixture() (*Usecase, *fakeConfigRepo) {
	repo := newFakeConfigRepo()
	return newTestUsecase(repo, &fakeSecretBox{}, &fakeRegistry{}, &fakeQuerier{}), repo
}

func seedModel(repo *fakeConfigRepo, kind Kind, name string, age time.Duration) *Model {
	m := &Model{
		ID: uuid.New(), ProviderID: uuid.New(), ModelID: name, Kind: kind,
		Capabilities: Capabilities{Chat: kind == KindChat},
		ContextWindow: 128000, MaxOutputTokens: 4096, TokenizerType: "o200k_base",
		CreatedAt: time.Now().Add(-age),
	}
	if kind == KindEmbedding {
		m.EmbeddingDim = 1024
		m.Capabilities = Capabilities{Embedding: true}
	}
	repo.models[m.ID] = m
	return m
}

// 改得动的那几项真的改了，而且落回了 repo。
func TestUpdateModel_UpdatesEditableFields(t *testing.T) {
	uc, repo := newAdminFixture()
	m := seedModel(repo, KindChat, "gpt-4o-mini", time.Hour)

	updated, err := uc.UpdateModel(context.Background(), m.ID, UpdateModelRequest{
		ModelID:         "gpt-4o",
		Capabilities:    Capabilities{Chat: true, ToolCalling: true, Reasoning: true},
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		TokenizerType:   "cl100k_base",
	})
	require.NoError(t, err)

	assert.Equal(t, "gpt-4o", updated.ModelID)
	assert.True(t, updated.Capabilities.Reasoning)
	assert.Equal(t, 200000, updated.ContextWindow)
	assert.Equal(t, "cl100k_base", updated.TokenizerType)
	// 不可改的那几项原样保留。
	assert.Equal(t, KindChat, updated.Kind)
	assert.Equal(t, m.ProviderID, updated.ProviderID)
	assert.Same(t, updated, repo.models[m.ID], "要真的写回存取层，不是只改了内存里那份副本")
}

// 【tokenizerType 必须在引导时那一批里被挡住】理由与 Bootstrap 那条相同：
// 这一个点不挡，非法值会先落库、之后每条消息才在聊天路径上失败，
// 而错误还会被归因到 provider 身上（见 tokenizer.go 的注释）。
func TestUpdateModel_RejectsUnknownTokenizer(t *testing.T) {
	uc, repo := newAdminFixture()
	m := seedModel(repo, KindChat, "gpt-4o-mini", time.Hour)

	_, err := uc.UpdateModel(context.Background(), m.ID, UpdateModelRequest{
		ModelID: "gpt-4o-mini", Capabilities: Capabilities{Chat: true},
		ContextWindow: 128000, MaxOutputTokens: 4096, TokenizerType: "o200k",
	})
	require.ErrorIs(t, err, platform.ErrInvalid)
	assert.Equal(t, "o200k_base", repo.models[m.ID].TokenizerType, "被拒之后不能留下半个改动")
}

func TestUpdateModel_RejectsNonPositiveNumbers(t *testing.T) {
	uc, repo := newAdminFixture()
	m := seedModel(repo, KindChat, "gpt-4o-mini", time.Hour)

	base := UpdateModelRequest{
		ModelID: "gpt-4o-mini", Capabilities: Capabilities{Chat: true},
		ContextWindow: 128000, MaxOutputTokens: 4096, TokenizerType: "o200k_base",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*UpdateModelRequest)
	}{
		{"contextWindow = 0", func(r *UpdateModelRequest) { r.ContextWindow = 0 }},
		{"maxOutputTokens = -1", func(r *UpdateModelRequest) { r.MaxOutputTokens = -1 }},
		{"modelId 空", func(r *UpdateModelRequest) { r.ModelID = "   " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			_, err := uc.UpdateModel(context.Background(), m.ID, req)
			require.ErrorIs(t, err, platform.ErrInvalid)
		})
	}
}

// 【embedding 模型不能改名】向量列的维度是引导时按那个模型探测出来并
// ALTER 到表上的，改名字会让列里那些向量和配置对不上——那是 409 而不是
// 400：请求本身合法，只是和已有状态冲突。
func TestUpdateModel_EmbeddingRenameIsConflict(t *testing.T) {
	uc, repo := newAdminFixture()
	m := seedModel(repo, KindEmbedding, "bge-m3", time.Hour)

	_, err := uc.UpdateModel(context.Background(), m.ID, UpdateModelRequest{
		ModelID: "bge-large", Capabilities: Capabilities{Embedding: true},
		ContextWindow: 8192, MaxOutputTokens: 8192, TokenizerType: "cl100k_base",
	})

	require.ErrorIs(t, err, platform.ErrConflict)
	assert.Contains(t, err.Error(), "bge-m3", "文案要指出是哪个模型不能改名")
	assert.Equal(t, "bge-m3", repo.models[m.ID].ModelID)
}

// 同名（没真的改）时不该被挡——否则设置页改个 capabilities 都会被拒。
func TestUpdateModel_EmbeddingWithSameNameIsAllowed(t *testing.T) {
	uc, repo := newAdminFixture()
	m := seedModel(repo, KindEmbedding, "bge-m3", time.Hour)

	updated, err := uc.UpdateModel(context.Background(), m.ID, UpdateModelRequest{
		ModelID: "bge-m3", Capabilities: Capabilities{Embedding: true},
		ContextWindow: 8192, MaxOutputTokens: 8192, TokenizerType: "cl100k_base",
	})
	require.NoError(t, err)
	assert.Equal(t, 8192, updated.ContextWindow)
}

func TestUpdateModel_NotFound(t *testing.T) {
	uc, _ := newAdminFixture()
	_, err := uc.UpdateModel(context.Background(), uuid.New(), UpdateModelRequest{
		ModelID: "x", ContextWindow: 1, MaxOutputTokens: 1, TokenizerType: "o200k_base",
	})
	require.ErrorIs(t, err, platform.ErrNotFound)
}

// ── 删除 ────────────────────────────────────────────────────────

// 【当前生效的那个删不掉】判据用的是 LatestByKind——和"哪个模型在生效"
// 是同一套规则。删掉它之后失败点离用户的操作很远（发下一条消息才炸）。
func TestDeleteModel_ActiveModelIsConflict(t *testing.T) {
	uc, repo := newAdminFixture()
	seedModel(repo, KindChat, "旧的", 2*time.Hour)
	active := seedModel(repo, KindChat, "当前生效的", time.Hour)

	err := uc.DeleteModel(context.Background(), active.ID)

	require.ErrorIs(t, err, platform.ErrConflict)
	assert.Contains(t, err.Error(), string(KindChat))
	_, still := repo.models[active.ID]
	assert.True(t, still, "被拒之后那条模型必须还在")
}

// 不是当前生效的那一条可以删——这正是设置页最常见的动作
// （把用过的旧模型清掉）。
func TestDeleteModel_NonActiveModelIsDeleted(t *testing.T) {
	uc, repo := newAdminFixture()
	old := seedModel(repo, KindChat, "旧的", 2*time.Hour)
	seedModel(repo, KindChat, "当前生效的", time.Hour)

	require.NoError(t, uc.DeleteModel(context.Background(), old.ID))

	_, still := repo.models[old.ID]
	assert.False(t, still)
}

// 「当前生效」是**按 kind 各判各的**：删 embedding 时不能被"有一个 chat
// 模型存在"这件事满足掉，也不能拿 chat 那条来比。
//
// 建两条 embedding（新的是当前生效的）+ 一条 chat，然后：
// 删当前生效的那条 embedding → 必须被拒（理由里写的是 embedding）；
// 删更旧的那条 embedding → 放行。
func TestDeleteModel_ActiveIsPerKind(t *testing.T) {
	uc, repo := newAdminFixture()
	seedModel(repo, KindChat, "gpt-4o-mini", time.Hour)
	oldEmbedding := seedModel(repo, KindEmbedding, "bge-large", 3*time.Hour)
	activeEmbedding := seedModel(repo, KindEmbedding, "bge-m3", 2*time.Hour)

	err := uc.DeleteModel(context.Background(), activeEmbedding.ID)
	require.ErrorIs(t, err, platform.ErrConflict,
		"它是当前生效的 embedding 模型——有 chat 模型存在这件事跟它没关系")
	assert.Contains(t, err.Error(), string(KindEmbedding))

	require.NoError(t, uc.DeleteModel(context.Background(), oldEmbedding.ID),
		"不是当前生效的那条要放行")
}

func TestDeleteModel_NotFound(t *testing.T) {
	uc, _ := newAdminFixture()
	err := uc.DeleteModel(context.Background(), uuid.New())
	require.ErrorIs(t, err, platform.ErrNotFound)
	// 顺带钉住：错误链里带的是 not_found，不是那个"当前生效"的 409。
	assert.False(t, errors.Is(err, platform.ErrConflict))
}
