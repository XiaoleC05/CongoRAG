package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func (f *fakeRegistry) ActiveModel(ctx context.Context, kind Kind) (*Model, error) {
	return nil, errors.New("fakeRegistry.ActiveModel: not implemented, this test should not reach here")
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
// 不真的连数据库。alterVectorColumns 现在先查两件事（列类型、非 NULL
// 向量行数）再决定要不要 ALTER,所以 QueryRow 也得能应答。
type fakeQuerier struct {
	execSQL  []string
	failExec bool

	// columnTypes / vectorCounts / foreignCounts 按表名给那三条前置查询喂返回值。
	// 空 map 表示"列还没有维度、表里没有向量、也没有别的模型留下的向量"——
	// 0001 迁移刚建完、第一次引导时的状态，也就是 ALTER 该跑且跑了不丢数据的那一种。
	columnTypes  map[string]string
	vectorCounts map[string]int
	// foreignCounts 喂的是"非 NULL 但不是当前模型生成的"行数（按表名索引），
	// 用来测"维度没变但换了 embedding 模型"这一条拒绝分支。
	foreignCounts map[string]int
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
	table := f.tableFor(sql, args)
	switch {
	case strings.Contains(sql, "format_type"):
		return fakeRow{str: f.columnTypes[table]}
	case strings.Contains(sql, "embedding_model"):
		// 顺序要紧：这条也带 count(*)，必须先于下面那条分支判断。
		return fakeRow{num: f.foreignCounts[table]}
	case strings.Contains(sql, "count(*)"):
		return fakeRow{num: f.vectorCounts[table]}
	default:
		return fakeRow{err: fmt.Errorf("fakeQuerier.QueryRow: 不认识的 SQL: %s", sql)}
	}
}

// tableFor 认出这条 SQL 打在哪个表上。先看 SQL 文本里有没有写死的表名——
// 两条 count 查询都把表名拼进了 SQL；列类型那条只在 WHERE 里用 $1，
// 文本里没有表名，所以退回 args[0]。顺序不能反：foreignEmbeddingCount
// 的 $1 是模型名，先看 args[0] 会把它当成表名。
func (f *fakeQuerier) tableFor(sql string, args []any) string {
	for _, vc := range vectorColumns {
		if strings.Contains(sql, vc.table) {
			return vc.table
		}
	}
	if len(args) > 0 {
		if name, ok := args[0].(string); ok {
			return name
		}
	}
	return ""
}

// fakeRow 按目标类型分派：列类型查回 *string,行数查回 *int。
// 这两种目标在 alterVectorColumns 里就是唯一的两种,不再多加一层开关。
type fakeRow struct {
	str string
	num int
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("fakeRow.Scan: 期望 1 个目标,拿到 %d 个", len(dest))
	}
	switch d := dest[0].(type) {
	case *string:
		*d = r.str
	case *int:
		*d = r.num
	default:
		return fmt.Errorf("fakeRow.Scan: 不认识的目标类型 %T", dest[0])
	}
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
//
// 【第六个参数（重新索引端口）传 nil】绝大多数用例走的是"拒绝"路径，那里
// 根本到不了入队。要验证重建的用例直接给返回的 uc 赋一个假的 reindexer
// （同包，字段可写）——比给这个构造器再加一个参数要少改十几处调用点。
func newTestUsecase(repo *fakeConfigRepo, box *fakeSecretBox, reg *fakeRegistry, q *fakeQuerier) *Usecase {
	return NewUsecase(repo, box, reg, &fakeTxManager{q: q}, q, nil)
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

// 【校验用的值和落库的值必须是同一个】validateBootstrapRequest 用
// strings.TrimSpace 之后的串去查 ValidTokenizerTypes，而 newTokenizer
// （tokenizer.go）拿的是原值、不做 trim。落库时如果不 trim，" cl100k_base "
// 这一类的输入会通过校验、引导页照常 201，然后在之后每一条消息上失败——
// 正是 #6 要消掉的那个"保存时成功、聊天才报错"的形态。
func TestBootstrap_StoresTrimmedTokenizerType(t *testing.T) {
	repo := newFakeConfigRepo()
	q := &fakeQuerier{}
	uc := newTestUsecase(repo, &fakeSecretBox{}, &fakeRegistry{probeDim: 1024}, q)

	req := validBootstrapRequest()
	req.ChatModel.TokenizerType = "  cl100k_base\n"

	result, err := uc.Bootstrap(context.Background(), req)

	require.NoError(t, err)
	stored := repo.models[result.ChatModel.ID]
	require.NotNil(t, stored)
	assert.Equal(t, "cl100k_base", stored.TokenizerType,
		"存的必须是校验时用的那个（trim 过的）值，否则聊天路径按原值查表会失败")
}

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

// 取消引导/再次引导时最危险的一条：列类型已经是对的（比如同一套配置
// 保存第二次），那条 ALTER 必须一条都不跑。
//
// 【为什么这条是回归测试】USING NULL 让 ATColumnChangeRequiresRewrite 恒为
// true——即使目标 typmod 和当前完全相同，这条 ALTER 也会重写整张表并把
// 所有向量置成 NULL。所以"列已经是 halfvec(1024) 且表里有 12 行向量"这个
// 场景，修复前会静默清空整库向量；修复后只留下两条幂等的 CREATE INDEX。
func TestBootstrap_SkipsAlterWhenColumnTypeAlreadyMatches(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 1024}
	q := &fakeQuerier{
		columnTypes: map[string]string{
			"document_chunks": "halfvec(1024)",
			"memories":        "halfvec(1024)",
		},
		vectorCounts: map[string]int{"document_chunks": 12, "memories": 3},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	require.NoError(t, err, "列类型已经匹配时不该报错,也不该拒绝")
	require.NotNil(t, result)
	require.Len(t, q.execSQL, 2, "只剩两张表的 CREATE INDEX,ALTER 一条都不该跑")
	for _, sql := range q.execSQL {
		assert.NotContains(t, sql, "ALTER TABLE", "类型已经是 halfvec(1024) 了,再 ALTER 一次就会清空已有向量")
		assert.Contains(t, sql, "CREATE INDEX IF NOT EXISTS")
	}
}

// 类型确实要变（维度不同 / 列还没有维度）、而表里已经有向量时，
// 必须拒绝而不是静默清空。
func TestBootstrap_RefusesToWipeExistingVectors(t *testing.T) {
	tests := []struct {
		name         string
		columnTypes  map[string]string
		vectorCounts map[string]int
		refusedTable string // 应该在那张表上被拦下（错误消息里点名它）
		refusedRows  int
		// sqlBeforeRefusal 是拒绝发生前允许跑掉的 SQL 条数：前面那张空表
		// 已经改完列、建完索引（ALTER + CREATE INDEX 两条）。这些语句在
		// 真事务里会随这次拒绝一起回滚，不留下半个状态。
		sqlBeforeRefusal int
	}{
		{
			name:         "两张表都有向量且维度不同",
			columnTypes:  map[string]string{"document_chunks": "halfvec(1024)", "memories": "halfvec(1024)"},
			vectorCounts: map[string]int{"document_chunks": 12, "memories": 4},
			refusedTable: "document_chunks", refusedRows: 12, sqlBeforeRefusal: 0,
		},
		{
			name:         "只有 memories 有向量",
			columnTypes:  map[string]string{"document_chunks": "halfvec", "memories": "halfvec(1024)"},
			vectorCounts: map[string]int{"document_chunks": 0, "memories": 4},
			refusedTable: "memories", refusedRows: 4, sqlBeforeRefusal: 2,
		},
		{
			name:         "列还没有维度但已经有向量",
			columnTypes:  map[string]string{"document_chunks": "halfvec", "memories": "halfvec"},
			vectorCounts: map[string]int{"document_chunks": 3, "memories": 0},
			refusedTable: "document_chunks", refusedRows: 3, sqlBeforeRefusal: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeConfigRepo()
			reg := &fakeRegistry{probeDim: 768}
			q := &fakeQuerier{columnTypes: tt.columnTypes, vectorCounts: tt.vectorCounts}
			uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

			result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

			assert.Nil(t, result)
			// 【#39 之后用的是本包的 sentinel，不再是 platform.ErrConflict】
			// 语义也变了：从"拒绝到底"变成"需要用户明确同意"——前端按这个
			// type 弹确认框，带 allowEmbeddingReset 重发一次就能继续。
			// 它仍然映射成 HTTP 409（见 problem.go 的 classify），所以
			// "调用方要知道这是配置与已有数据的冲突"这条没有变。
			assert.ErrorIs(t, err, ErrEmbeddingResetRequired)
			assert.Contains(t, err.Error(), tt.refusedTable)
			assert.Contains(t, err.Error(), fmt.Sprintf("%d 行非 NULL 向量", tt.refusedRows),
				"错误消息要说清楚会毁掉多少数据")
			assert.Contains(t, err.Error(), "allowEmbeddingReset",
				"错误消息要告诉调用方怎么继续，不能只说不行")

			// 有向量的那张表一条 ALTER 都不能跑（前面的空表可以先改完，
			// 那一条在真事务里会随这次拒绝一起回滚）。
			require.Len(t, q.execSQL, tt.sqlBeforeRefusal)
			for _, sql := range q.execSQL {
				assert.NotContains(t, sql, tt.refusedTable, "表里有向量时不该动它的列")
			}
		})
	}
}

// 【维度没变、换的是模型：也必须拒绝】这是只看列类型会漏掉的那一条。
// 探测出来的维度与列上已有的维度相同，format_type 一比"什么都不用做"，
// 于是新的 model 行照写、Bootstrap 返回 201——可检索谓词
// embedding_model = $2 里的 $2 已经是新模型名，库里每一行都还是旧模型名，
// 一条都匹配不上。文档仍显示 ready、检索静默返回零条，正是 #5 的原始形态。
func TestBootstrap_RefusesWhenVectorsComeFromAnotherModel(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		// 列类型已经是目标值：只比类型的实现会在这里直接放过去。
		columnTypes:  map[string]string{"document_chunks": "halfvec(768)", "memories": "halfvec(768)"},
		vectorCounts: map[string]int{"document_chunks": 12, "memories": 4},
		// 12 行是别的模型生成的。
		foreignCounts: map[string]int{"document_chunks": 12},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	assert.Nil(t, result)
	// 【#39 之后是 ErrEmbeddingResetRequired，仍是 409】换模型属于"配置与
	// 已有数据冲突"，只是现在由用户确认之后可以继续，而不是一律拒绝。
	assert.ErrorIs(t, err, ErrEmbeddingResetRequired,
		"换模型属于配置与已有数据冲突，不是 500；只是需要用户明确同意清空重建")
	assert.Contains(t, err.Error(), "document_chunks")
	assert.Contains(t, err.Error(), "12", "错误消息要说清楚有多少行会被抛下")

	// 一条 ALTER / CREATE INDEX 都不该跑：列类型本来就对，而模型不对这件事
	// 不能靠改列解决。
	assert.Empty(t, q.execSQL, "维度相同时本就不该动列，但不能因此把换模型放过去")
}

// 【原样再保存一次不能算换模型】Bootstrap 每次都新铸一个 model 行 uuid，
// 判据如果比行 id，这个最常见的良性操作（轮换 Key、改一个填错的字段）
// 会被自己拒掉。所以比的是 provider 那侧的模型名。
func TestBootstrap_ResavingSameModelIsAllowed(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		columnTypes:  map[string]string{"document_chunks": "halfvec(768)", "memories": "halfvec(768)"},
		vectorCounts: map[string]int{"document_chunks": 12, "memories": 4},
		// 名字相同 → 一行都不算 foreign（fake 按表名喂值，这里全 0）。
		foreignCounts: map[string]int{},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	result, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	require.NoError(t, err, "同一个 embedding 模型再保存一次不该被拒")
	require.NotNil(t, result)
	// 剩下两条是 CREATE INDEX IF NOT EXISTS（幂等，每次都要确保索引在）；
	// 该被跳掉的只有那条破坏性的 ALTER。
	require.Len(t, q.execSQL, 2, "只该剩两条 CREATE INDEX")
	for _, sql := range q.execSQL {
		assert.NotContains(t, sql, "ALTER TABLE", "列类型已经对且模型没变，不该再改列")
	}
}

// 维度变了但表是空的（第一次引导之后、还没来得及上传任何文档就改了
// embedding 模型）：改列无害，照旧执行，拒绝的判据是"有没有向量"
// 而不是"要不要改列"。
func TestBootstrap_AltersWhenTablesAreEmpty(t *testing.T) {
	repo := newFakeConfigRepo()
	reg := &fakeRegistry{probeDim: 768}
	q := &fakeQuerier{
		columnTypes: map[string]string{"document_chunks": "halfvec(1024)", "memories": "halfvec(1024)"},
	}
	uc := newTestUsecase(repo, &fakeSecretBox{}, reg, q)

	_, err := uc.Bootstrap(context.Background(), validBootstrapRequest())

	require.NoError(t, err)
	require.Len(t, q.execSQL, 4)
	assert.Contains(t, q.execSQL[0], "halfvec(768)")
	assert.Contains(t, q.execSQL[2], "halfvec(768)")
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
		// 非空但不在支持集合里：只校验非空的话这个值会落库、这里返回 201,
		// 之后每条消息都失败（issue #6）。
		{"tokenizer type 不在支持集合里", func(r *BootstrapRequest) { r.ChatModel.TokenizerType = "p50k_base" }},
		{"tokenizer type 少写了 _base 后缀", func(r *BootstrapRequest) { r.ChatModel.TokenizerType = "o200k" }},
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
