package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ────────────────────────────────────────────────────────────────
// 假实现。整套测试不连数据库、不打真实网络请求，跑完是毫秒级的。
// ────────────────────────────────────────────────────────────────

var _ ConfigRepo = (*fakeConfigRepo)(nil)

type fakeConfigRepo struct {
	providers map[uuid.UUID]*Provider
	keys      map[uuid.UUID][]byte
	models    map[uuid.UUID]*Model

	failOn string
	err    error
}

func newFakeConfigRepo() *fakeConfigRepo {
	return &fakeConfigRepo{
		providers: map[uuid.UUID]*Provider{},
		keys:      map[uuid.UUID][]byte{},
		models:    map[uuid.UUID]*Model{},
	}
}

func (f *fakeConfigRepo) UpsertProvider(ctx context.Context, q platform.Querier, p *Provider, keyCiphertext []byte) error {
	if f.failOn == "UpsertProvider" {
		return f.err
	}
	f.providers[p.ID] = p
	f.keys[p.ID] = keyCiphertext
	return nil
}

func (f *fakeConfigRepo) ListProviders(ctx context.Context, q platform.Querier) ([]*Provider, error) {
	if f.failOn == "ListProviders" {
		return nil, f.err
	}
	out := make([]*Provider, 0, len(f.providers))
	for _, p := range f.providers {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeConfigRepo) GetProvider(ctx context.Context, q platform.Querier, id uuid.UUID) (*Provider, error) {
	p, ok := f.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %s: %w", id, platform.ErrNotFound)
	}
	return p, nil
}

func (f *fakeConfigRepo) GetProviderKey(ctx context.Context, q platform.Querier, id uuid.UUID) ([]byte, error) {
	k, ok := f.keys[id]
	if !ok {
		return nil, fmt.Errorf("provider %s: %w", id, platform.ErrNotFound)
	}
	return k, nil
}

func (f *fakeConfigRepo) UpsertModel(ctx context.Context, q platform.Querier, m *Model) error {
	if f.failOn == "UpsertModel" {
		return f.err
	}
	f.models[m.ID] = m
	return nil
}

func (f *fakeConfigRepo) ListModels(ctx context.Context, q platform.Querier) ([]*Model, error) {
	if f.failOn == "ListModels" {
		return nil, f.err
	}
	out := make([]*Model, 0, len(f.models))
	for _, m := range f.models {
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeConfigRepo) GetModel(ctx context.Context, q platform.Querier, id uuid.UUID) (*Model, error) {
	m, ok := f.models[id]
	if !ok {
		return nil, fmt.Errorf("model %s: %w", id, platform.ErrNotFound)
	}
	return m, nil
}

// fakeSecretBox 不做真的加密——业务层只依赖"Seal 之后能 Open 回原样，
// 篡改过或者根本没 Seal 过的东西 Open 不了"这两条性质，真的密码学强度
// 由 secretbox_test.go 单独验收（见该文件顶部注释）。
type fakeSecretBox struct {
	failSeal bool
}

const fakeSealPrefix = "SEALED:"

func (f *fakeSecretBox) Seal(plaintext []byte) ([]byte, error) {
	if f.failSeal {
		return nil, errors.New("fake seal failure")
	}
	return append([]byte(fakeSealPrefix), plaintext...), nil
}

func (f *fakeSecretBox) Open(ciphertext []byte) ([]byte, error) {
	prefix := []byte(fakeSealPrefix)
	if len(ciphertext) < len(prefix) || string(ciphertext[:len(prefix)]) != fakeSealPrefix {
		return nil, platform.ErrInvalid // 复用现成的 sentinel,不新造一个只在测试里用的错误
	}
	return ciphertext[len(prefix):], nil
}

var _ Registry = (*fakeRegistry)(nil)

// fakeRegistry 只实现 Bootstrap 真正用到的 ProbeEmbeddingDimension；
// 其余四个方法这一轮用不上，返回错误而不是零值——用到了就会在测试里
// 立刻炸出来，而不是悄悄返回一个看起来合理但没意义的空结果。
type fakeRegistry struct {
	probeDim int
	probeErr error

	probed          bool
	lastBaseURL     string
	lastAPIKey      string
	lastEmbeddingID string
}

func (f *fakeRegistry) Chat(ctx context.Context, modelID string) (ChatModel, error) {
	return nil, errors.New("fakeRegistry.Chat: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Embedder(ctx context.Context, modelID string) (Embedder, error) {
	return nil, errors.New("fakeRegistry.Embedder: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Tokenizer(ctx context.Context, modelID string) (Tokenizer, error) {
	return nil, errors.New("fakeRegistry.Tokenizer: not implemented, this test should not reach here")
}

func (f *fakeRegistry) ActiveModelID(ctx context.Context, kind Kind) (string, error) {
	return "", errors.New("fakeRegistry.ActiveModelID: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Capabilities(ctx context.Context, modelID string) (Capabilities, error) {
	return Capabilities{}, errors.New("fakeRegistry.Capabilities: not implemented, this test should not reach here")
}

func (f *fakeRegistry) ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error) {
	f.probed = true
	f.lastBaseURL, f.lastAPIKey, f.lastEmbeddingID = baseURL, apiKey, modelID
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	return f.probeDim, nil
}

func (f *fakeRegistry) ResolveChatEndpoint(ctx context.Context, modelID string) (string, string, string, error) {
	return "", "", "", errors.New("fakeRegistry.ResolveChatEndpoint: not implemented, this test should not reach here")
}

// fakeQuerier 是 platform.Querier 的假实现,只记录被执行过的 SQL,
// 不真的连数据库。alterVectorColumns 只调用 Exec,所以 Query/QueryRow
// 在这些测试里不会被真的用到。
type fakeQuerier struct {
	execSQL  []string
	failExec bool
}

func (f *fakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execSQL = append(f.execSQL, sql)
	if f.failExec {
		return pgconn.CommandTag{}, errors.New("fake exec failure")
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("fakeQuerier.Query: not implemented, this test should not reach here")
}

func (f *fakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

// fakeTxManager 直接调用 fn,不模拟真正的提交/回滚。
//
// 【这意味着什么】用它测出来的"Bootstrap 出错时返回了 error"是可信的——
// 那是 Bootstrap 自己的控制流。但"出错时数据被回滚了"这条**不能**靠
// 这个假实现验证：fakeConfigRepo 的 map 一旦被写入就不会自动撤销，
// 真正的回滚行为由 platform.txManager（真的 Postgres 事务）保证，
// 那部分留给连真实数据库的集成测试（见 postgres_integration_test.go）。
// 这正是 helperDoc §13.7 记录过的教训：fake 和真实现的行为形状不一样时，
// 用 fake 测出来的"通过"说明不了什么。
type fakeTxManager struct {
	q platform.Querier
}

func (f *fakeTxManager) InTx(ctx context.Context, fn func(q platform.Querier) error) error {
	return fn(f.q)
}

// newTestUsecase 组一套全假的依赖,外加一个可断言的 fakeQuerier。
func newTestUsecase(repo *fakeConfigRepo, box *fakeSecretBox, reg *fakeRegistry, q *fakeQuerier) *Usecase {
	return NewUsecase(repo, box, reg, &fakeTxManager{q: q}, q)
}

func validBootstrapRequest() BootstrapRequest {
	return BootstrapRequest{
		BaseURL: "https://api.siliconflow.cn/v1",
		APIKey:  "sk-test-key",
		ChatModel: ChatModelInput{
			ModelID:         "Qwen/Qwen2.5-7B-Instruct",
			Capabilities:    Capabilities{Chat: true, Streaming: true, ToolCalling: true},
			ContextWindow:   32000,
			MaxOutputTokens: 4096,
			TokenizerType:   "cl100k_base",
		},
		EmbeddingModelID: "BAAI/bge-m3",
	}
}

// ════════════════════════════════════════════════════════════════
// Bootstrap
// ════════════════════════════════════════════════════════════════

func TestBootstrap_Success(t *testing.T) {
	repo := newFakeConfigRepo()
	box := &fakeSecretBox{}
	reg := &fakeRegistry{probeDim: 1024}
	q := &fakeQuerier{}
	uc := newTestUsecase(repo, box, reg, q)

	req := validBootstrapRequest()
	result, err := uc.Bootstrap(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, result)

	// 探测确实用了表单上的原始字段,不是别的来源。
	assert.True(t, reg.probed)
	assert.Equal(t, req.BaseURL, reg.lastBaseURL)
	assert.Equal(t, req.APIKey, reg.lastAPIKey)
	assert.Equal(t, req.EmbeddingModelID, reg.lastEmbeddingID)

	// Provider 落库,且能用 fakeSecretBox 解出原始 Key——
	// 验证的是"确实调用了 Seal"，不是真实密码学强度（那是 secretbox_test.go 的事）。
	require.Contains(t, repo.providers, result.Provider.ID)
	ciphertext := repo.keys[result.Provider.ID]
	plain, err := box.Open(ciphertext)
	require.NoError(t, err)
	assert.Equal(t, req.APIKey, string(plain))

	// 两个 Model 都落库,类型和维度对得上。
	require.Contains(t, repo.models, result.ChatModel.ID)
	require.Contains(t, repo.models, result.EmbeddingModel.ID)
	assert.Equal(t, KindChat, repo.models[result.ChatModel.ID].Kind)
	assert.Equal(t, KindEmbedding, repo.models[result.EmbeddingModel.ID].Kind)
	assert.Equal(t, 1024, repo.models[result.EmbeddingModel.ID].EmbeddingDim)
	assert.Equal(t, 0, repo.models[result.ChatModel.ID].EmbeddingDim, "聊天模型这一位应该恒为 0")

	// chat 模型的能力位原样保留。
	assert.Equal(t, req.ChatModel.Capabilities, repo.models[result.ChatModel.ID].Capabilities)
	// embedding 模型的能力位是 Bootstrap 自己填的,不是表单传的。
	assert.True(t, repo.models[result.EmbeddingModel.ID].Capabilities.Embedding)

	// 两张表的 ALTER + 建索引都跑了。
	require.Len(t, q.execSQL, 4, "两张表各一条 ALTER + 一条 CREATE INDEX")
	assert.Contains(t, q.execSQL[0], "document_chunks")
	assert.Contains(t, q.execSQL[0], "1024", "ALTER 的 SQL 里应该带着探测出的维度")
	assert.Contains(t, q.execSQL[1], "document_chunks")
	assert.Contains(t, q.execSQL[1], "halfvec_cosine_ops", "opclass 必须匹配 halfvec + 余弦距离,见 postgres.go 的注释")
	assert.Contains(t, q.execSQL[2], "memories")
	assert.Contains(t, q.execSQL[2], "1024")
	assert.Contains(t, q.execSQL[3], "memories")
	assert.Contains(t, q.execSQL[3], "halfvec_cosine_ops")
}

func TestBootstrap_ProbeFails_NothingPersisted(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeErr: fmt.Errorf("%w: connection refused", platform.ErrUpstream)}
	q := &fakeQuerier{}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	assert.Nil(t, result)
	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrUpstream, "探测失败要能映射成 502,不能变成看不出原因的 500")
	assert.Empty(t, repo.providers, "探测失败时不该写任何东西进去")
	assert.Empty(t, repo.models)
	assert.Empty(t, q.execSQL, "探测失败时事务应该从未开始,不该有任何 SQL 被执行")
}

func TestBootstrap_DimensionOutOfRange(t *testing.T) {
	tests := []struct {
		name string
		dim  int
	}{
		{"零维", 0},
		{"负数", -1},
		{"超过 HNSW 上限 4000", 4001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeConfigRepo()
			reg := &fakeRegistry{probeDim: tt.dim}
			q := &fakeQuerier{}
			uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

			result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

			assert.Nil(t, result)
			assert.ErrorIs(t, err, platform.ErrInvalid)
			assert.Empty(t, repo.providers, "维度不合法时不该写任何东西进去")
			assert.Empty(t, q.execSQL)
		})
	}
}

// 校验失败必须在探测之前挡住——没有理由为一个明显不合法的输入
// 打一次真实的网络请求。
func TestBootstrap_ValidationFailsBeforeProbe(t *testing.T) {
	base := validBootstrapRequest()

	tests := []struct {
		name   string
		mutate func(*BootstrapRequest)
	}{
		{"base url 为空", func(r *BootstrapRequest) { r.BaseURL = "" }},
		{"base url 不是合法 url", func(r *BootstrapRequest) { r.BaseURL = "not a url" }},
		{"api key 为空", func(r *BootstrapRequest) { r.APIKey = "" }},
		{"chat model id 为空", func(r *BootstrapRequest) { r.ChatModel.ModelID = "" }},
		{"context window 非正", func(r *BootstrapRequest) { r.ChatModel.ContextWindow = 0 }},
		{"max output tokens 非正", func(r *BootstrapRequest) { r.ChatModel.MaxOutputTokens = -1 }},
		{"tokenizer type 为空", func(r *BootstrapRequest) { r.ChatModel.TokenizerType = "" }},
		{"embedding model id 为空", func(r *BootstrapRequest) { r.EmbeddingModelID = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			tt.mutate(&req)

			repo := newFakeConfigRepo()
			reg := &fakeRegistry{probeDim: 1024}
			uc := newTestUsecase(repo, &fakeSecretBox{}, reg, &fakeQuerier{})

			_, err := uc.Bootstrap(context.Background(), req)

			assert.ErrorIs(t, err, platform.ErrInvalid)
			assert.False(t, reg.probed, "校验没过就不该发出探测请求")
		})
	}
}

// ALTER 失败时 Bootstrap 必须把错误原样传出去。
//
// 【不测"数据有没有回滚"】见 fakeTxManager 的注释——这条边界只有连
// 真实 Postgres 才能验证,这里只验证 Bootstrap 自己的错误传递路径。
func TestBootstrap_AlterFails_ErrorPropagates(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 1024}
	q := &fakeQuerier{failExec: true}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	_, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "alter vector columns")
}

func TestBootstrap_SealFails_ErrorPropagates(t *testing.T) {
	repo := newFakeConfigRepo()
	box := &fakeSecretBox{failSeal: true}
	reg := &fakeRegistry{probeDim: 1024}
	q := &fakeQuerier{}
	uc := newTestUsecase(repo, box, reg, q)

	_, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	require.Error(t, err)
	assert.Empty(t, repo.providers, "连 Key 都没加密成功,不该有任何东西落库")
}

// ════════════════════════════════════════════════════════════════
// ListProviders / ListModels
// ════════════════════════════════════════════════════════════════

func TestListProviders_PropagatesRepoError(t *testing.T) {
	repo := newFakeConfigRepo()
	repo.failOn, repo.err = "ListProviders", platform.ErrUpstream
	uc := newTestUsecase(repo, &fakeSecretBox{}, &fakeRegistry{}, &fakeQuerier{})

	_, err := uc.ListProviders(context.Background())

	assert.ErrorIs(t, err, platform.ErrUpstream)
}

func TestListModels_Empty(t *testing.T) {
	uc := newTestUsecase(newFakeConfigRepo(), &fakeSecretBox{}, &fakeRegistry{}, &fakeQuerier{})

	models, err := uc.ListModels(context.Background())

	require.NoError(t, err)
	assert.Empty(t, models)
}
