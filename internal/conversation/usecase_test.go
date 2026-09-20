package conversation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/ctxmgr"
	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 假实现。整套测试不连数据库、不打真实网络请求。
// ════════════════════════════════════════════════════════════════

var _ Repo = (*fakeRepo)(nil)

type fakeRepo struct {
	mu sync.Mutex

	conversations map[uuid.UUID]*Conversation
	messages      map[uuid.UUID][]*Message // convID -> 按插入顺序
	events        map[uuid.UUID][]Event
	nextEventID   map[uuid.UUID]int64
	summaries     map[uuid.UUID]*Summary

	failOn string
	err    error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		conversations: map[uuid.UUID]*Conversation{},
		messages:      map[uuid.UUID][]*Message{},
		events:        map[uuid.UUID][]Event{},
		nextEventID:   map[uuid.UUID]int64{},
		summaries:     map[uuid.UUID]*Summary{},
	}
}

func (f *fakeRepo) CreateConversation(ctx context.Context, q platform.Querier, c *Conversation) error {
	if f.failOn == "CreateConversation" {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conversations[c.ID] = c
	return nil
}

func (f *fakeRepo) GetConversation(ctx context.Context, q platform.Querier, id uuid.UUID) (*Conversation, error) {
	if f.failOn == "GetConversation" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.conversations[id]
	if !ok {
		return nil, platform.ErrNotFound
	}
	return c, nil
}

func (f *fakeRepo) NextSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	if f.failOn == "NextSequenceNo" {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var max int64
	for _, m := range f.messages[convID] {
		if m.SequenceNo > max {
			max = m.SequenceNo
		}
	}
	return max + 1, nil
}

func (f *fakeRepo) AppendMessage(ctx context.Context, q platform.Querier, m *Message) error {
	if f.failOn == "AppendMessage" {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// 复刻数据库唯一约束 (conversation_id, sequence_no) 的行为——
	// 假实现如果对这条不做任何检查，测试就测不出"顺序被破坏"这类 bug。
	for _, existing := range f.messages[m.ConversationID] {
		if existing.SequenceNo == m.SequenceNo {
			return platform.ErrDuplicateKey
		}
	}
	f.messages[m.ConversationID] = append(f.messages[m.ConversationID], m)
	return nil
}

func (f *fakeRepo) RecentMessages(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error) {
	if f.failOn == "RecentMessages" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var filtered []*Message
	for _, m := range f.messages[convID] {
		if m.SequenceNo > afterSequenceNo {
			filtered = append(filtered, m)
		}
	}
	// 倒序，最多 limit 条——复刻真实 SQL 的 ORDER BY sequence_no DESC LIMIT n。
	out := make([]*Message, 0, len(filtered))
	for i := len(filtered) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, filtered[i])
	}
	return out, nil
}

func (f *fakeRepo) MessagesAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterSequenceNo int64, limit int) ([]*Message, error) {
	if f.failOn == "MessagesAfter" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Message
	for _, m := range f.messages[convID] {
		if m.SequenceNo > afterSequenceNo {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeRepo) LatestSequenceNo(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	if f.failOn == "LatestSequenceNo" {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var max int64
	for _, m := range f.messages[convID] {
		if m.SequenceNo > max {
			max = m.SequenceNo
		}
	}
	return max, nil
}

func (f *fakeRepo) ListConversationIDs(ctx context.Context, q platform.Querier) ([]uuid.UUID, error) {
	if f.failOn == "ListConversationIDs" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uuid.UUID, 0, len(f.conversations))
	for id := range f.conversations {
		out = append(out, id)
	}
	return out, nil
}

func (f *fakeRepo) GetSummary(ctx context.Context, q platform.Querier, convID uuid.UUID) (*Summary, error) {
	if f.failOn == "GetSummary" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.summaries[convID]
	if !ok {
		return nil, platform.ErrNotFound
	}
	return s, nil
}

func (f *fakeRepo) UpsertSummary(ctx context.Context, q platform.Querier, s *Summary) error {
	if f.failOn == "UpsertSummary" {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[s.ConversationID] = s
	return nil
}

func (f *fakeRepo) ListMessages(ctx context.Context, q platform.Querier, convID uuid.UUID) ([]*Message, error) {
	if f.failOn == "ListMessages" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Message{}, f.messages[convID]...), nil
}

func (f *fakeRepo) UpdateMessageContent(ctx context.Context, q platform.Querier, id uuid.UUID, content string, status MessageStatus) error {
	if f.failOn == "UpdateMessageContent" {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, msgs := range f.messages {
		for _, m := range msgs {
			if m.ID == id {
				m.Content = content
				m.Status = status
				return nil
			}
		}
	}
	return platform.ErrNotFound
}

func (f *fakeRepo) NextEventID(ctx context.Context, q platform.Querier, convID uuid.UUID) (int64, error) {
	if f.failOn == "NextEventID" {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextEventID[convID]++
	return f.nextEventID[convID], nil
}

func (f *fakeRepo) AppendEvent(ctx context.Context, q platform.Querier, convID uuid.UUID, ev Event) error {
	if f.failOn == "AppendEvent" {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[convID] = append(f.events[convID], ev)
	return nil
}

func (f *fakeRepo) EventsAfter(ctx context.Context, q platform.Querier, convID uuid.UUID, afterEventID int64) ([]Event, error) {
	if f.failOn == "EventsAfter" {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Event
	for _, ev := range f.events[convID] {
		if ev.ID > afterEventID {
			out = append(out, ev)
		}
	}
	return out, nil
}

// fakeLockedWriter 不真的用 advisory lock——用一个 Go mutex 模拟"同一时刻
// 只有一个调用者在临界区里"这条性质，够测试 Send 的调用顺序和并发安全性，
// 不需要真实 Postgres。
type fakeLockedWriter struct {
	mu sync.Mutex
}

func (w *fakeLockedWriter) WithConversationLock(ctx context.Context, convID uuid.UUID, fn func(q platform.Querier) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fn(nil)
}

// fakeStream 模拟 llm.Stream：从一个预设的 chunk 列表里依次吐出，
// 吐完返回 io.EOF。
type fakeStream struct {
	chunks []string
	idx    int
	closed bool
	// recvDelay 让测试能控制"两个 chunk 之间隔多久"，用来测试
	// checkpointInterval 触发的时机——不真的 sleep 真实的 500ms，
	// 而是配合一个可控的时钟（见下面 TestSend_CheckspointsOnInterval）。
}

func (s *fakeStream) Recv() (*llm.Message, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return &llm.Message{Role: domain.RoleAssistant, Content: c}, nil
}

func (s *fakeStream) Close() error {
	s.closed = true
	return nil
}

var _ llm.ChatModel = (*fakeChatModel)(nil)

type fakeChatModel struct {
	streamChunks []string
	streamErr    error
	lastMessages []llm.Message // 记录最后一次 Stream 调用收到的完整消息列表

	// generateResp/generateErr 供 memory.go 的 MaintainSummary/
	// ExtractPreferences 使用——那两个方法调用 Generate,不是 Stream
	// （它们是后台批处理,不是流式响应给客户端）。generateCalls 记录
	// 每一次调用收到的完整消息列表,供测试断言 prompt 内容。
	generateResp  *llm.Message
	generateErr   error
	generateCalls [][]llm.Message
}

func (m *fakeChatModel) Generate(ctx context.Context, msgs []llm.Message, opts ...llm.CallOption) (*llm.Message, error) {
	m.generateCalls = append(m.generateCalls, msgs)
	if m.generateErr != nil {
		return nil, m.generateErr
	}
	if m.generateResp != nil {
		return m.generateResp, nil
	}
	return &llm.Message{Role: domain.RoleAssistant, Content: "generated"}, nil
}

func (m *fakeChatModel) Stream(ctx context.Context, msgs []llm.Message, opts ...llm.CallOption) (llm.Stream, error) {
	m.lastMessages = msgs
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return &fakeStream{chunks: m.streamChunks}, nil
}

var _ llm.Registry = (*fakeRegistry)(nil)

type fakeRegistry struct {
	chatModel      *fakeChatModel
	chatModelID    string
	activeModelErr error
	chatErr        error
	tokenizerErr   error
	embedder       llm.Embedder
	embedderErr    error
}

func (f *fakeRegistry) Chat(ctx context.Context, modelID string) (llm.ChatModel, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.chatModel, nil
}

func (f *fakeRegistry) Embedder(ctx context.Context, modelID string) (llm.Embedder, error) {
	if f.embedderErr != nil {
		return nil, f.embedderErr
	}
	if f.embedder != nil {
		return f.embedder, nil
	}
	return nil, errors.New("fakeRegistry.Embedder: not implemented, this test should not reach here")
}

func (f *fakeRegistry) Tokenizer(ctx context.Context, modelID string) (llm.Tokenizer, error) {
	if f.tokenizerErr != nil {
		return nil, f.tokenizerErr
	}
	return fakeTokenizer{}, nil
}

func (f *fakeRegistry) ActiveModelID(ctx context.Context, kind llm.Kind) (string, error) {
	if f.activeModelErr != nil {
		return "", f.activeModelErr
	}
	return f.chatModelID, nil
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

type fakeTokenizer struct{}

func (fakeTokenizer) Count(s string) int { return len(s) }

var _ llm.ConfigRepo = (*fakeConfigRepo)(nil)

type fakeConfigRepo struct {
	model  *llm.Model
	getErr error
	// models 供 ListModels 返回——只有涉及 activeEmbeddingModel 解析路径
	// 的测试（长期记忆相关）才需要设置它，其余测试留空,ListModels 报错
	// 也不影响它们（那些路径从不调 ListModels）。
	models  []*llm.Model
	listErr error
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
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.models != nil {
		return f.models, nil
	}
	return nil, errors.New("not implemented")
}
func (f *fakeConfigRepo) GetModel(ctx context.Context, q platform.Querier, id uuid.UUID) (*llm.Model, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.model, nil
}

var _ ctxmgr.Manager = (*fakeCtxManager)(nil)

// fakeCtxManager 不做真的预算裁剪——那部分是 ctxmgr 包自己的测试范围
// （internal/ctxmgr/usecase_test.go）。这里只需要验证 conversation.Usecase
// 把正确的原材料传给了 Build，以及正确处理了 Build 的返回值/错误。
type fakeCtxManager struct {
	result   *ctxmgr.FinalContext
	err      error
	lastReq  ctxmgr.Request
	buildErr error
}

func (f *fakeCtxManager) Build(ctx context.Context, req ctxmgr.Request) (*ctxmgr.FinalContext, error) {
	f.lastReq = req
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	if f.result != nil {
		return f.result, nil
	}
	return &ctxmgr.FinalContext{
		Items: []ctxmgr.Item{
			{Source: domain.SourceSystem, Content: req.SystemPrompt},
			{Source: domain.SourceUser, Content: req.UserInput},
		},
	}, nil
}

var _ ChunkSearcher = (*fakeSearcher)(nil)

type fakeSearcher struct {
	result  []domain.Chunk
	err     error
	called  bool
	lastReq domain.SearchRequest
}

func (f *fakeSearcher) Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error) {
	f.called = true
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

var _ MemoryRepo = (*fakeMemoryRepo)(nil)

// fakeMemoryRepo 不做真的相关度排序——insert 顺序即返回顺序,
// 测试如果需要验证"按相关度排好"这件事,应该在 ctxmgr 那边测
// （它才是真正依赖这个排序的消费方,见 buildMemoryItems 的注释）。
type fakeMemoryRepo struct {
	mu        sync.Mutex
	inserted  []domain.Memory
	searchErr error
	result    []domain.Memory
}

func (f *fakeMemoryRepo) Insert(ctx context.Context, q platform.Querier, m *domain.Memory, vec []float32, embeddingModel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, *m)
	return nil
}

func (f *fakeMemoryRepo) SearchByRelevance(ctx context.Context, q platform.Querier, req domain.MemoryRequest, vec []float32, embeddingModel string) ([]domain.Memory, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.result, nil
}

// fakeEmbedder 返回确定性的向量，不需要真的算语义——和
// retrieval.usecase_test.go 的同名类型同样的取舍，各自一份独立副本
// （两个包不允许互相依赖对方的测试代码）。
type fakeEmbedder struct {
	dim       int
	failEmbed bool
}

func (e *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e.failEmbed {
		return nil, errors.New("fake embed failure")
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{float32(i)}
	}
	return out, nil
}

func (e *fakeEmbedder) Dim() int { return e.dim }

// fakeSink 收集 Emit 调用，模拟一个不会主动断开的客户端连接
// （Done() 返回一个永远不关闭的 channel，除非测试显式调用 closeConn）。
type fakeSink struct {
	mu       sync.Mutex
	emitted  []Event
	done     chan struct{}
	failEmit bool
}

func newFakeSink() *fakeSink {
	return &fakeSink{done: make(chan struct{})}
}

func (s *fakeSink) Emit(ev Event) error {
	if s.failEmit {
		return errors.New("fake emit failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitted = append(s.emitted, ev)
	return nil
}

func (s *fakeSink) Flush() error { return nil }

func (s *fakeSink) Done() <-chan struct{} { return s.done }

func (s *fakeSink) closeConn() { close(s.done) }

func (s *fakeSink) eventsByType(t string) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, ev := range s.emitted {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

// ════════════════════════════════════════════════════════════════
// 测试用的装配辅助
// ════════════════════════════════════════════════════════════════

type testDeps struct {
	repo     *fakeRepo
	writer   *fakeLockedWriter
	registry *fakeRegistry
	llmRepo  *fakeConfigRepo
	ctxm     *fakeCtxManager
	search   *fakeSearcher
	memRepo  *fakeMemoryRepo
	uc       *Usecase
}

func newTestUsecase() *testDeps {
	modelID := uuid.New()
	d := &testDeps{
		repo:   newFakeRepo(),
		writer: &fakeLockedWriter{},
		registry: &fakeRegistry{
			chatModel:   &fakeChatModel{streamChunks: []string{"你好"}},
			chatModelID: modelID.String(),
		},
		llmRepo: &fakeConfigRepo{
			model: &llm.Model{ID: modelID, Kind: llm.KindChat, ContextWindow: 8000, MaxOutputTokens: 1000},
		},
		ctxm:    &fakeCtxManager{},
		search:  &fakeSearcher{},
		memRepo: &fakeMemoryRepo{},
	}
	d.uc = NewUsecase(d.repo, d.writer, d.registry, d.llmRepo, d.ctxm, d.search, d.memRepo, nil)
	return d
}

func (d *testDeps) createConversation(t *testing.T, kbID *uuid.UUID) *Conversation {
	t.Helper()
	c, err := d.uc.CreateConversation(context.Background(), "测试会话", kbID)
	require.NoError(t, err)
	return c
}

// ════════════════════════════════════════════════════════════════
// CreateConversation
// ════════════════════════════════════════════════════════════════

func TestCreateConversation_Success(t *testing.T) {
	d := newTestUsecase()

	c, err := d.uc.CreateConversation(context.Background(), "我的会话", nil)

	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, c.ID)
	assert.Equal(t, "我的会话", c.Title)
	assert.Nil(t, c.KnowledgeBaseID)
}

func TestCreateConversation_WithKnowledgeBase(t *testing.T) {
	d := newTestUsecase()
	kbID := uuid.New()

	c, err := d.uc.CreateConversation(context.Background(), "带知识库的会话", &kbID)

	require.NoError(t, err)
	require.NotNil(t, c.KnowledgeBaseID)
	assert.Equal(t, kbID, *c.KnowledgeBaseID)
}

func TestCreateConversation_TitleTooLong(t *testing.T) {
	d := newTestUsecase()
	longTitle := strings.Repeat("字", maxTitleLen+1)

	_, err := d.uc.CreateConversation(context.Background(), longTitle, nil)

	assert.ErrorIs(t, err, platform.ErrInvalid)
}

func TestCreateConversation_EmptyTitleIsAllowed(t *testing.T) {
	d := newTestUsecase()

	c, err := d.uc.CreateConversation(context.Background(), "   ", nil)

	require.NoError(t, err)
	assert.Empty(t, c.Title)
}

// ════════════════════════════════════════════════════════════════
// Send —— 正常路径
// ════════════════════════════════════════════════════════════════

func TestSend_Success_WritesMessagesInOrder(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.NoError(t, err)

	msgs := d.repo.messages[conv.ID]
	require.Len(t, msgs, 2, "应该有用户消息 + assistant 消息两条")
	assert.Equal(t, domain.RoleUser, msgs[0].Role)
	assert.Equal(t, "你好", msgs[0].Content)
	assert.Equal(t, int64(1), msgs[0].SequenceNo)
	assert.Equal(t, MsgCompleted, msgs[0].Status)

	assert.Equal(t, domain.RoleAssistant, msgs[1].Role)
	assert.Equal(t, int64(2), msgs[1].SequenceNo)
	assert.Equal(t, MsgCompleted, msgs[1].Status, "流结束后应该被置为 completed")
	assert.Equal(t, "你好", msgs[1].Content, "assistant 消息内容应该是 stream 吐出的全部增量拼起来")
}

func TestSend_EmptyText_Rejected(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "   ", sink)

	assert.ErrorIs(t, err, platform.ErrInvalid)
	assert.Empty(t, d.repo.messages[conv.ID], "校验不过时不该写任何消息")
}

func TestSend_EmitsTokenEventsForEachChunk(t *testing.T) {
	d := newTestUsecase()
	d.registry.chatModel.streamChunks = []string{"你", "好", "！"}
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "hi", sink)

	require.NoError(t, err)
	tokenEvents := sink.eventsByType("token")
	require.Len(t, tokenEvents, 3)

	doneEvents := sink.eventsByType("done")
	require.Len(t, doneEvents, 1, "流结束必须发一个 done 事件")

	// event_id 必须单调递增,且 done 是最后一个。
	assert.True(t, doneEvents[0].ID > tokenEvents[len(tokenEvents)-1].ID)
}

// 事件必须同时被持久化——断线续传（EventsAfter）依赖这一点。
func TestSend_EventsArePersisted(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "hi", sink)
	require.NoError(t, err)

	persisted, err := d.uc.EventsAfter(context.Background(), conv.ID, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, persisted)
	assert.Equal(t, len(sink.emitted), len(persisted), "推给客户端的和存进数据库的应该是同一批事件")
}

// ════════════════════════════════════════════════════════════════
// Send —— RAG（知识库关联）
// ════════════════════════════════════════════════════════════════

func TestSend_WithKnowledgeBase_SearchesAndEmitsCitations(t *testing.T) {
	d := newTestUsecase()
	kbID := uuid.New()
	chunkID := uuid.New()
	docID := uuid.New()
	d.search.result = []domain.Chunk{
		{ID: chunkID, DocumentID: docID, Content: "命中内容", Filename: "a.md", Score: 0.9},
	}
	d.ctxm.result = &ctxmgr.FinalContext{
		Items:     []ctxmgr.Item{{Source: domain.SourceUser, Content: "你好"}},
		Citations: []domain.Citation{{ChunkID: chunkID, DocumentID: docID, Filename: "a.md", Snippet: "命中内容", Score: 0.9}},
	}

	conv := d.createConversation(t, &kbID)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.NoError(t, err)
	assert.True(t, d.search.called)
	assert.Equal(t, kbID, d.search.lastReq.KnowledgeBaseID, "必须用会话关联的知识库 id 去检索")

	citationEvents := sink.eventsByType("citation")
	require.Len(t, citationEvents, 1)
	assert.Contains(t, string(citationEvents[0].Payload), "a.md")
}

func TestSend_WithoutKnowledgeBase_SkipsSearch(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil) // 没有关联知识库
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.NoError(t, err)
	assert.False(t, d.search.called, "没有关联知识库时不该调用检索")
}

// 检索失败不该让整条聊天失败——RAG 是增强，不是前提。
func TestSend_SearchFails_ChatStillSucceeds(t *testing.T) {
	d := newTestUsecase()
	kbID := uuid.New()
	d.search.err = errors.New("embedding model not configured")
	conv := d.createConversation(t, &kbID)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.NoError(t, err, "检索失败不该导致整个 Send 失败")
	msgs := d.repo.messages[conv.ID]
	require.Len(t, msgs, 2)
	assert.Equal(t, MsgCompleted, msgs[1].Status)
}

// ════════════════════════════════════════════════════════════════
// Send —— 失败路径：每一步失败都必须把 assistant 消息标记 failed
// ════════════════════════════════════════════════════════════════

func TestSend_ActiveModelResolutionFails_MarksFailed(t *testing.T) {
	d := newTestUsecase()
	d.registry.activeModelErr = errors.New("no chat model configured")
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.Error(t, err)
	msgs := d.repo.messages[conv.ID]
	require.Len(t, msgs, 2, "用户消息和占位 assistant 消息应该已经写入")
	assert.Equal(t, MsgFailed, msgs[1].Status)
}

func TestSend_ContextBuildOverflow_MarksFailed(t *testing.T) {
	d := newTestUsecase()
	d.ctxm.buildErr = ctxmgr.ErrOverflow
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	assert.ErrorIs(t, err, ctxmgr.ErrOverflow)
	msgs := d.repo.messages[conv.ID]
	assert.Equal(t, MsgFailed, msgs[1].Status)
}

func TestSend_ChatModelStreamFails_MarksFailed(t *testing.T) {
	d := newTestUsecase()
	// 【必须复刻真实 llm.ChatModel 的包装形状】internal/llm/eino.go 的
	// einoChatModel.Stream 失败时一律包成 `%w: stream: %v"` 用
	// platform.ErrUpstream——如果这里的 fake 只返回一个裸错误，
	// 测的就不是"上游出错时 error 事件的 type 对不对"，而是测了一个
	// 生产环境不会出现的错误形状。这正是 helperDoc 记录过的教训：
	// fake 和真实现的包装层数不一样时，测试全绿也说明不了什么。
	d.registry.chatModel.streamErr = fmt.Errorf("%w: stream: upstream 500", platform.ErrUpstream)
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrUpstream)
	msgs := d.repo.messages[conv.ID]
	assert.Equal(t, MsgFailed, msgs[1].Status)
}

// ════════════════════════════════════════════════════════════════
// error 事件——docs/sse-protocol.md 定义的失败通知路径
// ════════════════════════════════════════════════════════════════

// 【这条防的是一个真实踩过的坑】failMessage 只把消息标成 failed，
// 从不主动告诉客户端"出错了"——如果 Send 在这里就直接返回，客户端的
// SSE 连接会挂在那里，既不收到 token、也不收到 error/done，唯一线索
// 是 HTTP 请求本身的连接被服务端关闭，前端无法区分"正常结束"和
// "出错了"。docs/sse-protocol.md 定义的 error 事件就是为了避免这种情况。
func TestSend_AnyFailure_EmitsErrorEvent(t *testing.T) {
	d := newTestUsecase()
	d.registry.chatModel.streamErr = fmt.Errorf("%w: stream: upstream 500", platform.ErrUpstream)
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.Error(t, err)
	errorEvents := sink.eventsByType("error")
	require.Len(t, errorEvents, 1, "失败必须推一个 error 事件，不能让客户端的连接悄悄挂起")
	assert.Contains(t, string(errorEvents[0].Payload), "upstream_llm_error")
}

// 连"会话不存在"这种在写任何消息之前就失败的路径，也必须推 error 事件——
// 这个错误发生在 lockAndWriteInitialMessages 之前，是最容易被漏掉的分支。
func TestSend_ConversationNotFound_EmitsErrorEvent(t *testing.T) {
	d := newTestUsecase()
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), uuid.New(), "你好", sink)

	require.Error(t, err)
	errorEvents := sink.eventsByType("error")
	require.Len(t, errorEvents, 1)
	assert.Contains(t, string(errorEvents[0].Payload), "not_found")
}

func TestSend_Success_DoesNotEmitErrorEvent(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	sink := newFakeSink()

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.NoError(t, err)
	assert.Empty(t, sink.eventsByType("error"))
}

func TestEventErrorType_MapsKnownSentinels(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{platform.ErrInvalid, "invalid_argument"},
		{platform.ErrNotFound, "not_found"},
		{platform.ErrUpstream, "upstream_llm_error"},
		{ctxmgr.ErrOverflow, "context_overflow"},
		{errors.New("something else"), "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, eventErrorType(tt.err))
		})
	}
}

// 客户端断开连接：Send 必须停止消费流，返回错误，但不应该是 panic
// 或者悬挂（goroutine 泄漏）。
func TestSend_ClientDisconnects_StopsGracefully(t *testing.T) {
	d := newTestUsecase()
	// 一个很长的流,如果不提前停止会一直吐 chunk。
	chunks := make([]string, 1000)
	for i := range chunks {
		chunks[i] = "x"
	}
	d.registry.chatModel.streamChunks = chunks

	conv := d.createConversation(t, nil)
	sink := newFakeSink()
	sink.closeConn() // 提前关闭,模拟"还没开始收就已经断开"

	err := d.uc.Send(context.Background(), conv.ID, "你好", sink)

	require.Error(t, err)
	msgs := d.repo.messages[conv.ID]
	assert.Equal(t, MsgFailed, msgs[1].Status)
}

// ════════════════════════════════════════════════════════════════
// itemsToLLMMessages —— 纯函数，七类来源怎么摊平成对话历史
// ════════════════════════════════════════════════════════════════

func TestItemsToLLMMessages_ClassifiesBySource(t *testing.T) {
	items := []ctxmgr.Item{
		{Source: domain.SourceSystem, Content: "系统提示"},
		{Source: domain.SourceSummary, Content: "摘要内容"},
		{Source: domain.SourceRecent, Content: "历史消息"},
		{Source: domain.SourceChunk, Content: "检索片段"},
		{Source: domain.SourceMemory, Content: "用户偏好"},
		{Source: domain.SourceUser, Content: "当前提问"},
	}

	msgs := itemsToLLMMessages(items)

	require.GreaterOrEqual(t, len(msgs), 3)
	assert.Equal(t, domain.RoleSystem, msgs[0].Role)
	assert.Contains(t, msgs[0].Content, "系统提示")
	assert.Contains(t, msgs[0].Content, "摘要内容")
	assert.Contains(t, msgs[0].Content, "检索片段")
	assert.Contains(t, msgs[0].Content, "用户偏好")

	last := msgs[len(msgs)-1]
	assert.Equal(t, domain.RoleUser, last.Role)
	assert.Equal(t, "当前提问", last.Content, "最后一条必须是用户当前的提问，不能和历史消息混在一起")
}

// ════════════════════════════════════════════════════════════════
// ListMessages / EventsAfter 透传
// ════════════════════════════════════════════════════════════════

func TestListMessages_PropagatesRepoError(t *testing.T) {
	d := newTestUsecase()
	d.repo.failOn, d.repo.err = "ListMessages", platform.ErrUpstream

	_, err := d.uc.ListMessages(context.Background(), uuid.New())

	assert.ErrorIs(t, err, platform.ErrUpstream)
}

func TestEventsAfter_ReturnsOnlyNewerEvents(t *testing.T) {
	d := newTestUsecase()
	conv := d.createConversation(t, nil)
	sink := newFakeSink()
	require.NoError(t, d.uc.Send(context.Background(), conv.ID, "你好", sink))

	all, err := d.uc.EventsAfter(context.Background(), conv.ID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, all)

	// 从中间某个 id 之后续传,应该只拿到剩下的一部分。
	midpoint := all[0].ID
	partial, err := d.uc.EventsAfter(context.Background(), conv.ID, midpoint)
	require.NoError(t, err)
	assert.Len(t, partial, len(all)-1)
}
