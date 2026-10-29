package knowledge

import (
	"sort"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// citationPair 是引用边的端点对，用作权重 map 的 key。
type citationPair struct{ from, to int }

// CitationNode 是引用图谱里的一个节点（一篇库内文档）。
type CitationNode struct {
	DocumentID     uuid.UUID
	Filename       string
	Title          string
	CitationCount  int // 被库内其它文档引用的次数（入度）
	ReferenceCount int // 参考文献里命中库内的条数（出度）
	PageRank       float64
	Cornerstone    bool
}

// CitationEdge 是一条"引用"边：From 引用了 To。Weight 是引用条目数。
type CitationEdge struct {
	From   uuid.UUID
	To     uuid.UUID
	Weight int
}

// CitationGraph 是一个知识库的局域引用图谱。
type CitationGraph struct {
	Nodes []CitationNode
	Edges []CitationEdge
}

// BuildCitationGraph 把"每篇文档的参考文献匹配结果"聚合成有向图。
//
// 【为什么在内存里聚合，而不是建一张 document_citations 表】引用关系是
// 由"参考文献文本 ↔ 库内标题"动态匹配出来的，标题/文档变化后旧结论就
// 过期；持久化要额外一套失效逻辑，而一个知识库几十篇论文的图计算是毫秒级。
// 需要落库时再写 projections，那是 P2，不是这次的范围。
func BuildCitationGraph(docs []*Document, titles map[uuid.UUID]string, refs map[uuid.UUID][]domain.ReferenceEntry) *CitationGraph {
	idx := make(map[uuid.UUID]int, len(docs))
	graph := &CitationGraph{
		Nodes: make([]CitationNode, 0, len(docs)),
		Edges: []CitationEdge{},
	}
	for _, d := range docs {
		idx[d.ID] = len(graph.Nodes)
		graph.Nodes = append(graph.Nodes, CitationNode{
			DocumentID: d.ID,
			Filename:   d.Filename,
			Title:      titles[d.ID],
		})
	}

	weights := make(map[citationPair]int)
	for from, entries := range refs {
		fromIdx, ok := idx[from]
		if !ok {
			continue
		}
		for _, e := range entries {
			if e.MatchedDocumentID == "" {
				continue
			}
			to, err := uuid.Parse(e.MatchedDocumentID)
			if err != nil {
				continue
			}
			toIdx, ok := idx[to]
			if !ok || toIdx == fromIdx {
				continue
			}
			weights[citationPair{from: fromIdx, to: toIdx}]++
		}
	}

	for p, w := range weights {
		graph.Edges = append(graph.Edges, CitationEdge{
			From:   graph.Nodes[p.from].DocumentID,
			To:     graph.Nodes[p.to].DocumentID,
			Weight: w,
		})
		graph.Nodes[p.from].ReferenceCount++
		graph.Nodes[p.to].CitationCount += w
	}
	// map 迭代顺序不稳定；排序让同一份数据每次返回同样顺序，前端/SVG
	// 的布局和截图才是可复现的。
	sort.Slice(graph.Edges, func(i, j int) bool {
		a, b := graph.Edges[i], graph.Edges[j]
		if a.From != b.From {
			return a.From.String() < b.From.String()
		}
		return a.To.String() < b.To.String()
	})

	ranks := pageRank(len(graph.Nodes), weights)
	for i := range graph.Nodes {
		graph.Nodes[i].PageRank = ranks[i]
	}

	cornerstones := cornerstoneIndexes(graph.Nodes)
	for _, i := range cornerstones {
		graph.Nodes[i].Cornerstone = true
	}
	// 节点按 PageRank 降序输出：前端"基石论文"表格直接按数组顺序渲染，
	// 不用再排一次；SVG 按数组顺序编号也能让基石节点更靠前。
	sort.SliceStable(graph.Nodes, func(i, j int) bool {
		if graph.Nodes[i].PageRank != graph.Nodes[j].PageRank {
			return graph.Nodes[i].PageRank > graph.Nodes[j].PageRank
		}
		return graph.Nodes[i].Filename < graph.Nodes[j].Filename
	})
	return graph
}

// pageRank 是标准带阻尼的 PageRank（damping=0.85），迭代到收敛。
//
// 节点数很少（一个知识库几十篇），所以不用稀疏矩阵库；这里的目标是
// "基石论文排序正确且可解释"，不是大规模图计算。
func pageRank(n int, weights map[citationPair]int) []float64 {
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	const damping = 0.85
	// weights 的 key 是匿名结构；先统计出度，再按 from 分组。
	outDegree := make([]int, n)
	outLinks := make([][]int, n)
	for p, w := range weights {
		outLinks[p.from] = append(outLinks[p.from], p.to)
		outDegree[p.from] += w
	}
	rank := make([]float64, n)
	for i := range rank {
		rank[i] = 1 / float64(n)
	}
	for iter := 0; iter < 100; iter++ {
		next := make([]float64, n)
		base := (1 - damping) / float64(n)
		// 悬空节点（没有出链）的权重均分给所有节点，避免权重凭空消失。
		dangling := 0.0
		for i := 0; i < n; i++ {
			if outDegree[i] == 0 {
				dangling += rank[i]
				continue
			}
			for _, to := range outLinks[i] {
				// 同一个 to 可能在 outLinks 里只出现一次（weights 已经按
				// 边去重），但权重可能 >1：按权重比例分配。
				next[to] += damping * rank[i] * float64(weights[citationPair{from: i, to: to}]) / float64(outDegree[i])
			}
		}
		for i := 0; i < n; i++ {
			next[i] += base + damping*dangling/float64(n)
		}
		diff := 0.0
		for i := 0; i < n; i++ {
			d := next[i] - rank[i]
			if d < 0 {
				d = -d
			}
			if d > diff {
				diff = d
			}
		}
		rank = next
		if diff < 1e-9 {
			break
		}
	}
	return rank
}

// cornerstoneIndexes 选出"基石论文"：被引过的节点里 PageRank 最高的
// 前 max(1, n/5) 个。n 很小时仍然只标 1~2 个，避免把所有节点都标成基石。
func cornerstoneIndexes(nodes []CitationNode) []int {
	var cited []int
	for i, n := range nodes {
		if n.CitationCount > 0 {
			cited = append(cited, i)
		}
	}
	if len(cited) == 0 {
		return nil
	}
	sort.Slice(cited, func(a, b int) bool {
		return nodes[cited[a]].PageRank > nodes[cited[b]].PageRank
	})
	k := len(nodes) / 5
	if k < 1 {
		k = 1
	}
	if k > len(cited) {
		k = len(cited)
	}
	return cited[:k]
}
