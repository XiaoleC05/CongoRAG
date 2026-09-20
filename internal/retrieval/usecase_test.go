package retrieval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ────────────────────────────────────────────────────────────────
// 假实现
// ────────────────────────────────────────────────────────────────

var _ Repo = (*fakeRepo)(nil)

type fakeRepo struct {
	// chunks 按 docID 分组，模拟真实表里"一个文档对应若干行"的结构。
	chunks map[uuid.UUID][]domain.Chunk
	vecs   map[uuid.UUID][][]float32
	models map[uuid.UUID]string

	failInsert bool
	failDelete bool

	// Search 相关：既能摆放返回值，也能记录 Usecase.Search 传了什么参数。
	searchResult    []domain.Chunk
	searchErr       error
	lastSearchKBID  uuid.UUID
	lastSearchModel string
	lastSearchTopK  int
	lastSearchVec   []float32
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		chunks: map[uuid.UUID][]domain.Chunk{},
		vecs:   map[uuid.UUID][][]float32{},
		models: map[uuid.UUID]string{},
	}
}

func (f *fakeRepo) InsertChunks(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk, vecs [][]float32, model string) error {
	if f.failInsert {
		return errors.New("fake insert failure")
	}
	f.chunks[docID] = chunks
	f.vecs[docID] = vecs
	f.models[docID] = model
	return nil
}

func (f *fakeRepo) DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error {
	if f.failDelete {
		return errors.New("fake delete failure")
	}
	delete(f.chunks, docID)
	delete(f.vecs, docID)
	delete(f.models, docID)
	return nil
}

// searchResult 让测试直接摆放"这次 Search 该返回什么"，不需要真的模拟
// pgvector 的余弦距离计算——那部分只有连真实 Postgres 才测得出意义
// （见 llm 包 postgres_integration_test.go 同样的取舍）。
func (f *fakeRepo) Search(ctx context.Context, q platform.Querier, kbID uuid.UUID, vec []float32, model string, topK int) ([]domain.Chunk, error) {
	f.lastSearchKBID = kbID
	f.lastSearchModel = model
	f.lastSearchTopK = topK
	f.lastSearchVec = vec
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.searchResult, nil
}

// fakeRegistry 只实现 IndexDocument 真正用到的 Embedder；其余方法
// 用不到，返回错误而不是零值——用到了就会在测试里立刻炸出来。
var _ llm.Registry = (*fakeRegistry)(nil)

type fakeRegistry struct {
	embedder llm.Embedder
	// lastModelID 记录最近一次 Embedder() 调用传的 modelID，
	// 用来断言"确实用了 activeEmbeddingModel 解析出的那个 ID"。
	lastModelID string
	embedderErr error

	embedderCalls int
}

func (f *fakeRegistry) embedderCalled() bool { return f.embedderCalls > 0 }

func (f *fakeRegistry) Chat(ctx context.Context, modelID string) (llm.ChatModel, error) {
	return nil, errors.New("fakeRegistry.Chat: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Embedder(ctx context.Context, modelID string) (llm.Embedder, error) {
	f.embedderCalls++
	f.lastModelID = modelID
	if f.embedderErr != nil {
		return nil, f.embedderErr
	}
	return f.embedder, nil
}

func (f *fakeRegistry) Tokenizer(ctx context.Context, modelID string) (llm.Tokenizer, error) {
	return nil, errors.New("fakeRegistry.Tokenizer: not implemented, this test should not reach here")
}

func (f *fakeRegistry) ActiveModelID(ctx context.Context, kind llm.Kind) (string, error) {
	return "", errors.New("fakeRegistry.ActiveModelID: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Capabilities(ctx context.Context, modelID string) (llm.Capabilities, error) {
	return llm.Capabilities{}, errors.New("fakeRegistry.Capabilities: not implemented, this test should not reach here")
}

func (f *fakeRegistry) ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error) {
	return 0, errors.New("fakeRegistry.ProbeEmbeddingDimension: not implemented, this test should not reach here")
}

func (f *fakeRegistry) ResolveChatEndpoint(ctx context.Context, modelID string) (string, string, string, error) {
	return "", "", "", errors.New("fakeRegistry.ResolveChatEndpoint: not implemented, this test should not reach here")
}

// fakeEmbedder 返回确定性的向量（每个文本一个长度为 1 的向量，值是文本长度），
// 不需要真的算语义——这一层测的是"embed 完之后有没有正确落库"，
// 不是"embedding 算得准不准"。
type fakeEmbedder struct {
	dim       int
	failEmbed bool
	mismatch  bool // 故意返回和输入数量不一致的向量，测防御性检查
}

func (e *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.failEmbed {
		return nil, errors.New("fake embed failure")
	}
	n := len(texts)
	if e.mismatch {
		n++
	}
	out := make([][]float32, n)
	for i := range out {
		out[i] = []float32{float32(i)}
	}
	return out, nil
}

func (e *fakeEmbedder) Dim() int { return e.dim }

var _ llm.ConfigRepo = (*fakeConfigRepo)(nil)

// fakeConfigRepo 只需要 ListModels——activeEmbeddingModel 只调这一个方法。
type fakeConfigRepo struct {
	models  []*llm.Model
	failErr error
}

func (f *fakeConfigRepo) UpsertProvider(ctx context.Context, q platform.Querier, p *llm.Provider, keyCiphertext []byte) error {
	return errors.New("not implemented")
}
func (f *fakeConfigRepo) ListProviders(ctx context.Context, q platform.Querier) ([]*llm.Provider, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeConfigRepo) GetProvider(ctx context.Context, q platform.Querier, id uuid.UUID) (*llm.Provider, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeConfigRepo) GetProviderKey(ctx context.Context, q platform.Querier, id uuid.UUID) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeConfigRepo) UpsertModel(ctx context.Context, q platform.Querier, m *llm.Model) error {
	return errors.New("not implemented")
}
func (f *fakeConfigRepo) ListModels(ctx context.Context, q platform.Querier) ([]*llm.Model, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	return f.models, nil
}
func (f *fakeConfigRepo) GetModel(ctx context.Context, q platform.Querier, id uuid.UUID) (*llm.Model, error) {
	for _, m := range f.models {
		if m.ID == id {
			return m, nil
		}
	}
	return nil, platform.ErrNotFound
}

func embeddingModel(id uuid.UUID, modelID string, createdAt time.Time) *llm.Model {
	return &llm.Model{ID: id, ModelID: modelID, Kind: llm.KindEmbedding, CreatedAt: createdAt}
}

func chatModel(id uuid.UUID, createdAt time.Time) *llm.Model {
	return &llm.Model{ID: id, ModelID: "gpt-x", Kind: llm.KindChat, CreatedAt: createdAt}
}

// ════════════════════════════════════════════════════════════════
// activeEmbeddingModel（通过 IndexDocument 间接测，它是私有方法）
// ════════════════════════════════════════════════════════════════

func TestIndexDocument_UsesLatestEmbeddingModel(t *testing.T) {
	older := embeddingModel(uuid.New(), "old-embed", time.Now().Add(-time.Hour))
	newer := embeddingModel(uuid.New(), "new-embed", time.Now())
	ignoredChat := chatModel(uuid.New(), time.Now().Add(time.Hour)) // 时间更新但 kind 不对，必须被忽略

	repo := newFakeRepo()
	embedder := &fakeEmbedder{}
	registry := &fakeRegistry{embedder: embedder}
	configRepo := &fakeConfigRepo{models: []*llm.Model{older, newer, ignoredChat}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	chunks := []domain.Chunk{{Content: "a"}, {Content: "b"}}
	err := uc.IndexDocument(context.Background(), nil, uuid.New(), chunks)

	require.NoError(t, err)
	assert.Equal(t, newer.ID.String(), registry.lastModelID, "必须选最近创建的那个 embedding 模型，且不能被更晚创建的 chat 模型抢走")
}

func TestIndexDocument_NoEmbeddingModelConfigured(t *testing.T) {
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{chatModel(uuid.New(), time.Now())}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	err := uc.IndexDocument(context.Background(), nil, uuid.New(), []domain.Chunk{{Content: "a"}})

	assert.ErrorIs(t, err, platform.ErrNotFound)
}

// ════════════════════════════════════════════════════════════════
// IndexDocument —— 先删后插的幂等性 + 落库内容
// ════════════════════════════════════════════════════════════════

func TestIndexDocument_Success(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	docID := uuid.New()
	chunks := []domain.Chunk{{Content: "第一段"}, {Content: "第二段"}}

	err := uc.IndexDocument(context.Background(), nil, docID, chunks)

	require.NoError(t, err)
	assert.Equal(t, chunks, repo.chunks[docID])
	assert.Equal(t, model.ModelID, repo.models[docID], "落库的应该是人类可读的 ModelID，不是内部的 uuid")
	require.Len(t, repo.vecs[docID], 2)
}

// 【先删后插是重试安全性的核心】不先清空的话，同一个文档被 worker
// 重新处理一次（River 重试），表里会堆积两份重复的分块。
func TestIndexDocument_ClearsExistingChunksFirst(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	docID := uuid.New()
	// 模拟"上一次已经写过一份旧数据"。
	repo.chunks[docID] = []domain.Chunk{{Content: "旧的、应该被清掉的分块"}}

	err := uc.IndexDocument(context.Background(), nil, docID, []domain.Chunk{{Content: "新分块"}})

	require.NoError(t, err)
	require.Len(t, repo.chunks[docID], 1, "重新索引之后应该只有新的那一份，不是新旧累加")
	assert.Equal(t, "新分块", repo.chunks[docID][0].Content)
}

func TestIndexDocument_EmptyChunks_StillClearsOldOnes(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	docID := uuid.New()
	repo.chunks[docID] = []domain.Chunk{{Content: "旧的"}}

	err := uc.IndexDocument(context.Background(), nil, docID, nil)

	require.NoError(t, err)
	assert.NotContains(t, repo.chunks, docID, "空文档（比如一个空文件）应该清空旧分块，不该保留")
	assert.False(t, registry.embedderCalled(), "零个分块不该发一次空的 embedding 请求")
}

func TestIndexDocument_EmbedFails(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{failEmbed: true}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	err := uc.IndexDocument(context.Background(), nil, uuid.New(), []domain.Chunk{{Content: "a"}})

	require.Error(t, err)
}

// embedder 返回的向量数量和送进去的文本数量不一致——必须显式报错，
// 不能假装对齐、把某一段的向量错配给另一段。
func TestIndexDocument_EmbedderReturnsMismatchedCount(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{mismatch: true}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	err := uc.IndexDocument(context.Background(), nil, uuid.New(), []domain.Chunk{{Content: "a"}, {Content: "b"}})

	assert.ErrorIs(t, err, platform.ErrUpstream)
}

func TestIndexDocument_InsertFails(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	repo.failInsert = true
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	err := uc.IndexDocument(context.Background(), nil, uuid.New(), []domain.Chunk{{Content: "a"}})

	require.Error(t, err)
}

// ════════════════════════════════════════════════════════════════
// DeleteByDocument
// ════════════════════════════════════════════════════════════════

func TestDeleteByDocument_Success(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, &fakeRegistry{}, &fakeConfigRepo{}, nil)

	docID := uuid.New()
	repo.chunks[docID] = []domain.Chunk{{Content: "a"}}

	err := uc.DeleteByDocument(context.Background(), nil, docID)

	require.NoError(t, err)
	assert.NotContains(t, repo.chunks, docID)
}

func TestDeleteByDocument_PropagatesError(t *testing.T) {
	repo := newFakeRepo()
	repo.failDelete = true
	uc := NewUsecase(repo, &fakeRegistry{}, &fakeConfigRepo{}, nil)

	err := uc.DeleteByDocument(context.Background(), nil, uuid.New())

	require.Error(t, err)
}

// ════════════════════════════════════════════════════════════════
// Search —— conversation.ChunkSearcher 的实现
// ════════════════════════════════════════════════════════════════

func TestSearch_Success(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	want := []domain.Chunk{{Content: "命中的分块", Score: 0.87, Filename: "a.md"}}
	repo.searchResult = want
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	kbID := uuid.New()
	got, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: kbID, Text: "查询文本", TopK: 3,
	})

	require.NoError(t, err)
	assert.Equal(t, want, got)

	// Repo.Search 收到的参数必须是"解析出的 embedding 模型" + "调用方传的 TopK"，
	// 不是别的来源。
	assert.Equal(t, kbID, repo.lastSearchKBID)
	assert.Equal(t, model.ModelID, repo.lastSearchModel)
	assert.Equal(t, 3, repo.lastSearchTopK)
	assert.NotEmpty(t, repo.lastSearchVec, "应该真的调用了 embedder 把查询文本变成向量")
}

// TopK <= 0（调用方没指定）时应该退回 Usecase 的默认值，
// 不能把 0 原样传给 Repo.Search——LIMIT 0 会让查询直接返回空结果。
func TestSearch_DefaultsTopKWhenNotSpecified(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	_, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: uuid.New(), Text: "查询文本", TopK: 0,
	})

	require.NoError(t, err)
	assert.Equal(t, defaultTopK, repo.lastSearchTopK)
}

// WithTopK 这个 Functional Option 设的默认值应该被用上,
// 而不是包级常量 defaultTopK。
func TestSearch_WithTopKOption_OverridesDefault(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil, WithTopK(20))

	_, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: uuid.New(), Text: "查询文本",
	})

	require.NoError(t, err)
	assert.Equal(t, 20, repo.lastSearchTopK)
}

func TestSearch_NoEmbeddingModelConfigured(t *testing.T) {
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{} // 空的,没有任何模型
	uc := NewUsecase(repo, registry, configRepo, nil)

	_, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: uuid.New(), Text: "查询文本",
	})

	assert.ErrorIs(t, err, platform.ErrNotFound)
}

func TestSearch_EmbedFails(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	registry := &fakeRegistry{embedder: &fakeEmbedder{failEmbed: true}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	_, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: uuid.New(), Text: "查询文本",
	})

	require.Error(t, err)
}

func TestSearch_RepoFails(t *testing.T) {
	model := embeddingModel(uuid.New(), "embed-1", time.Now())
	repo := newFakeRepo()
	repo.searchErr = errors.New("db boom")
	registry := &fakeRegistry{embedder: &fakeEmbedder{}}
	configRepo := &fakeConfigRepo{models: []*llm.Model{model}}
	uc := NewUsecase(repo, registry, configRepo, nil)

	_, err := uc.Search(context.Background(), domain.SearchRequest{
		KnowledgeBaseID: uuid.New(), Text: "查询文本",
	})

	require.Error(t, err)
}
