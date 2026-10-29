package recommend

import (
	"strings"
	"testing"

	"github.com/XiaoleC05/CongoRAG/internal/arxiv"
)

func TestBuildRanksRelevantAndFiltersDown(t *testing.T) {
	lib := []string{"Attention Is All You Need", "BERT: Pre-training of Deep Bidirectional Transformers"}
	papers := []arxiv.Paper{
		{
			ID:        "1",
			Title:     "Efficient Attention for Long Documents",
			Abstract:  "We study attention mechanisms and transformer efficiency for long documents.",
			Published: "2026-10-01T00:00:00Z",
		},
		{
			ID:        "2",
			Title:     "A Survey of Graph Databases",
			Abstract:  "Graph databases store vertices and edges.",
			Published: "2026-10-01T00:00:00Z",
		},
		{
			ID:        "3",
			Title:     "Attention Is All You Need Revisited",
			Abstract:  "Attention transformer revisiting. Code: https://github.com/example/attn",
			Comment:   "github.com/example/attn2",
			Published: "2026-09-01T00:00:00Z",
		},
	}
	votes := map[string]string{"2": VoteDown}
	out := Build(lib, papers, votes, 10)
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2 (down-voted filtered): %+v", len(out), out)
	}
	if out[0].Paper.ID != "3" {
		t.Fatalf("top result = %s, want 3 (github + relevance)", out[0].Paper.ID)
	}
	if out[0].GithubURL == "" {
		t.Fatalf("expected github url, got reason %q", out[0].Reason)
	}
	if !strings.Contains(out[0].Reason, "开源代码") {
		t.Fatalf("reason = %q", out[0].Reason)
	}
}

func TestBuildDeduplicatesTitles(t *testing.T) {
	papers := []arxiv.Paper{
		{ID: "1", Title: "Attention Is All You Need", Abstract: "attention transformer"},
		{ID: "2", Title: "attention is all you need", Abstract: "attention transformer"},
		{ID: "3", Title: "Other", Abstract: "attention transformer"},
	}
	out := Build([]string{"Attention Is All You Need"}, papers, nil, 10)
	if len(out) != 1 || out[0].Paper.ID != "3" {
		t.Fatalf("dedupe/library filter failed: %+v", out)
	}
}

func TestQueryForTitles(t *testing.T) {
	q := QueryForTitles([]string{"Attention Is All You Need", "BERT pre-training", "attention transformer"})
	if !strings.Contains(q, "attention") || !strings.Contains(q, " OR ") {
		t.Fatalf("query = %q", q)
	}
	if len(strings.Split(q, " OR ")) > 4 {
		t.Fatalf("query has too many terms: %q", q)
	}
}

func TestFindGithub(t *testing.T) {
	if got := findGithub("code at https://github.com/a/b"); got != "https://github.com/a/b" {
		t.Fatalf("got %q", got)
	}
	if got := findGithub("no link here"); got != "" {
		t.Fatalf("got %q", got)
	}
}
