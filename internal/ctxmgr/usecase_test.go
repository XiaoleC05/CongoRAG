package ctxmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

// fakeTokenizer 按"每项固定成本"计数，不 import 任何真实 tokenizer 库——
// 这正是代码架构设计 §5.7 说的"用假 token 数测预算阶梯"能成立的原因：
// Count 只看字符串是否非空，返回一个可预测的常数，测试因此完全不依赖
// 真实分词器的具体行为，只测阶梯逻辑本身对不对。
type fakeTokenizer struct{ perItem int }

func (f fakeTokenizer) Count(s string) int {
	if s == "" {
		return 0
	}
	return f.perItem
}

// fakeCompressor 把传入的条目直接合并成一条固定长度的摘要，
// 不真的调用任何 LLM——ctxmgr 包本身不允许有网络调用（package 说明）。
type fakeCompressor struct {
	// resultTokens 是压缩后声称的 token 数；0 表示"压不动"（原样返回)，
	// 用于测试第 2 步压缩不够、必须进入第 3 步削减的路径。
	resultTokens int
	called       bool
	failErr      error
}

func (f *fakeCompressor) Compress(ctx context.Context, items []Item, targetTokens int, tok llm.Tokenizer) ([]Item, error) {
	f.called = true
	if f.failErr != nil {
		return nil, f.failErr
	}
	if f.resultTokens == 0 {
		return items, nil // 压不动，原样返回
	}
	return []Item{{Source: domain.SourceSummary, Content: "compressed", TokenCost: f.resultTokens, Compressible: true}}, nil
}

func budgetOf(input int, perItem int) Budget {
	return Budget{Input: input, MaxOutput: 100, Tokenizer: fakeTokenizer{perItem: perItem}}
}

// ════════════════════════════════════════════════════════════════
// 开发文档 §6.3 的五行预算阶梯，表驱动，一行对一条
// ════════════════════════════════════════════════════════════════

func TestBuildBudgetLadder(t *testing.T) {
	tests := []struct {
		name string
		// perItem 是 fakeTokenizer 给每个非空字符串算出的固定成本。
		perItem      int
		req          func(perItem int) Request
		compressor   *fakeCompressor
		budgetInput  int
		wantErr      error
		wantContains []string // 期望存活在 FinalContext.Items 里的内容
		wantAbsent   []string // 期望被削减掉、不该出现的内容
	}{
		{
			// 第 0 行：不可裁剪区自身就超了，直接 overflow，
			// 不看后面还有没有可压缩区、删除区。
			name:    "第0步_不可裁剪区自身超预算",
			perItem: 100,
			req: func(perItem int) Request {
				return Request{SystemPrompt: "system", UserInput: "user"}
			},
			budgetInput: 50, // 两项固定区加起来 200，远超 50
			wantErr:     ErrOverflow,
		},
		{
			// 第 1 行：总量没超，什么都不用做，原样通过。
			name:    "第1步_总量未超_不动作",
			perItem: 10,
			req: func(perItem int) Request {
				return Request{
					SystemPrompt: "system", UserInput: "user",
					Chunks: []domain.Chunk{{Content: "chunk-a"}},
				}
			},
			budgetInput:  1000,
			wantContains: []string{"system", "user", "chunk-a"},
		},
		{
			// 第 2 行：超了，但压缩可压缩区之后就够了——不应该走到第 3 步
			// （chunk 应该原样保留，没有被削减）。
			name:    "第2步_压缩后足够_不动删除区",
			perItem: 50,
			req: func(perItem int) Request {
				return Request{
					SystemPrompt: "s", UserInput: "u",
					RecentMessages: []llm.Message{{Content: "很长的历史消息一"}, {Content: "很长的历史消息二"}},
					Chunks:         []domain.Chunk{{Content: "重要分块"}},
				}
			},
			// 固定区 100 + chunk 50 = 150；不压缩的话可压缩区还要 100，总 250 超预算 200。
			// 压缩之后可压缩区只剩一条摘要（fakeCompressor.resultTokens 见下）。
			compressor:   &fakeCompressor{resultTokens: 10},
			budgetInput:  200,
			wantContains: []string{"s", "u", "重要分块", "compressed"},
			wantAbsent:   []string{"很长的历史消息一", "很长的历史消息二"},
		},
		{
			// 第 3 行：压缩之后还超，进入删除区——按顺序先删 Memory 再删 Chunk，
			// 从"相关度最低"（切片尾部）开始丢。
			name:    "第3步_压缩不够_削减删除区",
			perItem: 20,
			req: func(perItem int) Request {
				return Request{
					SystemPrompt: "s", UserInput: "u",
					RecentMessages: []llm.Message{{Content: "历史"}},
					Chunks: []domain.Chunk{
						{ID: uuid.New(), Content: "chunk-高相关", Score: 0.9},
						{ID: uuid.New(), Content: "chunk-低相关", Score: 0.1},
					},
					Memories: []domain.Memory{
						{ID: uuid.New(), Content: "memory-1"},
					},
				}
			},
			// 固定区 40，两个 chunk 共 40，memory 20，可压缩区压不动还是 20 —— 总 120。
			// 预算 100：压缩后（compressor 返回原样，不压）仍然超（120>100），
			// 丢掉 memory-1（20）之后总量变成 100，不再超过预算，
			// 两个 chunk 都应该保留，不需要再丢 chunk。
			compressor:   &fakeCompressor{resultTokens: 0},
			budgetInput:  100,
			wantContains: []string{"s", "u", "历史", "chunk-高相关", "chunk-低相关"},
			wantAbsent:   []string{"memory-1"},
		},
		{
			// 第 3 行的延伸：memory 全丢光还不够，接着从 chunk 尾部丢——
			// chunk-低相关（相关度低，排在切片尾部）应该先被丢，
			// chunk-高相关必须存活到最后。
			name:    "第3步_memory丢光后继续削减chunk尾部",
			perItem: 20,
			req: func(perItem int) Request {
				return Request{
					SystemPrompt: "s", UserInput: "u",
					Chunks: []domain.Chunk{
						{ID: uuid.New(), Content: "chunk-高相关", Score: 0.9},
						{ID: uuid.New(), Content: "chunk-低相关", Score: 0.1},
					},
					Memories: []domain.Memory{{Content: "memory-1"}},
				}
			},
			// 固定区 40，两个 chunk 各 20 共 40，memory 20 —— 总 100。
			// 预算 70：丢掉 memory（剩 80）还超，接着丢 chunk 尾部（剩 60）才够。
			compressor:   &fakeCompressor{resultTokens: 0},
			budgetInput:  70,
			wantContains: []string{"s", "u", "chunk-高相关"},
			wantAbsent:   []string{"memory-1", "chunk-低相关"},
		},
		{
			// 第 4 行：全部丢光依然超预算，只能 overflow。
			name:    "第4步_全部削减仍超预算_overflow",
			perItem: 100,
			req: func(perItem int) Request {
				return Request{
					SystemPrompt: "s", UserInput: "u",
					Chunks:   []domain.Chunk{{Content: "chunk"}},
					Memories: []domain.Memory{{Content: "memory"}},
				}
			},
			compressor:  &fakeCompressor{resultTokens: 0},
			budgetInput: 150, // 固定区已经 200，比预算还大，其实这条会在第0步就拦下
			wantErr:     ErrOverflow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := tt.compressor
			if comp == nil {
				comp = &fakeCompressor{}
			}
			uc := NewUsecase(comp)

			req := tt.req(tt.perItem)
			req.Budget = budgetOf(tt.budgetInput, tt.perItem)

			result, err := uc.Build(context.Background(), req)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			contents := itemContents(result.Items)
			for _, want := range tt.wantContains {
				assert.Contains(t, contents, want, "应该存活")
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, contents, absent, "应该被削减掉")
			}
		})
	}
}

func itemContents(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Content
	}
	return out
}

// ════════════════════════════════════════════════════════════════
// 细节行为
// ════════════════════════════════════════════════════════════════

func TestBuild_NoTokenizer_ReturnsError(t *testing.T) {
	uc := NewUsecase(&fakeCompressor{})
	req := Request{SystemPrompt: "s", UserInput: "u"}
	// 故意不设 Budget.Tokenizer。

	_, err := uc.Build(context.Background(), req)

	require.Error(t, err)
}

// 没有可压缩区（RecentMessages 和 Summary 都是空）时，第 2 步必须
// 跳过——不能对着一个空切片调用 Compressor，那样浪费一次网络请求
// （真实实现是 LLMCompressor，会真的打一次 API）。
func TestBuild_NoCompressibleItems_SkipsCompressorCall(t *testing.T) {
	comp := &fakeCompressor{resultTokens: 10}
	uc := NewUsecase(comp)

	req := Request{
		SystemPrompt: "s", UserInput: "u",
		Chunks: []domain.Chunk{
			{Content: "a"}, {Content: "b"}, {Content: "c"},
		},
		Budget: budgetOf(35, 10), // 固定区 20 + 3 个 chunk 30 = 50，超预算但没有可压缩区可压
	}

	_, err := uc.Build(context.Background(), req)

	// 没有可压缩区时应该直接进入第 3 步削减 chunk，不调用 compressor。
	require.NoError(t, err)
	assert.False(t, comp.called, "没有可压缩区时不该调用 Compressor")
}

func TestBuild_CompressorFails_ErrorPropagates(t *testing.T) {
	comp := &fakeCompressor{failErr: errors.New("upstream boom")}
	uc := NewUsecase(comp)

	req := Request{
		SystemPrompt: "s", UserInput: "u",
		RecentMessages: []llm.Message{{Content: "历史消息"}},
		Budget:         budgetOf(15, 10), // 固定区 20 已经超过 15，第 0 步应该先拦下
	}
	// 调整到确实会走到压缩这一步：固定区必须小于预算。
	req.Budget = budgetOf(25, 10) // 固定区 20 < 25，可压缩区 10，总 30 超预算，进入第 2 步

	_, err := uc.Build(context.Background(), req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream boom")
}

// AgentState 非 nil 时必须被计入不可裁剪区——这是 M4-A 会用到的分支，
// 现在先保证格式化+计数的路径是通的。
func TestBuild_AgentState_CountsTowardFixedRegion(t *testing.T) {
	uc := NewUsecase(&fakeCompressor{})

	withState := Request{
		SystemPrompt: "s", UserInput: "u",
		AgentState: &AgentState{CurrentStep: 2, ToolCalls: []string{"calculator"}},
		Budget:     budgetOf(25, 10), // 固定区（含 AgentState）30 > 25
	}

	_, err := uc.Build(context.Background(), withState)
	assert.ErrorIs(t, err, ErrOverflow, "AgentState 必须占预算，否则这里不该 overflow")

	withoutState := Request{
		SystemPrompt: "s", UserInput: "u",
		Budget: budgetOf(25, 10), // 固定区（不含 AgentState）20 <= 25
	}
	_, err = uc.Build(context.Background(), withoutState)
	assert.NoError(t, err)
}

// Citations 必须只包含"存活到最后"的 Chunk，不是原始传入的全部——
// 被第 3 步削减掉的 chunk 不该出现在随流下发给前端的引用列表里。
func TestBuild_CitationsOnlyIncludeSurvivingChunks(t *testing.T) {
	comp := &fakeCompressor{}
	uc := NewUsecase(comp)

	survivingID := uuid.New()
	droppedID := uuid.New()

	req := Request{
		SystemPrompt: "s", UserInput: "u",
		Chunks: []domain.Chunk{
			{ID: survivingID, Content: "留下来的", Score: 0.9, Filename: "a.md"},
			{ID: droppedID, Content: "会被删的", Score: 0.1, Filename: "b.md"},
		},
		Budget: budgetOf(30, 10), // 固定区 20 + 一个 chunk 10 = 30，只放得下一个 chunk
	}

	result, err := uc.Build(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, result.Citations, 1)
	assert.Equal(t, survivingID, result.Citations[0].ChunkID)
	assert.Equal(t, "a.md", result.Citations[0].Filename)
}

func TestBuild_TokenCostMatchesSumOfItems(t *testing.T) {
	uc := NewUsecase(&fakeCompressor{})
	req := Request{
		SystemPrompt: "s", UserInput: "u",
		Chunks: []domain.Chunk{{Content: "a"}, {Content: "b"}},
		Budget: budgetOf(1000, 15),
	}

	result, err := uc.Build(context.Background(), req)

	require.NoError(t, err)
	var sum int
	for _, it := range result.Items {
		sum += it.TokenCost
	}
	assert.Equal(t, sum, result.TokenCost)
}
