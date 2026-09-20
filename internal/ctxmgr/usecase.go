package ctxmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

var _ Manager = (*Usecase)(nil)

// Usecase 是 ctxmgr 唯一的实现。
//
// 【没有 tokenizer 字段】理由写在 Budget 类型定义那里：tokenizer 要按
// 每次请求实际用的模型解析，不能在构造时定死成一个单例共享的实例。
type Usecase struct {
	compressor Compressor
}

func NewUsecase(compressor Compressor) *Usecase {
	return &Usecase{compressor: compressor}
}

// Build 实现开发文档 §6.3 的预算阶梯，五行判据按顺序执行：
//
//	0  不可裁剪区自身超预算        → 直接 ErrOverflow，不进入 1-4
//	1  总量未超                   → 不动作，原样返回
//	2  超了，且有可压缩区          → 压缩 Summary / 较早的 Recent Messages
//	3  还超                       → 减 RAG Top-K / Memory Top-K
//	4  还超                       → 返回 ErrOverflow
//
// 【第 3 步先减 Memory 再减 Chunk】方案没有明确这个顺序，这是本实现的
// 选择：Chunk 是回答用户这次提问的直接证据，Memory 是"记得用户长期
// 偏好"这类锦上添花的个性化信息——预算紧张时，优先保住能不能把问题
// 答对，个性化可以先让路。这个顺序本身不是外部承诺，只是没有更高层
// 依据时的一个合理默认。
func (u *Usecase) Build(ctx context.Context, req Request) (*FinalContext, error) {
	tok := req.Budget.Tokenizer
	if tok == nil {
		return nil, fmt.Errorf("ctxmgr: Budget.Tokenizer must not be nil")
	}

	fixed := buildFixedItems(req, tok)
	fixedCost := sumCost(fixed)

	// 第 0 步：必须最先判断。写错顺序（比如先压缩再判断这一步）
	// 就是方案警告过的"静默截断"——不可裁剪区本来就放不下时，
	// 后面的压缩/削减都是徒劳，唯一正确的结果是明确报错。
	if fixedCost > req.Budget.Input {
		return nil, ErrOverflow
	}

	compressible := buildCompressibleItems(req, tok)
	chunkItems := buildChunkItems(req.Chunks, tok)
	memoryItems := buildMemoryItems(req.Memories, tok)

	total := func() int {
		return fixedCost + sumCost(compressible) + sumCost(chunkItems) + sumCost(memoryItems)
	}

	// 第 1 步。
	if total() <= req.Budget.Input {
		return finalize(fixed, compressible, chunkItems, memoryItems, req.Chunks), nil
	}

	// 第 2 步：压缩可压缩区。目标 token 数是"扣掉固定区和删除区之后，
	// 可压缩区还能占多少"——压缩器按这个目标尽力压，压不到位也没关系，
	// 第 3 步会接着削减删除区。
	if len(compressible) > 0 {
		remaining := req.Budget.Input - fixedCost - sumCost(chunkItems) - sumCost(memoryItems)
		if remaining < 0 {
			remaining = 0
		}
		compressed, err := u.compressor.Compress(ctx, compressible, remaining, tok)
		if err != nil {
			return nil, fmt.Errorf("compress context: %w", err)
		}
		compressible = compressed
	}

	if total() <= req.Budget.Input {
		return finalize(fixed, compressible, chunkItems, memoryItems, req.Chunks), nil
	}

	// 第 3 步：削减删除区。从尾部丢——两个切片都已经由调用方按相关度
	// 从高到低排好，尾部就是"当前存活的里面最不相关的"。
	for len(memoryItems) > 0 && total() > req.Budget.Input {
		memoryItems = memoryItems[:len(memoryItems)-1]
	}
	for len(chunkItems) > 0 && total() > req.Budget.Input {
		chunkItems = chunkItems[:len(chunkItems)-1]
	}

	if total() <= req.Budget.Input {
		survivingChunks := req.Chunks[:len(chunkItems)]
		return finalize(fixed, compressible, chunkItems, memoryItems, survivingChunks), nil
	}

	// 第 4 步：真的装不下。
	return nil, ErrOverflow
}

func buildFixedItems(req Request, tok llm.Tokenizer) []Item {
	items := []Item{
		{Source: domain.SourceSystem, Content: req.SystemPrompt, TokenCost: tok.Count(req.SystemPrompt)},
		{Source: domain.SourceUser, Content: req.UserInput, TokenCost: tok.Count(req.UserInput)},
	}
	if req.AgentState != nil {
		content := formatAgentState(req.AgentState)
		items = append(items, Item{
			Source: domain.SourceAgentState, Content: content, TokenCost: tok.Count(content),
		})
	}
	return items
}

// formatAgentState 只是给 tokenizer 计数、拼进最终 prompt 用的确定性
// 序列化——不是给别的代码反序列化用的，所以不用 JSON。
func formatAgentState(s *AgentState) string {
	return fmt.Sprintf("current_step=%d tool_calls=%s", s.CurrentStep, strings.Join(s.ToolCalls, ","))
}

func buildCompressibleItems(req Request, tok llm.Tokenizer) []Item {
	var items []Item
	// Summary 排在最前——它代表"更早"的上下文，和 RecentMessages
	// "新 → 旧"的顺序拼在一起，整个可压缩区就是一条从旧到新的时间线。
	if req.Summary != "" {
		items = append(items, Item{
			Source: domain.SourceSummary, Content: req.Summary,
			TokenCost: tok.Count(req.Summary), Compressible: true,
		})
	}
	for _, m := range req.RecentMessages {
		items = append(items, Item{
			Source: domain.SourceRecent, Content: m.Content,
			TokenCost: tok.Count(m.Content), Compressible: true,
		})
	}
	return items
}

func buildChunkItems(chunks []domain.Chunk, tok llm.Tokenizer) []Item {
	items := make([]Item, len(chunks))
	for i, c := range chunks {
		items[i] = Item{
			Source: domain.SourceChunk, Content: c.Content,
			TokenCost: tok.Count(c.Content), Relevance: c.Score,
		}
	}
	return items
}

// buildMemoryItems 的 Relevance 恒为零值——domain.Memory 不携带数值
// 相关度，调用方（conversation.RetrieveMemory）已经按相关度把切片排好，
// Build 裁剪时依赖的是这个"顺序"本身，不读 Item.Relevance 这个字段。
func buildMemoryItems(memories []domain.Memory, tok llm.Tokenizer) []Item {
	items := make([]Item, len(memories))
	for i, m := range memories {
		items[i] = Item{Source: domain.SourceMemory, Content: m.Content, TokenCost: tok.Count(m.Content)}
	}
	return items
}

func sumCost(items []Item) int {
	total := 0
	for _, it := range items {
		total += it.TokenCost
	}
	return total
}

// finalize 把四组条目拼成最终结果，并把存活下来的 Chunk 转成 Citation。
func finalize(fixed, compressible, chunkItems, memoryItems []Item, survivingChunks []domain.Chunk) *FinalContext {
	all := make([]Item, 0, len(fixed)+len(compressible)+len(chunkItems)+len(memoryItems))
	all = append(all, fixed...)
	all = append(all, compressible...)
	all = append(all, chunkItems...)
	all = append(all, memoryItems...)

	citations := make([]domain.Citation, 0, len(survivingChunks))
	for _, c := range survivingChunks {
		citations = append(citations, domain.Citation{
			ChunkID: c.ID, DocumentID: c.DocumentID,
			Filename: c.Filename, Snippet: c.Content, Score: c.Score,
		})
	}

	return &FinalContext{Items: all, TokenCost: sumCost(all), Citations: citations}
}
