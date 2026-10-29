package knowledge

import (
	"testing"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

func docWithID(name string, id uuid.UUID) *Document {
	return &Document{ID: id, Filename: name, Status: StatusReady}
}

func refTo(target uuid.UUID, times int) []domain.ReferenceEntry {
	out := make([]domain.ReferenceEntry, 0, times)
	for i := 0; i < times; i++ {
		out = append(out, domain.ReferenceEntry{MatchedDocumentID: target.String()})
	}
	return out
}

func TestBuildCitationGraphRanksCornerstone(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	docs := []*Document{
		docWithID("paper-a.pdf", a),
		docWithID("paper-b.pdf", b),
		docWithID("paper-c.pdf", c),
	}
	refs := map[uuid.UUID][]domain.ReferenceEntry{
		a: refTo(b, 1), // A 引用 B
		b: refTo(c, 1), // B 引用 C
		c: refTo(b, 2), // C 引用 B 两次
	}
	graph := BuildCitationGraph(docs, map[uuid.UUID]string{a: "Paper A"}, refs)
	if len(graph.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(graph.Nodes))
	}
	if len(graph.Edges) != 3 {
		t.Fatalf("edges = %d, want 3", len(graph.Edges))
	}
	// B 被 A、C 引用，且 C 引用了两次：入度 3，应排第一。
	top := graph.Nodes[0]
	if top.DocumentID != b {
		t.Fatalf("top node = %s, want B", top.Filename)
	}
	if top.CitationCount != 3 {
		t.Fatalf("B citation count = %d, want 3", top.CitationCount)
	}
	if !top.Cornerstone {
		t.Fatal("B should be marked as cornerstone")
	}
	if top.PageRank <= graph.Nodes[1].PageRank {
		t.Fatal("B page rank should be highest")
	}
}

func TestBuildCitationGraphSingleNode(t *testing.T) {
	a := uuid.New()
	graph := BuildCitationGraph([]*Document{docWithID("solo.pdf", a)}, nil, nil)
	if len(graph.Nodes) != 1 || len(graph.Edges) != 0 {
		t.Fatalf("graph = %+v", graph)
	}
	if graph.Nodes[0].Cornerstone {
		t.Fatal("single-node graph should not mark a cornerstone")
	}
}

func TestBuildCitationGraphIgnoresSelfAndUnknown(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	unknown := uuid.New()
	docs := []*Document{docWithID("a.pdf", a), docWithID("b.pdf", b)}
	refs := map[uuid.UUID][]domain.ReferenceEntry{
		a: append(refTo(a, 1), refTo(unknown, 1)...),
	}
	graph := BuildCitationGraph(docs, nil, refs)
	if len(graph.Edges) != 0 {
		t.Fatalf("edges = %+v, want none", graph.Edges)
	}
	for _, n := range graph.Nodes {
		if n.CitationCount != 0 {
			t.Fatalf("node %s citation count = %d", n.Filename, n.CitationCount)
		}
	}
}
