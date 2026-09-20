package ctxmgr

import (
	"context"
	"fmt"
	"strings"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/llm"
)

var _ Compressor = (*LLMCompressor)(nil)

// LLMCompressor 用当前配置的 chat 模型把可压缩区（Summary + 较早的
// Recent Messages）压成一段更短的摘要。
//
// 【modelID 每次调用时才解析，不在构造时解析】这个类型在装配根只 new
// 一次、长期存活（技术方案 §6 的单例装配模式）；但 BYOK 引导页是进程
// 启动之后用户才走的流程——如果构造函数就去解析"当前 chat 模型"，
// 进程刚启动、用户还没配置任何 provider 时这里会直接失败，
// 而这个类型此刻还什么都没被调用过。所以和 llm.Tokenizer 同一个道理
// （代码架构设计 §5.7 解释过 tokenizer 为什么不能是 Usecase 的字段）：
// "当前是哪个模型"只有在真正要用的那一刻才问得出答案。
type LLMCompressor struct {
	registry llm.Registry
}

func NewLLMCompressor(registry llm.Registry) *LLMCompressor {
	return &LLMCompressor{registry: registry}
}

// compressPrompt 是压缩请求的系统提示词。
//
// 【故意不做成可配置项】开发文档 §6.4 说"ContextCompressor 要定死三件事：
// 每级释放多少 token、用什么提示词、同步还是异步"——这里定的是提示词
// 那一件事：一次性把攒起来的历史压成摘要，不分级。M1.0 先用一个提示词，
// 如果将来需要"分级压缩"（比如先压最旧的一半，不够再压更多），
// 再引入分级参数，不要在没有真实需求之前猜一个分级接口。
const compressPrompt = "请把下面这段对话历史压缩成一段简洁的摘要，" +
	"保留关键事实、决定和用户表达过的偏好，去掉寒暄和重复内容。" +
	"直接输出摘要正文，不要加任何前缀说明。"

// compressBudgetPrompt 把尺寸约束写进提示词。targetTokens 是调用方算出来的
// "可压缩区还允许占多少 token"（Build 第 2 步的 remaining），模型只能看见
// 提示词里的话：不给尺寸指令，它返回多长完全看运气——而多出来的长度是从
// 尾部 chunk 的预算里挤出来的，被挤掉的 chunk 的引用也跟着从下发给客户端
// 的流里消失。这个约束只是"尽量"：LLM 不是精确的 token 计数器，真正的
// 兜底在 Build 那边（压缩结果不比原区域小就整条丢掉）。
func compressBudgetPrompt(targetTokens int) string {
	if targetTokens <= 0 {
		// remaining 被夹到 0 时（删除区已经把预算占满）不能写"控制在 0 个
		// token 以内"——那条指令模型没法遵守，等于什么都没说。
		return compressPrompt + "这段摘要必须尽可能短。"
	}
	return fmt.Sprintf("%s这段摘要控制在 %d 个 token 以内。", compressPrompt, targetTokens)
}

// Compress 把 items 的内容拼起来发给 chat 模型，请求一段不超过
// targetTokens 的摘要，包成一个新的 Item 返回。
//
// 【返回值只有一个 Item，不是压缩后的多个 Item】压缩的产出物本质上就是
// "一段摘要"，用一个 Item（Source: domain.SourceSummary）表示最自然——
// 调用方（Usecase.Build）不关心压缩前是几条消息，只关心压缩后占多少
// token、内容是什么。
//
// 【压缩结果不保证严格不超过 targetTokens】LLM 不是精确的 token 计数器，
// 只能"尽量压到目标附近"。Build 那边压缩完之后还会重新算一次总量，
// 压过头或压不够都会在那一步被发现（压过头的结果会被整条丢掉），
// 这里不需要重试到精确为止。
func (c *LLMCompressor) Compress(ctx context.Context, items []Item, targetTokens int, tok llm.Tokenizer) ([]Item, error) {
	if len(items) == 0 {
		return items, nil
	}

	modelID, err := c.registry.ActiveModelID(ctx, llm.KindChat)
	if err != nil {
		return nil, fmt.Errorf("resolve active chat model for compression: %w", err)
	}
	chatModel, err := c.registry.Chat(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("get chat model for compression: %w", err)
	}

	resp, err := chatModel.Generate(ctx, []llm.Message{
		{Role: domain.RoleSystem, Content: compressBudgetPrompt(targetTokens)},
		{Role: domain.RoleUser, Content: formatCompressibleItems(items)},
	})
	if err != nil {
		return nil, fmt.Errorf("generate compressed summary: %w", err)
	}

	summary := resp.Content
	return []Item{{
		Source:       domain.SourceSummary,
		Content:      summary,
		TokenCost:    tok.Count(summary),
		Compressible: true,
	}}, nil
}

// formatCompressibleItems 把待压缩的条目拼成一段带说话人标签的文本。
//
// 【Recent 条目必须用真角色做标签，不能用 Source】可压缩区里混着两个
// 说话人：user 和 assistant。如果两类都用 Source（"recent:"）标，
// 模型看到的是两段无主的文本，压出来的摘要会把助手自己的话记成
// "用户说……"——这个错误归属会写进持久化的 summary，跨摘要边界
// 一路带下去。Role 为空时退回 Source，保证标签永远不是空字符串。
func formatCompressibleItems(items []Item) string {
	var b strings.Builder
	for _, it := range items {
		label := string(it.Source)
		if it.Source == domain.SourceRecent && it.Role != "" {
			label = string(it.Role)
		}
		b.WriteString(label + ": " + it.Content + "\n")
	}
	return b.String()
}
