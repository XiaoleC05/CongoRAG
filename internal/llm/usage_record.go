package llm

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// usageWriteTimeout 是记账那次写库的上限。
//
// 与 agent.finalizeTimeout / knowledge 的终态写入同值同理由（5 秒）：它们是
// 同一类"主流程已经结束、只差把结果记下来"的写。
const usageWriteTimeout = 5 * time.Second

// usageRecorder 是适配器记账需要的最小上下文：往哪记、记给谁。
//
// 【为什么做成值类型挂在适配器上，而不是让适配器每次去查】provider 与 model
// 的 uuid 在构造适配器时就已经解析好了（见 registry.Chat / Embedder），
// 调用时再查一遍是白跑。
type usageRecorder struct {
	repo       UsageRepo
	db         platform.Querier
	providerID uuid.UUID
	modelID    uuid.UUID
	kind       Kind
}

// enabled 报告这个 recorder 是否可用。
//
// 【为什么允许它不可用】装配时没传 UsageRepo（测试里常见）不该让适配器
// 崩掉——记账是观测，缺了它对话仍然要能跑。
func (r usageRecorder) enabled() bool { return r.repo != nil && r.db != nil }

// record 落一行用量。
//
// 【同步写，但脱离请求 ctx】两个理由：
//   - 不阻塞用户：写入发生在"这次调用已经结束"之后（流式路径上就是收到
//     最后一个 chunk、或者 Close 的时候），用户等待的 token 流已经走完。
//   - 不能因为主流程失败而丢：客户端断开时请求 ctx 已经被取消，pgx 用
//     已取消的 ctx 连连接都拿不到。context.WithoutCancel 保留值（比如
//     注入的 message_id）但去掉取消。
//
// 【失败只记日志、绝不上抛】记账是观测，不是业务正确性的一部分；一次记账
// 失败不该让用户看不到回答。代价是"记账一直在失败"只会出现在日志里——
// 所以日志里带上 provider/model/kind，方便 grep。
func (r usageRecorder) record(ctx context.Context, promptTokens, completionTokens int) {
	if !r.enabled() {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageWriteTimeout)
	defer cancel()

	u := &Usage{
		ProviderID:       r.providerID,
		ModelID:          r.modelID,
		MessageID:        usageMessageFrom(ctx),
		Kind:             r.kind,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CreatedAt:        time.Now(),
	}
	if err := r.repo.InsertUsage(wctx, r.db, u); err != nil {
		slog.Default().Warn("failed to record token usage",
			"provider_id", r.providerID, "model_id", r.modelID, "kind", r.kind, "error", err)
	}
}

// recordFromMeta 从 Eino 的 ResponseMeta 里取用量并记一行；没有用量时什么都不做。
func (r usageRecorder) recordFromMeta(ctx context.Context, meta *schema.ResponseMeta) {
	if meta == nil || meta.Usage == nil {
		return
	}
	r.record(ctx, meta.Usage.PromptTokens, meta.Usage.CompletionTokens)
}

// ────────────────────────────────────────────────────────────────
// 把 message_id 沿 ctx 传进适配器
// ────────────────────────────────────────────────────────────────

type usageMessageKey struct{}

// WithUsageMessage 把这条 assistant 消息的 id 放进 ctx，记账时写进
// token_usage.message_id。
//
// 【为什么走 ctx 而不是加参数】记账发生在适配器内部（Recv / Generate），
// 那里拿不到调用方的消息 id——把消息 id 加进 llm.Message 会让线路类型认识
// 数据库实体（port.go 明确说过它不是一回事）。ctx 值在这个项目里有先例：
// platform.RequestIDFrom。
//
// 只有聊天主路径会设它；摘要压缩、记忆抽取、文档索引这些调用说明不了
// 归属，留 NULL（那正是迁移注释里 message_id 可空的理由）。
func WithUsageMessage(ctx context.Context, messageID uuid.UUID) context.Context {
	return context.WithValue(ctx, usageMessageKey{}, messageID)
}

func usageMessageFrom(ctx context.Context) *uuid.UUID {
	if id, ok := ctx.Value(usageMessageKey{}).(uuid.UUID); ok {
		return &id
	}
	return nil
}
