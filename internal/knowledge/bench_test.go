package knowledge

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// benchPaperText 约 20KB 的论文正文：中英混排 + 重复段落，
// 用来给"上传时的 SimHash 指纹"和"解析切分"提供稳定输入。
var benchPaperText = strings.Repeat(
	"Attention is all you need. The Transformer is the first transduction model relying entirely on self-attention.\n"+
		"我们提出一种新的简单网络架构 Transformer，完全基于注意力机制，彻底抛弃了循环与卷积。\n", 60)

// BenchmarkContentFingerprint 是上传去重的入口：整篇正文算一次 64 位 SimHash。
func BenchmarkContentFingerprint(b *testing.B) {
	b.SetBytes(int64(len(benchPaperText)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if ContentFingerprint(benchPaperText) == "" {
			b.Fatal("empty fingerprint")
		}
	}
}

// BenchmarkParseAndChunk 是 worker 解析流水线里不依赖 PDF sidecar 的一半：
// 把纯文本按标题/段落切成有页码语义的分块。
func BenchmarkParseAndChunk(b *testing.B) {
	var doc strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&doc, "## %d 相关工作\n\n", i+1)
		doc.WriteString(benchPaperText[:1200])
		doc.WriteString("\n\n")
	}
	raw := []byte(doc.String())
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(parseAndChunk(raw)) == 0 {
			b.Fatal("no chunks")
		}
	}
}

// BenchmarkBuildCitationGraph 是引用图谱的构图 + PageRank。
// 20 篇论文、每篇引用 4 篇其它论文，规模与演示库一致。
func BenchmarkBuildCitationGraph(b *testing.B) {
	docs := make([]*Document, 20)
	titles := make(map[uuid.UUID]string, 20)
	refs := make(map[uuid.UUID][]domain.ReferenceEntry, 20)
	for i := range docs {
		id := uuid.New()
		docs[i] = &Document{ID: id, Filename: fmt.Sprintf("paper-%02d.pdf", i)}
		titles[id] = fmt.Sprintf("Paper %02d", i)
		for j := 1; j <= 4; j++ {
			refs[id] = append(refs[id], domain.ReferenceEntry{
				Index: j,
				Text:  fmt.Sprintf("Paper %02d", (i+j)%20),
			})
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g := BuildCitationGraph(docs, titles, refs)
		if g == nil || len(g.Nodes) == 0 {
			b.Fatal("empty graph")
		}
	}
}
