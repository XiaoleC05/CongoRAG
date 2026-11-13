package retrieval

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// dedupeBenchChunks 构造 50 个分块：每块 1200 字符左右，其中每 10 块里
// 有 1 块是前面某块的"OCR 变体"（只改标点与空格），其余是不同句子组合。
//
// 为什么按这个形状造：真实检索一次召回 top-50 分块，同一篇论文的多个版本
// 会同时出现——去重函数要在这个规模下对每一块做 5-gram 集合比较。
func dedupeBenchChunks() []domain.Chunk {
	// 每块正文用"块编号 + 独立词表"生成，块与块之间的 5-gram 重合度很低；
	// 每 10 块把前一块去掉空格当成 OCR 变体，保证有真实的近重复对。
	out := make([]domain.Chunk, 0, 50)
	for i := 0; i < 50; i++ {
		var text string
		if i%10 == 9 && len(out) > 0 {
			text = strings.ReplaceAll(out[len(out)-1].Content, " ", "") + " OCRnoise"
		} else {
			var b strings.Builder
			for j := 0; j < 12; j++ {
				fmt.Fprintf(&b, "chunk %d sentence %d studies topic %d and reports evidence %d. ",
					i, j, (i*31+j*17)%997, (i*53+j*29)%997)
				if j%3 == 2 {
					b.WriteString("\n")
				}
			}
			b.WriteString("检索增强生成把外部证据与参数化记忆结合起来，降低事实性错误。 ")
			b.WriteString("引用溯源要求每个论断都能回到原文的具体页码与位置。 ")
			text = b.String()
		}
		out = append(out, domain.Chunk{ID: uuid.New(), Content: text})
	}
	return out
}

// BenchmarkDedupeNearDuplicates 是混合检索排序后的最后一步：
// 对候选分块做 5-gram Jaccard 近重复抑制，直到凑满 topK。
func BenchmarkDedupeNearDuplicates(b *testing.B) {
	chunks := dedupeBenchChunks()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := dedupeNearDuplicates(chunks, 5)
		if len(out) != 5 {
			b.Fatalf("got %d chunks, want 5", len(out))
		}
	}
}

// fuseBenchHits 构造两路各 50 条的召回结果，其中一半 ID 重合，
// 模拟"向量路与词法路同时命中"（RRF 融合的典型输入）。
func fuseBenchHits() (vectorHits, lexicalHits []domain.Chunk) {
	shared := make([]uuid.UUID, 25)
	for i := range shared {
		shared[i] = uuid.New()
	}
	for i := 0; i < 50; i++ {
		id := uuid.New()
		if i < len(shared) {
			id = shared[i]
		}
		vectorHits = append(vectorHits, domain.Chunk{
			ID:        id,
			Content:   fmt.Sprintf("vector chunk %d", i),
			Score:     0.9 - float64(i)*0.01,
			PageStart: i%20 + 1,
			PageEnd:   i%20 + 1,
		})
	}
	for i := 0; i < 50; i++ {
		id := uuid.New()
		if i < len(shared) {
			id = shared[i]
		}
		lexicalHits = append(lexicalHits, domain.Chunk{
			ID:      id,
			Content: fmt.Sprintf("lexical chunk %d", i),
			Score:   float64(50-i) / 50,
		})
	}
	return vectorHits, lexicalHits
}

// BenchmarkFuseRRF 是混合检索的融合步骤（RRF k=60，按名次累加）。
// 它是纯内存排序，没有数据库与模型调用，适合作为回归护栏。
func BenchmarkFuseRRF(b *testing.B) {
	vectorHits, lexicalHits := fuseBenchHits()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := fuseRRF(vectorHits, lexicalHits, 20)
		if len(out) != 20 {
			b.Fatalf("got %d chunks, want 20", len(out))
		}
	}
}
