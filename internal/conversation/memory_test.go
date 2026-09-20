package conversation

import (
	"context"
	"errors"
	"fmt"
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
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", Status: MsgCompleted, SequenceNo: i})
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
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", Status: MsgCompleted, SequenceNo: i})
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
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "新消息", Status: MsgCompleted, SequenceNo: i})
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
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", Status: MsgCompleted, SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream boom")
}

// 最新一条还是生成中的占位行（空内容、status='streaming'）时,这一轮
// 不该摘要任何东西：门控数的是已定稿的消息,占位行不该把它顶过去。
// 修复前 latest 会把占位行算进去、批次也把它当普通消息压掉,水位线因此
// 跨过它——生成结束后写进去的正文永远进不了后续 prompt。
func TestMaintainSummary_NewestRowStillStreaming_NoOp(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	for i := int64(1); i <= summaryTriggerMessages-1; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "msg", Status: MsgCompleted, SequenceNo: i})
	}
	d.repo.messages[convID] = append(d.repo.messages[convID],
		&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleAssistant,
			Content: "", Status: MsgStreaming, SequenceNo: summaryTriggerMessages})

	err := d.uc.MaintainSummary(context.Background(), convID)

	require.NoError(t, err)
	_, err = d.repo.GetSummary(context.Background(), nil, convID)
	assert.ErrorIs(t, err, platform.ErrNotFound, "生成中的占位行不该触发摘要")
	assert.Empty(t, d.registry.chatModel.generateCalls, "没到门槛不该调用模型")
}

// 批次里夹着一条 streaming 行（客户端断开时留下的、正文已经冻结的那条）时,
// 水位线只能推进到它前面那条定稿消息：跨过去就等于把这条行从摘要和
// RecentMessages 里同时排除掉。
func TestMaintainSummary_StreamingRowInsideBatch_WatermarkStopsBeforeIt(t *testing.T) {
	d := newTestUsecase()
	convID := uuid.New()
	d.repo.conversations[convID] = &Conversation{ID: convID}
	for i := int64(1); i <= summaryTriggerMessages-1; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "已定稿的历史", Status: MsgCompleted, SequenceNo: i})
	}
	// 第 20 条是一次被中断的生成：正文停在半截，status 永远停在 streaming
	// （客户端断开时不会有人补写完成态）。
	orphan := &Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleAssistant,
		Content: "半截答案", Status: MsgStreaming, SequenceNo: summaryTriggerMessages}
	d.repo.messages[convID] = append(d.repo.messages[convID], orphan)
	for i := int64(summaryTriggerMessages + 1); i <= summaryTriggerMessages*2-1; i++ {
		d.repo.messages[convID] = append(d.repo.messages[convID],
			&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "更晚的历史", Status: MsgCompleted, SequenceNo: i})
	}

	err := d.uc.MaintainSummary(context.Background(), convID)
	require.NoError(t, err)

	s, err := d.repo.GetSummary(context.Background(), nil, convID)
	require.NoError(t, err)
	assert.Equal(t, int64(summaryTriggerMessages-1), s.CoveredUntilSequenceNo,
		"水位线只能推进到批次里最后一条已定稿的消息")

	// 被截掉的那条留在水位线之后,下一轮还会被 RecentMessages 取到——
	// 这才是"这一轮没吸收"和"永久从 prompt 里消失"的区别。
	recent, err := d.repo.RecentMessages(context.Background(), nil, convID, s.CoveredUntilSequenceNo, 0, 100)
	require.NoError(t, err)
	var sawOrphan bool
	for _, m := range recent {
		if m.SequenceNo == summaryTriggerMessages {
			sawOrphan = true
			assert.Equal(t, "半截答案", m.Content)
		}
	}
	assert.True(t, sawOrphan, "水位线之后的 streaming 行必须还能被取回")
}

// 模型返回空（或纯空白）completion 时必须当作失败：不能写库、更不能推进
// 水位线——summary 列是 text NOT NULL,空串写得进去,写进去就是旧摘要被
// 覆盖 + 那段历史从 prompt 里消失的双重损失,而且不可恢复。
func TestMaintainSummary_EmptyCompletion_KeepsPriorSummaryAndWatermark(t *testing.T) {
	for _, content := range []string{"", "  \n\t "} {
		t.Run(fmt.Sprintf("content=%q", content), func(t *testing.T) {
			d := newTestUsecase()
			d.registry.chatModel.generateResp = &llm.Message{Content: content}
			convID := uuid.New()
			d.repo.conversations[convID] = &Conversation{ID: convID}
			d.repo.summaries[convID] = &Summary{ConversationID: convID, Summary: "旧摘要内容", CoveredUntilSequenceNo: 5}
			for i := int64(6); i <= 5+summaryTriggerMessages; i++ {
				d.repo.messages[convID] = append(d.repo.messages[convID],
					&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser, Content: "新消息", Status: MsgCompleted, SequenceNo: i})
			}

			err := d.uc.MaintainSummary(context.Background(), convID)

			require.Error(t, err, "空 completion 必须让这一轮失败,由下一个 tick 重试")
			s, getErr := d.repo.GetSummary(context.Background(), nil, convID)
			require.NoError(t, getErr)
			assert.Equal(t, "旧摘要内容", s.Summary, "旧摘要不能被空串覆盖")
			assert.Equal(t, int64(5), s.CoveredUntilSequenceNo, "水位线不能推进")
		})
	}
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

// ════════════════════════════════════════════════════════════════
// 整轮扫描被 job 超时截断时必须上报失败
// ════════════════════════════════════════════════════════════════

// seedSummarizableConversations 造 n 个越过摘要门槛的会话。
func seedSummarizableConversations(d *testDeps, n int) {
	for i := 0; i < n; i++ {
		convID := uuid.New()
		d.repo.conversations[convID] = &Conversation{ID: convID}
		for seq := int64(1); seq <= summaryTriggerMessages; seq++ {
			d.repo.messages[convID] = append(d.repo.messages[convID],
				&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser,
					Content: "msg", Status: MsgCompleted, SequenceNo: seq})
		}
	}
}

// 单个会话失败可以吞掉,ctx 被取消不行——那说明 River 的 job 超时把这一轮
// 截断了,排在后面的会话根本没被扫到。这里让第一个会话的模型调用正好触发
// 取消,扫描返回 nil 的话 River 会把被截断的一轮记成 completed(@issue #20)。
func TestMaintainAllSummaries_CancelledMidSweep_ReportsFailure(t *testing.T) {
	d := newMemoryTestUsecase()
	seedSummarizableConversations(d, 2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.registry.chatModel.generateHook = cancel

	err := d.uc.maintainAllSummaries(ctx)

	require.Error(t, err, "被截断的一轮不能返回 nil")
	assert.ErrorIs(t, err, context.Canceled)
}

// extractAllPreferences 是同一个形状的第二条扫描路径(每 10 分钟一轮,
// 复扫所有累计超过 40 条消息的会话),同样不能把截断当成功。
func TestExtractAllPreferences_CancelledMidSweep_ReportsFailure(t *testing.T) {
	d := newMemoryTestUsecase()
	for i := 0; i < 2; i++ {
		convID := uuid.New()
		d.repo.conversations[convID] = &Conversation{ID: convID}
		for seq := int64(1); seq <= preferenceExtractionMessages; seq++ {
			d.repo.messages[convID] = append(d.repo.messages[convID],
				&Message{ID: uuid.New(), ConversationID: convID, Role: domain.RoleUser,
					Content: "msg", Status: MsgCompleted, SequenceNo: seq})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.registry.chatModel.generateHook = cancel

	err := d.uc.extractAllPreferences(ctx)

	require.Error(t, err, "被截断的一轮不能返回 nil")
	assert.ErrorIs(t, err, context.Canceled)
}
