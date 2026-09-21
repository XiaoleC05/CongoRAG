package ctxmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

// fakeChatModel 记录最后一次 Generate 收到的完整消息列表——这里要验证的
// 正是"尺寸约束有没有真的进到发给模型的提示词里"，所以必须看得见那条
// 提示词，不能只断言返回值。
type fakeChatModel struct {
	lastMessages []llm.Message
	resp         *llm.Message
}

func (m *fakeChatModel) Generate(ctx context.Context, msgs []llm.Message, opts ...llm.CallOption) (*llm.Message, error) {
	m.lastMessages = msgs
	if m.resp != nil {
		return m.resp, nil
	}
	return &llm.Message{Role: domain.RoleAssistant, Content: "压缩后的摘要"}, nil
}

func (m *fakeChatModel) Stream(ctx context.Context, msgs []llm.Message, opts ...llm.CallOption) (llm.Stream, error) {
	return nil, errors.New("fakeChatModel.Stream: 压缩不走流式，这个测试不该走到这里")
}

// fakeRegistry 只实现 LLMCompressor 用得到的那两个方法，其余方法一律
// 返回错误——"这条路径没实现"比返回零值更容易在测试里被发现。
type fakeRegistry struct {
	chat *fakeChatModel
}

func (r *fakeRegistry) Chat(ctx context.Context, modelID string) (llm.ChatModel, error) {
	return r.chat, nil
}

func (r *fakeRegistry) ActiveModelID(ctx context.Context, kind llm.Kind) (string, error) {
	return "fake-chat-model", nil
}

// 压缩器只用 Chat/Generate，从不问"当前是哪个模型"——走到这里说明
// 被测代码的依赖方向变了。
func (r *fakeRegistry) ActiveModel(ctx context.Context, kind llm.Kind) (*llm.Model, error) {
	return nil, errors.New("fakeRegistry.ActiveModel: 这个测试不该走到这里")
}

func (r *fakeRegistry) Embedder(ctx context.Context, modelID string) (llm.Embedder, error) {
	return nil, errors.New("fakeRegistry.Embedder: 这个测试不该走到这里")
}

func (r *fakeRegistry) Tokenizer(ctx context.Context, modelID string) (llm.Tokenizer, error) {
	return fakeTokenizer{}, nil
}

func (r *fakeRegistry) Capabilities(ctx context.Context, modelID string) (llm.Capabilities, error) {
	return llm.Capabilities{}, errors.New("fakeRegistry.Capabilities: 这个测试不该走到这里")
}

func (r *fakeRegistry) ResolveChatEndpoint(ctx context.Context, modelID string) (string, string, string, error) {
	return "", "", "", errors.New("fakeRegistry.ResolveChatEndpoint: 这个测试不该走到这里")
}

func (r *fakeRegistry) ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error) {
	return 0, errors.New("fakeRegistry.ProbeEmbeddingDimension: 这个测试不该走到这里")
}

// targetTokens 必须真的进到提示词里：它是 Build 算出来的"可压缩区还允许
// 占多少 token"，模型看不见这个数字就只能自由发挥，压出来的摘要多长全
// 看运气——而多出来的长度是从尾部 chunk 的预算里挤的。
func TestCompress_PutsTokenBudgetIntoPrompt(t *testing.T) {
	chat := &fakeChatModel{}
	c := NewLLMCompressor(&fakeRegistry{chat: chat})

	got, err := c.Compress(context.Background(),
		[]Item{{Source: domain.SourceRecent, Role: domain.RoleUser, Content: "很长的一段历史"}},
		321, fakeTokenizer{perItem: 7})

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, chat.lastMessages, 2)
	assert.Contains(t, chat.lastMessages[0].Content, "321",
		"预算必须写进系统提示词，否则 targetTokens 这个参数等于没传")
	assert.Contains(t, chat.lastMessages[1].Content, "很长的一段历史",
		"待压缩的内容仍然要原样交给模型")
}

// 预算被夹到 0（删除区已经把预算占满）时，提示词里不能出现"控制在 0 个
// token 以内"这种没法遵守的指令，但仍然要给出"尽可能短"这个方向。
func TestCompress_ZeroBudget_AsksForShortestPossible(t *testing.T) {
	chat := &fakeChatModel{}
	c := NewLLMCompressor(&fakeRegistry{chat: chat})

	_, err := c.Compress(context.Background(),
		[]Item{{Source: domain.SourceRecent, Role: domain.RoleUser, Content: "历史"}},
		0, fakeTokenizer{perItem: 7})

	require.NoError(t, err)
	require.Len(t, chat.lastMessages, 2)
	assert.NotContains(t, chat.lastMessages[0].Content, "0 个 token")
	assert.Contains(t, chat.lastMessages[0].Content, "尽可能短")
}

// 压缩提示词里必须看得出"谁说的"：可压缩区里混着 user 和 assistant，
// 两类条目如果共用 Source 当标签，模型看到的就是两段无主文本，压出来的
// 摘要会把助手自己的话记成"用户说……"——而这个错误归属会被持久化进
// summary，跨摘要边界一路带下去。
func TestFormatCompressibleItems_LabelsSpeakerByRole(t *testing.T) {
	items := []Item{
		{Source: domain.SourceSummary, Content: "更早的摘要"},
		{Source: domain.SourceRecent, Role: domain.RoleUser, Content: "知识库在哪个目录？"},
		{Source: domain.SourceRecent, Role: domain.RoleAssistant, Content: "data/documents"},
	}

	got := formatCompressibleItems(items)

	assert.Contains(t, got, "summary: 更早的摘要")
	assert.Contains(t, got, "user: 知识库在哪个目录？")
	assert.Contains(t, got, "assistant: data/documents")
	assert.NotContains(t, got, "recent: ", "Recent 条目不能用 Source 当标签")
}

// Role 为空的 Recent 条目退回 Source 标签——标签可以是"来源"，但不能
// 什么都不写（一行以 ": " 开头的文本对模型毫无意义）。
func TestFormatCompressibleItems_FallsBackToSourceWhenRoleMissing(t *testing.T) {
	got := formatCompressibleItems([]Item{
		{Source: domain.SourceRecent, Content: "没有角色的历史"},
	})

	assert.Equal(t, "recent: 没有角色的历史\n", got)
}
