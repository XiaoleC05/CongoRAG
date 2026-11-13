package grounding

import (
	"strings"
	"testing"
)

// benchAnswer 模拟一条"每句都带引用"的中文长回答：20 句、8 条 citation。
// 这是 grounding.Verify 在流式回答结束时要扫一遍的真实输入规模。
var benchAnswer = strings.Repeat(
	"实验结果显示该方法在长文档上更稳定 [1]。训练成本主要来自向量索引重建 [2]。"+
		"消融实验表明去重环节贡献最大 [3]。检索延迟在 200ms 以内 [4]。"+
		"该结论与文献 [5] 一致，但样本规模更小 [6]。", 4)

// BenchmarkVerify 是回答结束时的句子级引用落地检查。
// 它跑在热路径上（每条回答一次），但只做正则与 rune 扫描，不调模型。
func BenchmarkVerify(b *testing.B) {
	b.SetBytes(int64(len(benchAnswer)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := Verify(benchAnswer, 8)
		if !r.Valid || r.Sentences == 0 {
			b.Fatal("fixture should be fully grounded")
		}
	}
}
