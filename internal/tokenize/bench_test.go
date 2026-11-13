package tokenize

import (
	"strings"
	"testing"
)

// 基准用的固定语料：中英混排，模拟"论文片段 + 中文提问"的真实输入形状。
// 语料写死而不是随机生成，是为了让不同机器/不同提交之间的数字可比。
var (
	benchParagraph = strings.Repeat("注意力机制（Attention）让模型在处理长序列时能够直接对齐任意两个位置。"+
		"Transformer 用多头自注意力替代了循环结构，训练并行度显著提升。"+
		"但长文档检索仍然受限于分块粒度与词法切分的质量。", 6)

	benchQuery = "Transformer 的注意力机制为什么能缓解长程依赖？"
)

// BenchmarkText_Paragraph 是 worker 每个分块都要走的一条路径：
// 把正文切成 tsquery 词项（英文小写词 + 中文 bigram，去重）。
func BenchmarkText_Paragraph(b *testing.B) {
	b.SetBytes(int64(len(benchParagraph)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if Text(benchParagraph) == "" {
			b.Fatal("empty token stream")
		}
	}
}

// BenchmarkText_Query 是检索入口的一条路径：用户提问通常几十个字，
// 调用频率高但单次很便宜，数字用来确认它没有退化成"每问一次都重扫大文本"。
func BenchmarkText_Query(b *testing.B) {
	b.SetBytes(int64(len(benchQuery)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if Text(benchQuery) == "" {
			b.Fatal("empty token stream")
		}
	}
}

// BenchmarkTokens_Paragraph 是不去重的切分：SimHash 指纹需要词频，
// 和 Text 共用同一套规则但多保留重复项。
func BenchmarkTokens_Paragraph(b *testing.B) {
	b.SetBytes(int64(len(benchParagraph)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if len(Tokens(benchParagraph)) == 0 {
			b.Fatal("empty token stream")
		}
	}
}

// BenchmarkQuery 把词项拼成 to_tsquery('simple', ...) 的表达式（| 连接）。
func BenchmarkQuery(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if Query(benchQuery) == "" {
			b.Fatal("empty query expression")
		}
	}
}
