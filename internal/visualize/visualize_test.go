package visualize

import (
	"strings"
	"testing"
)

func TestBuildBarChart(t *testing.T) {
	csv := "方法,BLEU,参数量\nTransformer,28.4,65\nBERT,24.1,110\nGPT-3,0,175"
	res, err := Build(csv, "auto", "模型对比")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Chart.Type != "bar" {
		t.Fatalf("type = %s, want bar", res.Chart.Type)
	}
	if len(res.Chart.Categories) != 3 || res.Chart.Categories[0] != "Transformer" {
		t.Fatalf("categories = %v", res.Chart.Categories)
	}
	if len(res.Chart.Series) != 2 {
		t.Fatalf("series = %d, want 2", len(res.Chart.Series))
	}
	if !strings.Contains(res.Mermaid, "xychart-beta") || !strings.Contains(res.Mermaid, "bar [") {
		t.Fatalf("mermaid output = %q", res.Mermaid)
	}
	if !strings.Contains(res.TikZ, "\\begin{tikzpicture}") || !strings.Contains(res.TikZ, "\\addplot") {
		t.Fatalf("tikz output = %q", res.TikZ)
	}
	if !strings.Contains(res.Caption, "模型对比") {
		t.Fatalf("caption = %q", res.Caption)
	}
}

func TestBuildLineChartNumericAxis(t *testing.T) {
	csv := "step,loss\n100,2.5\n200,1.8\n300,1.2"
	res, err := Build(csv, "auto", "训练损失")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Chart.Type != "line" {
		t.Fatalf("type = %s, want line", res.Chart.Type)
	}
	if !strings.Contains(res.Mermaid, "line [") {
		t.Fatalf("mermaid = %q", res.Mermaid)
	}
}

func TestBuildHeaderless(t *testing.T) {
	csv := "1,2\n3,4\n5,6"
	res, err := Build(csv, "", "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(res.Chart.Series) != 2 {
		t.Fatalf("series = %d, want 2", len(res.Chart.Series))
	}
	if res.Table.Headers[0] != "列1" {
		t.Fatalf("headers = %v", res.Table.Headers)
	}
}

func TestBuildNoNumeric(t *testing.T) {
	if _, err := Build("name,note\na,hello\nb,world", "", ""); err == nil {
		t.Fatal("expected error for CSV without numeric columns")
	}
}

func TestBuildEmpty(t *testing.T) {
	if _, err := Build("   ", "", ""); err == nil {
		t.Fatal("expected error for empty csv")
	}
}
