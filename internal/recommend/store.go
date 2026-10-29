package recommend

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// VoteUp / VoteDown 是允许的反馈值；表上也有 CHECK 约束兜底。
const (
	VoteUp   = "up"
	VoteDown = "down"
)

// Store 存取"用户对某条推荐的赞/踩"（migration 0019）。
type Store struct{}

func NewStore() *Store { return &Store{} }

// Vote 记录一次赞/踩（同一用户对同一 arXiv id 覆盖式更新）。
func (s *Store) Vote(ctx context.Context, q platform.Querier, kbID uuid.UUID, arxivID, vote string) error {
	if vote != VoteUp && vote != VoteDown {
		return fmt.Errorf("invalid vote %q: %w", vote, platform.ErrInvalid)
	}
	_, err := q.Exec(ctx,
		`INSERT INTO arxiv_feedback (knowledge_base_id, arxiv_id, vote, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (knowledge_base_id, arxiv_id)
		 DO UPDATE SET vote = EXCLUDED.vote, updated_at = now()`,
		kbID, arxivID, vote)
	if err != nil {
		return fmt.Errorf("record arxiv vote: %w", platform.WrapPgErr(err))
	}
	return nil
}

// Votes 一次取回这批判荐的反馈（没有记录的论文不在 map 里）。
func (s *Store) Votes(ctx context.Context, q platform.Querier, kbID uuid.UUID, arxivIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(arxivIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx,
		`SELECT arxiv_id, vote FROM arxiv_feedback
		  WHERE knowledge_base_id = $1 AND arxiv_id = ANY($2)`,
		kbID, arxivIDs)
	if err != nil {
		return nil, fmt.Errorf("load arxiv votes: %w", platform.WrapPgErr(err))
	}
	defer rows.Close()
	for rows.Next() {
		var id, vote string
		if err := rows.Scan(&id, &vote); err != nil {
			return nil, fmt.Errorf("scan arxiv vote: %w", err)
		}
		out[id] = vote
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate arxiv votes: %w", err)
	}
	return out, nil
}
