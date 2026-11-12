package retrieval

import "testing"

func TestExpandQueryMetadata(t *testing.T) {
	cases := []struct{ q, wantSub string }{
		{"Attention 论文的作者和机构是什么？", "authors affiliations"},
		{"这篇论文的作者是谁", "authors affiliations"},
		{"训练用了多少 GPU", ""},
	}
	for _, c := range cases {
		got := expandQuery(c.q)
		if c.wantSub == "" {
			if got != c.q { t.Fatalf("non-metadata query changed: %q -> %q", c.q, got) }
			continue
		}
		if !contains(got, c.wantSub) { t.Fatalf("expandQuery(%q) = %q, want contains %q", c.q, got, c.wantSub) }
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub { return true }
	}
	return false
}
