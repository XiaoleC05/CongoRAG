package knowledge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// fakeRepo 是 KBRepo 的内存实现，用来在不连数据库的情况下测业务逻辑。
//
// Usecase 持有的是接口而不是 *PgRepo，所以塞这个进去业务逻辑就能跑，
// 不需要 PostgreSQL、不需要容器、不需要迁移。整个测试跑完是毫秒级的。
var _ KBRepo = (*fakeRepo)(nil)

type fakeRepo struct {
	kbs []*KB

	// 让指定的方法返回错误，用来测"repo 出错了，usecase 会不会正确处理"
	failOn string
	err    error
}

func (f *fakeRepo) Insert(ctx context.Context, q platform.Querier, kb *KB) error {
	if f.failOn == "Insert" {
		return f.err
	}
	f.kbs = append(f.kbs, kb)
	return nil
}

func (f *fakeRepo) List(ctx context.Context, q platform.Querier) ([]*KB, error) {
	if f.failOn == "List" {
		return nil, f.err
	}
	return f.kbs, nil
}

func (f *fakeRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*KB, error) {
	if f.failOn == "ByID" {
		return nil, f.err
	}
	for _, kb := range f.kbs {
		if kb.ID == id {
			return kb, nil
		}
	}
	return nil, platform.ErrNotFound
}

func (f *fakeRepo) Rename(ctx context.Context, q platform.Querier, id uuid.UUID, newName string, updatedAt time.Time) error {
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
	return platform.ErrNotFound
}

func (f *fakeRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) error {
	if f.failOn == "Delete" {
		return f.err
	}
	for i, kb := range f.kbs {
		if kb.ID == id {
			f.kbs = append(f.kbs[:i], f.kbs[i+1:]...)
			return nil
		}
	}
	return nil // 和 PgRepo 一致：删不存在的东西算成功（幂等）
}

// ────────────────────────────────────────────────────────────────
// 文档相关的假实现。KB-only 的测试（Create/List/Get/Rename）不会
// 碰到它们，但 Usecase 的构造函数现在需要全部九个依赖——
// newTestUsecase 内部用最简单的空壳补上剩下八个，让那些测试的调用
// 语法不用变。真正测 Upload/ProcessDocument/对账逻辑的测试
// 用 newFullTestUsecase，可以拿到每个假实现的指针去断言调用记录。
// ────────────────────────────────────────────────────────────────

var _ DocRepo = (*fakeDocRepo)(nil)

type fakeDocRepo struct {
	docs []*Document

	failOn string
	err    error
}

func (f *fakeDocRepo) Insert(ctx context.Context, q platform.Querier, d *Document) error {
	if f.failOn == "Insert" {
		return f.err
	}
	f.docs = append(f.docs, d)
	return nil
}

func (f *fakeDocRepo) ByID(ctx context.Context, q platform.Querier, id uuid.UUID) (*Document, error) {
	if f.failOn == "ByID" {
		return nil, f.err
	}
	for _, d := range f.docs {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, platform.ErrNotFound
}

func (f *fakeDocRepo) UpdateStatus(ctx context.Context, q platform.Querier, id uuid.UUID, from, to Status) error {
	if f.failOn == "UpdateStatus" {
		return f.err
	}
	if !from.CanTransition(to) {
		return errors.New("illegal transition in fake")
	}
	for _, d := range f.docs {
		if d.ID == id {
			if d.Status != from {
				return platform.ErrConflict
			}
			d.Status = to
			return nil
		}
	}
	return platform.ErrNotFound
}

func (f *fakeDocRepo) ListByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]*Document, error) {
	if f.failOn == "ListByKnowledgeBase" {
		return nil, f.err
	}
	var out []*Document
	for _, d := range f.docs {
		if d.KnowledgeBaseID == kbID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDocRepo) Delete(ctx context.Context, q platform.Querier, id uuid.UUID) (string, error) {
	if f.failOn == "Delete" {
		return "", f.err
	}
	for i, d := range f.docs {
		if d.ID == id {
			f.docs = append(f.docs[:i], f.docs[i+1:]...)
			return d.StorageKey, nil
		}
	}
	return "", platform.ErrNotFound
}

func (f *fakeDocRepo) DeleteByKnowledgeBase(ctx context.Context, q platform.Querier, kbID uuid.UUID) ([]string, error) {
	if f.failOn == "DeleteByKnowledgeBase" {
		return nil, f.err
	}
	var keys []string
	remaining := f.docs[:0:0] //nolint:staticcheck // 显式清零重建，避免和原切片共享底层数组
	for _, d := range f.docs {
		if d.KnowledgeBaseID == kbID {
			keys = append(keys, d.StorageKey)
			continue
		}
		remaining = append(remaining, d)
	}
	f.docs = remaining
	return keys, nil
}

func (f *fakeDocRepo) ExistingStorageKeys(ctx context.Context, q platform.Querier, keys []string) (map[string]bool, error) {
	if f.failOn == "ExistingStorageKeys" {
		return nil, f.err
	}
	out := make(map[string]bool)
	for _, key := range keys {
		for _, d := range f.docs {
			if d.StorageKey == key {
				out[key] = true
				break
			}
		}
	}
	return out, nil
}

var _ FileStore = (*fakeFileStore)(nil)

// fakeFileStore 用内存 map 模拟磁盘，同时记录方法调用顺序——
// 上传写路径的顺序本身就是要测试的东西（WriteTemp 必须先于 Commit，
// Commit 必须先于数据库事务），记录顺序比只记录"调过没调过"更有用。
type fakeFileStore struct {
	tmp   map[string][]byte
	files map[string]FileInfo // storageKey -> info（内容单独存在 contents）

	contents map[string][]byte // storageKey -> 内容，Open 用它

	calls []string

	failWriteTemp bool
	failCommit    bool
	failOpen      bool

	nextTmpSeq int
}

func newFakeFileStore() *fakeFileStore {
	return &fakeFileStore{
		tmp:      map[string][]byte{},
		files:    map[string]FileInfo{},
		contents: map[string][]byte{},
	}
}

func (f *fakeFileStore) WriteTemp(ctx context.Context, r io.Reader) (string, int64, error) {
	f.calls = append(f.calls, "WriteTemp")
	if f.failWriteTemp {
		return "", 0, errors.New("fake write temp failure")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", 0, err
	}
	f.nextTmpSeq++
	tmpPath := "/tmp/fake-upload-" + uuid.NewString()
	f.tmp[tmpPath] = data
	return tmpPath, int64(len(data)), nil
}

func (f *fakeFileStore) Commit(ctx context.Context, tmpPath, storageKey string) error {
	f.calls = append(f.calls, "Commit")
	if f.failCommit {
		return errors.New("fake commit failure")
	}
	data, ok := f.tmp[tmpPath]
	if !ok {
		return errors.New("fake: tmp file not found, WriteTemp must run before Commit")
	}
	delete(f.tmp, tmpPath)
	f.contents[storageKey] = data
	f.files[storageKey] = FileInfo{StorageKey: storageKey, ModTime: time.Now()}
	return nil
}

func (f *fakeFileStore) Open(ctx context.Context, storageKey string) (io.ReadCloser, error) {
	f.calls = append(f.calls, "Open")
	if f.failOpen {
		return nil, errors.New("fake open failure")
	}
	data, ok := f.contents[storageKey]
	if !ok {
		return nil, errors.New("fake: file not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeFileStore) RemoveTemp(ctx context.Context, tmpPath string) error {
	f.calls = append(f.calls, "RemoveTemp")
	delete(f.tmp, tmpPath) // Commit 成功后本来就不在了，delete 一个不存在的 key 是安全的
	return nil
}

func (f *fakeFileStore) Remove(ctx context.Context, storageKey string) error {
	f.calls = append(f.calls, "Remove")
	delete(f.contents, storageKey)
	delete(f.files, storageKey)
	return nil
}

func (f *fakeFileStore) List(ctx context.Context) ([]FileInfo, error) {
	out := make([]FileInfo, 0, len(f.files))
	for _, info := range f.files {
		out = append(out, info)
	}
	return out, nil
}

// setModTime 让测试能控制某个文件"看起来多旧"，不用真的等待时间流逝——
// 对账逻辑的 grace period 判断全靠 ModTime，测试必须能直接摆放这个值。
func (f *fakeFileStore) setModTime(storageKey string, t time.Time) {
	info := f.files[storageKey]
	info.StorageKey = storageKey
	info.ModTime = t
	f.files[storageKey] = info
}

var _ Enqueuer = (*fakeEnqueuer)(nil)

type fakeEnqueuer struct {
	enqueued []uuid.UUID
	fail     bool
}

func (f *fakeEnqueuer) EnqueueProcessing(ctx context.Context, q platform.Querier, documentID uuid.UUID) error {
	if f.fail {
		return errors.New("fake enqueue failure")
	}
	f.enqueued = append(f.enqueued, documentID)
	return nil
}

var _ ChunkIndexer = (*fakeChunkIndexer)(nil)

type fakeChunkIndexer struct {
	indexed map[uuid.UUID][]domain.Chunk
	fail    bool
}

func newFakeChunkIndexer() *fakeChunkIndexer {
	return &fakeChunkIndexer{indexed: map[uuid.UUID][]domain.Chunk{}}
}

func (f *fakeChunkIndexer) IndexDocument(ctx context.Context, q platform.Querier, docID uuid.UUID, chunks []domain.Chunk) error {
	if f.fail {
		return errors.New("fake index failure")
	}
	f.indexed[docID] = chunks
	return nil
}

func (f *fakeChunkIndexer) DeleteByDocument(ctx context.Context, q platform.Querier, docID uuid.UUID) error {
	delete(f.indexed, docID)
	return nil
}

var _ FileCleaner = (*fakeFileCleaner)(nil)

type fakeFileCleaner struct {
	scheduled [][]string
}

func (f *fakeFileCleaner) Schedule(storageKeys []string) {
	f.scheduled = append(f.scheduled, storageKeys)
}

var _ platform.PeriodicScheduler = (*fakeScheduler)(nil)

// fakeScheduler 不真的定时跑，只记住注册的函数——测试直接调用它来
// 模拟"这一轮周期任务被触发了"，不需要真的等 10 分钟。
type fakeScheduler struct {
	registered map[string]func(ctx context.Context) error
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{registered: map[string]func(ctx context.Context) error{}}
}

func (f *fakeScheduler) RegisterPeriodic(name string, every time.Duration, fn func(ctx context.Context) error) {
	f.registered[name] = fn
}

var _ platform.TxManager = (*fakeTxManager)(nil)

// fakeTxManager 直接调用 fn，不模拟真正的提交/回滚——和 internal/llm 的
// 同名假实现一样的取舍和一样的限制，见那边的注释：这个假实现能验证
// "出错时返回了 error"，不能验证"出错时数据被回滚了"，后者需要连
// 真实数据库的集成测试。
type fakeTxManager struct {
	q platform.Querier
}

func (f *fakeTxManager) InTx(ctx context.Context, fn func(q platform.Querier) error) error {
	return fn(f.q)
}

// newTestUsecase 造一个只关心知识库 CRUD 的 Usecase——文档相关的八个
// 依赖全部用最简单的空壳填上。KB-only 的测试从来不会真的调用到
// Upload/ProcessDocument，这些空壳只是为了满足构造函数的参数数量。
func newTestUsecase(repo KBRepo) *Usecase {
	return NewUsecase(
		repo,
		&fakeDocRepo{},
		newFakeFileStore(),
		&fakeEnqueuer{},
		newFakeChunkIndexer(),
		&fakeFileCleaner{},
		newFakeScheduler(),
		&fakeTxManager{},
		nil,
	)
}

// testDeps 打包全套假依赖的指针，方便 Upload/Delete/ProcessDocument 的
// 测试在调用 Usecase 方法之后回头断言"这个假实现被怎么调用了"。
type testDeps struct {
	kbRepo  *fakeRepo
	docRepo *fakeDocRepo
	files   *fakeFileStore
	enq     *fakeEnqueuer
	indexer *fakeChunkIndexer
	cleaner *fakeFileCleaner
	sched   *fakeScheduler
	uc      *Usecase
}

func newFullTestUsecase() *testDeps {
	d := &testDeps{
		kbRepo:  &fakeRepo{},
		docRepo: &fakeDocRepo{},
		files:   newFakeFileStore(),
		enq:     &fakeEnqueuer{},
		indexer: newFakeChunkIndexer(),
		cleaner: &fakeFileCleaner{},
		sched:   newFakeScheduler(),
	}
	d.uc = NewUsecase(d.kbRepo, d.docRepo, d.files, d.enq, d.indexer, d.cleaner, d.sched, &fakeTxManager{}, nil)
	return d
}

func TestCleanName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"正常名字", "我的知识库", "我的知识库", false},
		{"首尾空格被去掉", "  我的知识库  ", "我的知识库", false},
		{"空字符串", "", "", true},
		{"只有空格", "   ", "", true},
		{"只有制表符和换行", "\t\n ", "", true},
		{"单字", "a", "a", false},
		{"刚好到上限", strings.Repeat("a", maxNameLen), strings.Repeat("a", maxNameLen), false},
		{"超出一个字符", strings.Repeat("a", maxNameLen+1), "", true},
		// 按字符数算而不是字节数：中文一个字三字节，按字节算限制会随语言变化
		{"中文刚好到上限", strings.Repeat("知", maxNameLen), strings.Repeat("知", maxNameLen), false},
		{"中文超限", strings.Repeat("知", maxNameLen+1), "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanName(tt.input)
			if tt.wantErr {
				assert.ErrorIs(t, err, platform.ErrInvalid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ════════════════════════════════════════════════════════════════
// Create
// ════════════════════════════════════════════════════════════════

func TestCreate_Success(t *testing.T) {
	repo := &fakeRepo{}
	uc := newTestUsecase(repo)

	kb, err := uc.Create(context.Background(), "我的知识库")

	require.NoError(t, err)
	require.NotNil(t, kb)

	assert.NotEqual(t, uuid.Nil, kb.ID, "id 应该在 usecase 层生成，不能是零值")
	assert.Equal(t, "我的知识库", kb.Name)
	assert.False(t, kb.CreatedAt.IsZero())
	assert.Equal(t, kb.CreatedAt, kb.UpdatedAt, "新建时两个时间戳应该完全相同")

	// 确认真的写进 repo 了
	require.Len(t, repo.kbs, 1)
	assert.Equal(t, kb.ID, repo.kbs[0].ID)
}

func TestCreate_InvalidName(t *testing.T) {
	for _, input := range []string{"", "   ", "\t"} {
		t.Run("input="+input, func(t *testing.T) {
			repo := &fakeRepo{}
			uc := newTestUsecase(repo)

			kb, err := uc.Create(context.Background(), input)

			assert.Nil(t, kb)
			assert.ErrorIs(t, err, platform.ErrInvalid)
			assert.Empty(t, repo.kbs, "校验不过时不应该碰 repo")
		})
	}
}

func TestCreate_PropagatesRepoError(t *testing.T) {
	repo := &fakeRepo{failOn: "Insert", err: platform.ErrDuplicateKey}
	uc := newTestUsecase(repo)

	_, err := uc.Create(context.Background(), "我的知识库")

	// errors.Is 要能穿透 usecase 用 %w 包的那一层，认到底下的 sentinel。
	// 写成 %v 的话这里会失败，而那种 bug 在别处看不出来。
	assert.ErrorIs(t, err, platform.ErrDuplicateKey)
}

// ════════════════════════════════════════════════════════════════
// 校验规则必须两个入口一致
// ════════════════════════════════════════════════════════════════

func TestRename_UsesSameRuleAsCreate(t *testing.T) {
	configuredRepo := func() (*fakeRepo, *KB) {
		repo := &fakeRepo{}
		kb := &KB{ID: uuid.New(), Name: "原名", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		repo.kbs = append(repo.kbs, kb)
		return repo, kb
	}

	// Create 拒绝的输入，Rename 也必须拒绝。
	// 不抽 cleanName 的话，这条测试会失败——而"两个入口规则不一致"
	// 这种 bug 光靠看代码很容易漏。
	for _, input := range []string{"", "   ", "\t"} {
		t.Run("input="+input, func(t *testing.T) {
			repo, kb := configuredRepo()
			uc := newTestUsecase(repo)

			err := uc.Rename(context.Background(), kb.ID, input)

			assert.ErrorIs(t, err, platform.ErrInvalid)
			assert.Equal(t, "原名", repo.kbs[0].Name, "校验不过时不应该改动数据")
		})
	}
}

func TestRename_Success(t *testing.T) {
	// 原始时间戳设成一小时前，不用 time.Now()：
	// 创建和改名之间只隔几微秒，time.Now() 可能返回同一个值，
	// assert.After 就会随机失败（flaky test）。
	old := time.Now().Add(-time.Hour)

	repo := &fakeRepo{}
	kb := &KB{ID: uuid.New(), Name: "原名", CreatedAt: old, UpdatedAt: old}
	repo.kbs = append(repo.kbs, kb)
	uc := newTestUsecase(repo)

	err := uc.Rename(context.Background(), kb.ID, "  新名字  ")

	require.NoError(t, err)
	assert.Equal(t, "新名字", kb.Name, "应该存 trim 过的名字，不是原样")
	assert.True(t, kb.UpdatedAt.After(old), "改名应该刷新 updatedAt")
}

func TestRename_NotFound(t *testing.T) {
	uc := newTestUsecase(&fakeRepo{})

	err := uc.Rename(context.Background(), uuid.New(), "新名字")

	assert.ErrorIs(t, err, platform.ErrNotFound)
}

// ════════════════════════════════════════════════════════════════
// List / Get / Delete
// ════════════════════════════════════════════════════════════════

func TestList(t *testing.T) {
	repo := &fakeRepo{}
	uc := newTestUsecase(repo)

	// 空的时候应该返回 nil 而不是报错
	kbs, err := uc.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, kbs)

	_, err = uc.Create(context.Background(), "a")
	require.NoError(t, err)
	_, err = uc.Create(context.Background(), "b")
	require.NoError(t, err)

	kbs, err = uc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, kbs, 2)
}

func TestGet_NotFound(t *testing.T) {
	uc := newTestUsecase(&fakeRepo{})

	kb, err := uc.Get(context.Background(), uuid.New())

	assert.Nil(t, kb)
	assert.ErrorIs(t, err, platform.ErrNotFound)
}

func TestDelete(t *testing.T) {
	repo := &fakeRepo{}
	uc := newTestUsecase(repo)

	kb, err := uc.Create(context.Background(), "要删的")
	require.NoError(t, err)

	require.NoError(t, uc.Delete(context.Background(), kb.ID))

	kbs, err := uc.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, kbs)
}

// ════════════════════════════════════════════════════════════════
// 错误包装
// ════════════════════════════════════════════════════════════════

func TestErrorsAreWrappedNotSwallowed(t *testing.T) {
	repo := &fakeRepo{failOn: "List", err: platform.ErrUpstream}
	uc := newTestUsecase(repo)

	_, err := uc.List(context.Background())

	require.Error(t, err)
	// 两头都要满足：能认到底层的 sentinel，又保留了本层的上下文
	assert.ErrorIs(t, err, platform.ErrUpstream)
	assert.Contains(t, err.Error(), "list knowledge bases",
		"错误信息里应该有本层的上下文，方便定位是哪一步出的错")
	assert.False(t, errors.Is(err, platform.ErrNotFound), "别把不相干的 sentinel 也认了")
}

// ════════════════════════════════════════════════════════════════
// CanTransition —— 纯函数，状态迁移表
// ════════════════════════════════════════════════════════════════

func TestStatus_CanTransition(t *testing.T) {
	tests := []struct {
		from, to Status
		want     bool
	}{
		{StatusQueued, StatusProcessing, true},
		{StatusQueued, StatusFailed, true},
		{StatusQueued, StatusReady, false}, // 不能跳过 processing
		{StatusProcessing, StatusReady, true},
		{StatusProcessing, StatusFailed, true},
		{StatusProcessing, StatusQueued, true}, // 允许重试
		{StatusReady, StatusProcessing, true},  // 重新索引
		{StatusReady, StatusQueued, false},
		{StatusReady, StatusFailed, false},
		{StatusFailed, StatusQueued, true}, // 重试
		{StatusFailed, StatusProcessing, false},
		{StatusFailed, StatusReady, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.from.CanTransition(tt.to))
		})
	}
}

// ════════════════════════════════════════════════════════════════
// parseAndChunk —— 纯函数，切分逻辑
// ════════════════════════════════════════════════════════════════

func TestParseAndChunk_SplitsOnBlankLines(t *testing.T) {
	content := []byte("第一段。\n\n第二段。\n\n第三段。")

	chunks := parseAndChunk(content)

	// 三段都不长，会被贪心地合并进同一个块（都在 maxChunkChars 以内）。
	require.Len(t, chunks, 1)
	assert.Contains(t, chunks[0].Content, "第一段")
	assert.Contains(t, chunks[0].Content, "第二段")
	assert.Contains(t, chunks[0].Content, "第三段")
}

func TestParseAndChunk_SplitsWhenExceedingLimit(t *testing.T) {
	// 两段，每段都接近上限，两段加起来必然超限——应该落进两个不同的块。
	para := strings.Repeat("字", maxChunkChars-10)
	content := []byte(para + "\n\n" + para)

	chunks := parseAndChunk(content)

	require.Len(t, chunks, 2)
	assert.Equal(t, para, chunks[0].Content)
	assert.Equal(t, para, chunks[1].Content)
}

func TestParseAndChunk_HardSplitsOversizedParagraph(t *testing.T) {
	// 单个段落本身就超过上限：必须被硬切成多块，且每块都不超过上限。
	huge := strings.Repeat("x", maxChunkChars*2+5)

	chunks := parseAndChunk([]byte(huge))

	require.Len(t, chunks, 3)
	for _, c := range chunks {
		assert.LessOrEqual(t, len([]rune(c.Content)), maxChunkChars)
	}
	// 拼回去应该等于原文——硬切不能丢字符。
	var rebuilt strings.Builder
	for _, c := range chunks {
		rebuilt.WriteString(c.Content)
	}
	assert.Equal(t, huge, rebuilt.String())
}

func TestParseAndChunk_EmptyInput(t *testing.T) {
	assert.Empty(t, parseAndChunk([]byte("")))
	assert.Empty(t, parseAndChunk([]byte("   \n\n  \n")))
}

func TestParseAndChunk_NormalizesWindowsLineEndings(t *testing.T) {
	crlf := parseAndChunk([]byte("段一\r\n\r\n段二"))
	lf := parseAndChunk([]byte("段一\n\n段二"))
	require.Len(t, crlf, 1)
	require.Len(t, lf, 1)
	assert.Equal(t, lf[0].Content, crlf[0].Content)
}

// ════════════════════════════════════════════════════════════════
// Upload —— 写路径的顺序和事务边界
// ════════════════════════════════════════════════════════════════

func TestUpload_Success(t *testing.T) {
	d := newFullTestUsecase()
	kbID := uuid.New()

	doc, err := d.uc.Upload(context.Background(), kbID, "笔记.md", strings.NewReader("内容"))

	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, kbID, doc.KnowledgeBaseID)
	assert.Equal(t, "笔记.md", doc.Filename)
	assert.Equal(t, StatusQueued, doc.Status)
	assert.True(t, strings.HasSuffix(doc.StorageKey, ".md"), "storage_key 应该带上原始扩展名，方便运维用肉眼辨认")
	assert.Equal(t, int64(len([]byte("内容"))), doc.ByteSize,
		"byte_size 必须是服务端自己数出来的实际字节数，不能是 0 或客户端上报的值")

	// 顺序断言：WriteTemp 必须先于 Commit（这是写路径顺序本身，不是细节）。
	require.GreaterOrEqual(t, len(d.files.calls), 2)
	assert.Equal(t, "WriteTemp", d.files.calls[0])
	assert.Equal(t, "Commit", d.files.calls[1])

	// 数据库记录和入队都发生了。
	require.Len(t, d.docRepo.docs, 1)
	assert.Equal(t, doc.ID, d.docRepo.docs[0].ID)
	require.Len(t, d.enq.enqueued, 1)
	assert.Equal(t, doc.ID, d.enq.enqueued[0])

	// RemoveTemp 最终被调用过（defer），且此刻临时文件早已被 Commit
	// rename 走——调用它不应该报错（fakeFileStore.RemoveTemp 对不存在
	// 的 key 也返回 nil，验证的正是这条"no-op"约定）。
	assert.Contains(t, d.files.calls, "RemoveTemp")
}

func TestUpload_EmptyFilenameRejected(t *testing.T) {
	d := newFullTestUsecase()

	_, err := d.uc.Upload(context.Background(), uuid.New(), "   ", strings.NewReader("x"))

	assert.ErrorIs(t, err, platform.ErrInvalid)
	assert.Empty(t, d.files.calls, "校验不过时不该碰文件系统")
}

func TestUpload_FilenameTooLong(t *testing.T) {
	d := newFullTestUsecase()
	longName := strings.Repeat("a", maxUploadFilenameLen+1) + ".txt"

	_, err := d.uc.Upload(context.Background(), uuid.New(), longName, strings.NewReader("x"))

	assert.ErrorIs(t, err, platform.ErrInvalid)
}

func TestUpload_WriteTempFails_NothingElseHappens(t *testing.T) {
	d := newFullTestUsecase()
	d.files.failWriteTemp = true

	_, err := d.uc.Upload(context.Background(), uuid.New(), "a.txt", strings.NewReader("x"))

	require.Error(t, err)
	assert.Empty(t, d.docRepo.docs)
	assert.Empty(t, d.enq.enqueued)
}

func TestUpload_CommitFails_NoDBRecordCreated(t *testing.T) {
	d := newFullTestUsecase()
	d.files.failCommit = true

	_, err := d.uc.Upload(context.Background(), uuid.New(), "a.txt", strings.NewReader("x"))

	require.Error(t, err)
	assert.Empty(t, d.docRepo.docs, "rename 都没成功，不该有数据库记录")
	assert.Empty(t, d.enq.enqueued)
}

// 事务失败时，文件已经落位（Commit 已经成功）——这正是"孤儿文件"的定义。
// Upload 本身不负责清理它，那是对账 job 的职责，这条测试只确认
// Upload 老实地把错误报出来，没有假装成功，也没有自己动手去删文件
// （删文件的决定权应该完全在对账逻辑里，理由是宽限期——Upload 这一刻
// 不知道"到底是真失败还是马上要重试成功"）。
//
// 【这条测不了"数据库记录被回滚"】fakeTxManager 直接调用 fn，不模拟
// 真正的提交/回滚（见它自己的注释）——所以 EnqueueProcessing 失败之前
// docRepo.Insert 已经"成功"过一次，fakeDocRepo 里会留下这一行，
// 而真实的 Postgres 事务会把它连同这次失败一起回滚掉。这个差异正是
// helperDoc 记录过的教训：fake 和真实现的行为形状不一样的地方，
// fake 测不出来，需要连真实数据库的集成测试兜底（M1 的 CRUD 那批测试
// 已经踩过一次这个坑，见知识库那边的教训）。这里只断言 fake 能验证的
// 那部分：Upload 把错误报出来了，文件确实落了地。
func TestUpload_TxFails_FileBecomesOrphan(t *testing.T) {
	d := newFullTestUsecase()
	d.enq.fail = true

	_, err := d.uc.Upload(context.Background(), uuid.New(), "a.txt", strings.NewReader("x"))

	require.Error(t, err)
	assert.NotEmpty(t, d.files.contents, "文件已经落位——这就是孤儿；数据库层面的回滚需要真实事务，这里的 fake 验证不了")
}

// ════════════════════════════════════════════════════════════════
// Delete —— 现在要清磁盘了
// ════════════════════════════════════════════════════════════════

func TestDelete_SchedulesCleanupForOwnedDocuments(t *testing.T) {
	d := newFullTestUsecase()
	kb, err := d.uc.Create(context.Background(), "要删的库")
	require.NoError(t, err)

	// 直接往假 repo 里塞两份"属于这个知识库"的文档，不必真的走 Upload。
	d.docRepo.docs = []*Document{
		{ID: uuid.New(), KnowledgeBaseID: kb.ID, StorageKey: "key-1", Status: StatusReady},
		{ID: uuid.New(), KnowledgeBaseID: kb.ID, StorageKey: "key-2", Status: StatusReady},
	}

	require.NoError(t, d.uc.Delete(context.Background(), kb.ID))

	require.Len(t, d.cleaner.scheduled, 1)
	assert.ElementsMatch(t, []string{"key-1", "key-2"}, d.cleaner.scheduled[0])
	assert.Empty(t, d.docRepo.docs, "文档记录应该已经被删掉")
}

func TestDelete_NoDocuments_SchedulesEmptyCleanup(t *testing.T) {
	d := newFullTestUsecase()
	kb, err := d.uc.Create(context.Background(), "空知识库")
	require.NoError(t, err)

	require.NoError(t, d.uc.Delete(context.Background(), kb.ID))

	require.Len(t, d.cleaner.scheduled, 1)
	assert.Empty(t, d.cleaner.scheduled[0])
}

// ════════════════════════════════════════════════════════════════
// ProcessDocument —— worker 侧的状态机 + 重试安全性
// ════════════════════════════════════════════════════════════════

func TestProcessDocument_Success(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{
		{ID: docID, StorageKey: "doc-1.txt", Status: StatusQueued},
	}
	d.files.contents["doc-1.txt"] = []byte("一些内容\n\n另一段内容")

	err := d.uc.ProcessDocument(context.Background(), docID)

	require.NoError(t, err)
	assert.Equal(t, StatusReady, d.docRepo.docs[0].Status)
	require.Contains(t, d.indexer.indexed, docID)
	assert.NotEmpty(t, d.indexer.indexed[docID])
}

// 同一个任务被 River 重试：进程崩溃发生在"标记 processing 成功之后、
// IndexDocument 还没跑完之前"，第二次调用看到的初始状态就是 processing
// 而不是 queued。ProcessDocument 必须能接着跑完，不能因为
// "queued->processing 这次 CAS 会失败"就直接报错。
func TestProcessDocument_RetryFromProcessing_Succeeds(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{
		{ID: docID, StorageKey: "doc-1.txt", Status: StatusProcessing},
	}
	d.files.contents["doc-1.txt"] = []byte("内容")

	err := d.uc.ProcessDocument(context.Background(), docID)

	require.NoError(t, err)
	assert.Equal(t, StatusReady, d.docRepo.docs[0].Status)
}

func TestProcessDocument_UnexpectedStatus_Rejected(t *testing.T) {
	for _, status := range []Status{StatusReady, StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			d := newFullTestUsecase()
			docID := uuid.New()
			d.docRepo.docs = []*Document{{ID: docID, Status: status}}

			err := d.uc.ProcessDocument(context.Background(), docID)

			assert.ErrorIs(t, err, platform.ErrConflict)
		})
	}
}

func TestProcessDocument_OpenFails_MarksFailed(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{
		{ID: docID, StorageKey: "missing.txt", Status: StatusQueued},
	}
	d.files.failOpen = true

	err := d.uc.ProcessDocument(context.Background(), docID)

	require.Error(t, err)
	assert.Equal(t, StatusFailed, d.docRepo.docs[0].Status,
		"打开文件失败必须把文档标记为 failed，不能让它卡在 processing 里")
}

func TestProcessDocument_IndexFails_MarksFailed(t *testing.T) {
	d := newFullTestUsecase()
	docID := uuid.New()
	d.docRepo.docs = []*Document{
		{ID: docID, StorageKey: "doc-1.txt", Status: StatusQueued},
	}
	d.files.contents["doc-1.txt"] = []byte("内容")
	d.indexer.fail = true

	err := d.uc.ProcessDocument(context.Background(), docID)

	require.Error(t, err)
	assert.Equal(t, StatusFailed, d.docRepo.docs[0].Status)
}

// ════════════════════════════════════════════════════════════════
// StartReconciler —— 孤儿对账
// ════════════════════════════════════════════════════════════════

func TestStartReconciler_RemovesOnlyOldUnknownFiles(t *testing.T) {
	d := newFullTestUsecase()
	d.uc.StartReconciler(context.Background())
	reconcile, ok := d.sched.registered["orphan-files"]
	require.True(t, ok, "必须用固定的名字注册周期任务")

	old := time.Now().Add(-1 * time.Hour)
	recent := time.Now()

	// 四种情况都要覆盖：
	//   old-orphan   老、数据库不认领   → 应该被删
	//   old-known    老、数据库认领着   → 不该被删（这是正常的旧文件）
	//   recent-orphan 新、数据库不认领  → 不该被删（可能是正在上传中的）
	//   recent-known  新、数据库认领着  → 不该被删
	d.files.contents["old-orphan"] = []byte("x")
	d.files.setModTime("old-orphan", old)

	d.files.contents["old-known"] = []byte("x")
	d.files.setModTime("old-known", old)
	d.docRepo.docs = append(d.docRepo.docs, &Document{ID: uuid.New(), StorageKey: "old-known"})

	d.files.contents["recent-orphan"] = []byte("x")
	d.files.setModTime("recent-orphan", recent)

	require.NoError(t, reconcile(context.Background()))

	_, oldOrphanStillThere := d.files.contents["old-orphan"]
	assert.False(t, oldOrphanStillThere, "老且没人认领的文件应该被清理")

	_, oldKnownStillThere := d.files.contents["old-known"]
	assert.True(t, oldKnownStillThere, "有数据库记录认领着，不该被删")

	_, recentOrphanStillThere := d.files.contents["recent-orphan"]
	assert.True(t, recentOrphanStillThere, "太新，可能是正在上传中的，宽限期内不清")
}

func TestStartReconciler_NoFiles_NoOp(t *testing.T) {
	d := newFullTestUsecase()
	d.uc.StartReconciler(context.Background())
	reconcile := d.sched.registered["orphan-files"]

	assert.NoError(t, reconcile(context.Background()))
}
