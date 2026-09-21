package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/llm"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// ════════════════════════════════════════════════════════════════
// 按 Capabilities.ToolCalling 门控 Agent（issue #38）
//
// 这一位在实现之前是「只写不读」：用户勾了它、平台存了它、GET /providers
// 原样返回，但没有任何生产代码读它，Agent 也一直无条件绑工具。于是把一个
// 不支持 function calling 的模型配给要用工具的 Agent，表现是运行到一半
// 失败、错误指向模型返回，而不是"你配错了模型"。
// ════════════════════════════════════════════════════════════════

// fakeLLMRegistry 只对「当前生效的模型是谁」给出答案。
//
// 其余方法一律 panic——门控只走 ActiveModel，别的被走到就说明测试的意图
// 变了；默默返回零值会让这种变化被吞掉（和 fakeRepo 的既有风格一致）。
type fakeLLMRegistry struct {
	model    *llm.Model
	modelErr error
}

func (f *fakeLLMRegistry) ActiveModel(ctx context.Context, kind llm.Kind) (*llm.Model, error) {
	if f.modelErr != nil {
		return nil, f.modelErr
	}
	return f.model, nil
}

func (f *fakeLLMRegistry) ActiveModelID(ctx context.Context, kind llm.Kind) (string, error) {
	if f.modelErr != nil {
		return "", f.modelErr
	}
	return f.model.ID.String(), nil
}

func (f *fakeLLMRegistry) Chat(ctx context.Context, modelID string) (llm.ChatModel, error) {
	panic("fakeLLMRegistry.Chat: 这个测试不该走到这里")
}

func (f *fakeLLMRegistry) Embedder(ctx context.Context, modelID string) (llm.Embedder, error) {
	panic("fakeLLMRegistry.Embedder: 这个测试不该走到这里")
}

func (f *fakeLLMRegistry) Tokenizer(ctx context.Context, modelID string) (llm.Tokenizer, error) {
	panic("fakeLLMRegistry.Tokenizer: 这个测试不该走到这里")
}

func (f *fakeLLMRegistry) Capabilities(ctx context.Context, modelID string) (llm.Capabilities, error) {
	panic("fakeLLMRegistry.Capabilities: 这个测试不该走到这里")
}

func (f *fakeLLMRegistry) ResolveChatEndpoint(ctx context.Context, modelID string) (string, string, string, error) {
	panic("fakeLLMRegistry.ResolveChatEndpoint: 这个测试不该走到这里")
}

func (f *fakeLLMRegistry) ProbeEmbeddingDimension(ctx context.Context, baseURL, apiKey, modelID string) (int, error) {
	panic("fakeLLMRegistry.ProbeEmbeddingDimension: 这个测试不该走到这里")
}

// newChatModel 造一行 chat 模型，工具能力由调用方给。
func newChatModel(humanName string, toolCalling bool) *llm.Model {
	return &llm.Model{
		ID: uuid.New(), ModelID: humanName, Kind: llm.KindChat,
		Capabilities: llm.Capabilities{Chat: true, ToolCalling: toolCalling},
	}
}

// newGatedUsecase 造一个装好了工具与模型的 Usecase，供门控测试用。
func newGatedUsecase(t *testing.T, toolCalling bool) (*Usecase, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	tools := NewToolRegistry()
	tools.Register(NewCalculator())
	reg := &fakeLLMRegistry{model: newChatModel("test-model", toolCalling)}
	return &Usecase{repo: repo, cp: &fakeCheckpointStore{}, tools: tools, registry: reg}, repo
}

// 声明了工具、而生效模型没有 tool_calling → 创建被拒，且什么都没落库。
func TestCreateAgent_ModelWithoutToolCalling_Rejected(t *testing.T) {
	u, repo := newGatedUsecase(t, false)

	_, err := u.CreateAgent(context.Background(), "计算助手", "", "", []string{"calculator"})

	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrInvalid,
		"必须是 invalid_argument：运行期这条拒绝只能走 SSE error 帧，而 sseerr 不认 ErrConflict")
	// 验收标准要求"报文说清是哪个模型、缺哪个能力"。
	assert.Contains(t, err.Error(), "test-model", "报文要点名是哪个模型")
	assert.Contains(t, err.Error(), "工具调用", "报文要说明缺的是哪个能力")

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.agents, "被拒的创建不该落库")
}

// 【这条钉的是逃生口】零工具 Agent 不需要工具能力，不能因为模型没声明
// tool_calling 就把所有 Agent 一起挡掉。
func TestCreateAgent_NoTools_AllowedWithoutToolCalling(t *testing.T) {
	u, repo := newGatedUsecase(t, false)

	a, err := u.CreateAgent(context.Background(), "纯对话助手", "", "", nil)

	require.NoError(t, err, "零工具 Agent 对模型没有工具能力的要求")
	assert.Equal(t, "纯对话助手", a.Name)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.agents, 1)
}

// 正向路径：能力位为真时照常创建。没有这一条，门控写成恒真也测不出来。
func TestCreateAgent_ModelWithToolCalling_Allowed(t *testing.T) {
	u, repo := newGatedUsecase(t, true)

	_, err := u.CreateAgent(context.Background(), "计算助手", "", "", []string{"calculator"})

	require.NoError(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Len(t, repo.agents, 1)
}

// 运行期是权威的一道：创建之后用户可能换掉 chat 模型（当前生效模型由
// LatestByKind 按 created_at 决定，重跑一次引导页就会换）。
//
// 【这条同时钉四件事】
//  ① Start 返回 ErrInvalid；
//  ② 恰好推了一条 error 帧，type 是 invalid_argument——不是
//     upstream_llm_error，也不是 internal_error（issue #34 修掉的误报形状）；
//  ③ InsertRun 没被调用（没有留下一条用户从没见过的 running 行）；
//  ④ 拦截发生在解析模型之后、runAgent 之前，所以不会去碰真实上游。
func TestStart_ModelWithoutToolCalling_RejectedBeforeRunInserted(t *testing.T) {
	u, repo := newGatedUsecase(t, false)

	a, err := u.CreateAgent(context.Background(), "纯对话助手", "", "", nil)
	require.NoError(t, err)

	// 创建之后把工具补上，模拟"Agent 是合法的，但模型换了"。
	repo.mu.Lock()
	repo.agents[0].ToolNames = []string{"calculator"}
	repo.mu.Unlock()

	sink := newFakeSink()
	_, err = u.Start(context.Background(), a.ID, "算一下 1+1", sink)

	require.ErrorIs(t, err, platform.ErrInvalid)

	require.Len(t, sink.events, 1, "恰好一条 error 帧")
	assert.Equal(t, "error", sink.events[0].Type)

	var envelope struct {
		Type string `json:"type"`
		Data struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(sink.events[0].Payload, &envelope))
	assert.Equal(t, "invalid_argument", envelope.Data.Type,
		"运行期不能返回状态码，只能靠这个 type；用 ErrConflict 会变成 internal_error")
	assert.Contains(t, envelope.Data.Detail, "test-model",
		"detail 必须直接是包装 sentinel 的那一层，否则客户端拿到的是半截话")

	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.runs, "被拒的这一轮不该在 agent_runs 里留下 running 行")
}

// 模型行不存在时（引导还没做）也要给出可操作的错误，而不是 nil 解引用。
func TestCreateAgent_NoActiveModel_IsNotFound(t *testing.T) {
	repo := newFakeRepo()
	tools := NewToolRegistry()
	tools.Register(NewCalculator())
	u := &Usecase{repo: repo, cp: &fakeCheckpointStore{}, tools: tools,
		registry: &fakeLLMRegistry{modelErr: fmt.Errorf("no chat model: %w", platform.ErrNotFound)}}

	_, err := u.CreateAgent(context.Background(), "计算助手", "", "", []string{"calculator"})

	require.ErrorIs(t, err, platform.ErrNotFound)
}
