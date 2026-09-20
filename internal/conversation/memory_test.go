package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// newMemoryTestUsecase 和 newTestUsecase 同样的装配,但补上
// activeEmbeddingModel 解析路径需要的东西（fakeConfigRepo.models +
// fakeRegistry.embedder）——普通聊天测试从不触发这条路径,只有这个
// 文件里的测试需要。
func newMemoryTestUsecase() *testDeps {
	d := newTestUsecase()
	embeddingModelID := uuid.New()
	d.llmRepo.models = []*llm.Model{
		{ID: embeddingModelID, Kind: llm.KindEmbedding, ModelID: "fake-embed-model"},
	}
	d.registry.embedder = &fakeEmbedder{dim: 1}
	return d
}

// ════════════════════════════════════════════════════════════════
// RetrieveSummary / MaintainSummary
// ════════════════════════════════════════════════════════════════

func TestRetrieveSummary_NoSummaryYet_ReturnsEmptyNotError(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()

	summary, coveredUntil, err := d.uc.RetrieveSummary(context.Background(), convID)

	require.NoError(t, err)
	assert.Equal(t, "", summary)
	assert.Equal(t, int64(0), coveredUntil)
}

func TestRetrieveSummary_ExistingSummary_ReturnsItsFields(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()
	d.repo.summaries[convID] = &Summary{ConversationID: convID, Summary: "此前聊了 Go 并发", CoveredUntilSequenceNo: 12}

	summary, coveredUntil, err := d.uc.RetrieveSummary(context.Background(), convID)

	require.NoError(t, err)
	assert.Equal(t, "此前聊了 Go 并发", summary)
	assert.Equal(t, int64(12), coveredUntil)
}

// 新消息数量没到 summaryTriggerMessages 门槛时,不该触发任何压缩——
// 不该调用 chatModel.Generate,也不该写 UpsertSummary。
func TestMaintainSummary_BelowThreshold_NoOp(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	for i := int64(1); i <= summaryTriggerMessages-1; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)

	require.NoError(t, err)
	_, err = d.repo.GetSummary(context.Background(), nil, convID)
	assert.ErrorIs(t, err, platform.ErrNotFound, "没到门槛不该产生任何摘要")
}

// 达到门槛后,MaintainSummary 必须：调用 chatModel 生成新摘要、
// 把 covered_until_sequence_no 前进到这一批最后一条消息的 sequence_no。
func TestMaintainSummary_AboveThreshold_GeneratesAndAdvancesWatermark(t *testing.T) {
	d := newTestUsecase()
	d.registry.chatModel = &fakeChatModel{}
	d.registry.chatModel.streamChunks = nil // 这条路径用 Generate,不用 Stream
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	for i := int64(1); i <= summaryTriggerMessages; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)
	require.NoError(t, err)

	s, err := d.repo.GetSummary(context.Background(), nil, convID)
	require.NoError(t, err)
	assert.Equal(t, int64(summaryTriggerMessages), s.CoveredUntilSequenceNo)
}

// 已经存在的摘要必须原样传给 Generate（作为"已有摘要"那部分的上下文），
// 不能在重新压缩时被丢弃——否则每一轮压缩都会丢掉更早的历史。
func TestMaintainSummary_IncludesPriorSummaryInPrompt(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	d.repo.summaries[convID] = &Summary{ConversationID: convID, Summary: "旧摘要内容", CoveredUntilSequenceNo: 5}
	for i := int64(6); i <= 5+summaryTriggerMessages; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "新消息", SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)
	require.NoError(t, err)

	require.Len(t, d.registry.chatModel.generateCalls, 1)
	var sawPriorSummary bool
	for _, msg := range d.registry.chatModel.generateCalls[0] {
		if strings.Contains(msg.Content, "旧摘要内容") {
			sawPriorSummary = true
		}
	}
	assert.True(t, sawPriorSummary, "重新压缩时必须把已有摘要传给模型,不能丢掉")
}

func TestMaintainSummary_ChatModelFails_ErrorPropagates(t *testing.T) {
	d := newTestUsecase()
	d.registry.chatModel.generateErr = errors.New("upstream boom")
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	for i := int64(1); i <= summaryTriggerMessages; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream boom")
}

// ════════════════════════════════════════════════════════════════
// RetrieveMemory / ExtractPreferences
// ════════════════════════════════════════════════════════════════

func TestRetrieveMemory_ReturnsRepoResult(t *testing.T) {
	d := newMemoryTestUsecase()
	want := []domain.Memory{{ID: uuid.New(), Content: "偏好简洁的回答"}}
	d.memRepo.result = want

	got, err := d.uc.RetrieveMemory(context.Background(), domain.MemoryRequest{Text: "随便问点什么"})

	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestRetrieveMemory_NoEmbeddingModelConfigured_ReturnsError(t *testing.T) {
	d := newTestUsecase() // 故意不用 newMemoryTestUsecase：llmRepo.models 留空
	d.llmRepo.listErr = nil

	_, err := d.uc.RetrieveMemory(context.Background(), domain.MemoryRequest{Text: "x"})

	require.Error(t, err)
}

// 抽取到内容时必须：调用一次 embedder,把结果逐条 Insert 进 memRepo,
// 且每条记忆的 scope 是 defaultMemoryScope。
func TestExtractPreferences_FindsContent_InsertsMemories(t *testing.T) {
	d := newMemoryTestUsecase()
	d.registry.chatModel.generateResp = &llm.Message{Content: "用户偏好简洁的回答\n用户是 Go 开发者"}

	convID := uuid.New()
	recent := []llm.Message{{Role: domain.RoleUser, Content: "请以后回答简洁一点，我是写 Go 的"}}

	got, err := d.uc.ExtractPreferences(context.Background(), convID, recent)

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "用户偏好简洁的回答", got[0].Content)
	assert.Equal(t, "用户是 Go 开发者", got[1].Content)
	for _, m := range got {
		assert.Equal(t, defaultMemoryScope, m.Scope)
	}
	assert.Len(t, d.memRepo.inserted, 2, "两条抽取结果都应该被写入 memRepo")
}

// 模型判断"这批消息没有值得记住的内容"时输出空字符串——ExtractPreferences
// 必须能识别这种情况,返回空切片,不写入任何空内容的记忆。
func TestExtractPreferences_NothingWorthRemembering_InsertsNothing(t *testing.T) {
	d := newMemoryTestUsecase()
	d.registry.chatModel.generateResp = &llm.Message{Content: ""}

	got, err := d.uc.ExtractPreferences(context.Background(), uuid.New(), []llm.Message{{Content: "今天天气怎么样"}})

	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, d.memRepo.inserted)
}

func TestExtractPreferences_EmptyInput_SkipsLLMCall(t *testing.T) {
	d := newMemoryTestUsecase()

	got, err := d.uc.ExtractPreferences(context.Background(), uuid.New(), nil)

	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Empty(t, d.registry.chatModel.generateCalls, "没有输入消息时不该调用模型")
}
