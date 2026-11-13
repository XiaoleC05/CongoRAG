package visualize

import (
	"fmt"
	"strings"
	"testing"
)

// benchCSV 是一份 500 行 × 4 列的表格，规模对应论文里一张中等大小的实验结果表。
func benchCSV() string {
	var b strings.Builder
	b.WriteString("model,accuracy,latency_ms,memory_mb\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "model-%03d,%.4f,%d,%d\n", i, 0.7+float64(i%1000)/10000, 40+i%160, 512+(i%8)*128)
	}
	return b.String()
}

// BenchmarkBuild 是 CSV 可视化端点的主路径：解析表格 → 选列/选图 → 生成
// Mermaid/TikZ/Matplotlib 代码与 Figure Caption（纯字符串组装，不调模型）。
func BenchmarkBuild(b *testing.B) {
	csv := benchCSV()
	b.SetBytes(int64(len(csv)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := Build(csv, "auto", "Benchmark Table")
		if err != nil || res == nil || res.Mermaid == "" {
			b.Fatalf("build failed: %v", err)
		}
	}
}
