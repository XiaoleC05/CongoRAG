package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ UsageRepo = (*PgUsageRepo)(nil)

// PgUsageRepo 是 token_usage 表的实现。无状态，连接由调用方通过 q 传入——
// 和本包 / knowledge / agent 的其余 repo 同一个模式。
type PgUsageRepo struct{}

func NewPgUsageRepo() *PgUsageRepo { return &PgUsageRepo{} }

func (r *PgUsageRepo) InsertUsage(ctx context.Context, q platform.Querier, u *Usage) error {
	_, err := q.Exec(ctx,
		`INSERT INTO token_usage
		   (id, provider_id, model_id, message_id, kind, prompt_tokens, completion_tokens, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.New(), u.ProviderID, u.ModelID, u.MessageID, string(u.Kind),
		u.PromptTokens, u.CompletionTokens, u.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert token usage for model %s: %w", u.ModelID, platform.WrapPgErr(err))
	}
	return nil
}

func (r *PgUsageRepo) UsageSummary(ctx context.Context, q platform.Querier, since, until *time.Time) ([]*UsageByModel, error) {
	// 【为什么 JOIN llm_models】原始设计就是按模型聚合（索引建在 provider_id
	// 上，迁移注释里写着"GET /api/v1/usage 按它们聚合"）。模型名要取
	// llm_models.model_id——那是用户在引导页里敲进去、认得出来的那个名字，
	// 而不是 llm_models.id 那个 uuid。
	//
	// 【左闭右开】`>= since AND < until`：这样相邻两个区间拼起来不重不漏。
	// 两边都可以为 NULL（不限），用 ($1::timestamptz IS NULL OR ...) 表达。
	//
	// 本查询按 created_at 过滤但没有对应索引——见 UsageRepo 接口上的注释，
	// 行数到十万级时再加。
	rows, err := q.Query(ctx,
		`SELECT t.provider_id, t.model_id, m.model_id, m.kind,
		        COUNT(*), COALESCE(SUM(t.prompt_tokens), 0), COALESCE(SUM(t.completion_tokens), 0)
		 FROM token_usage t
		 JOIN llm_models m ON m.id = t.model_id
		 WHERE ($1::timestamptz IS NULL OR t.created_at >= $1)
		   AND ($2::timestamptz IS NULL OR t.created_at < $2)
		 GROUP BY t.provider_id, t.model_id, m.model_id, m.kind
		 ORDER BY SUM(t.prompt_tokens + t.completion_tokens) DESC`,
		since, until)
	if err != nil {
		return nil, fmt.Errorf("summarise token usage: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()

	// 【必须是空切片而不是 nil】响应体里的 byModel 会序列化成 null，
	// 而前端拿到 null 去做 .map() 会直接崩——和 toAPIKBList 那条
	// "空切片不能是 null"是同一个约定。
	out := make([]*UsageByModel, 0)
	for rows.Next() {
		row := &UsageByModel{}
		var kind string
		if err := rows.Scan(&row.ProviderID, &row.ModelID, &row.ModelName, &kind,
			&row.Calls, &row.PromptTokens, &row.CompletionTokens); err != nil {
			return nil, fmt.Errorf("scan usage row: %w", err)
		}
		row.Kind = Kind(kind)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage rows: %w", err)
	}
	return out, nil
}
