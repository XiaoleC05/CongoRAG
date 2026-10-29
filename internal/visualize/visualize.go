// Package visualize 把用户上传的 CSV 变成"可直接用于论文的图表描述"（参赛版 M6）。
//
// 【为什么在 Go 里做而不用 Python/pandas】图表需求只有三类（折线/柱状/雷达）、
// 数据规模是论文表格级（几十行），解析 + 选列 + 生成 Mermaid/TikZ 代码
// 用标准库 encoding/csv 就够；再起一个 Python 服务只为画图不值得。
// 真正的"画图"交给前端 SVG（无需图表库）和学术工具（Mermaid/TikZ 代码）。
package visualize

import (
	"encoding/csv"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// 输入上限与赛题场景匹配：论文表格几十行、十几列；超过说明不是"图表数据"。
const (
	maxRows = 2000
	maxCols = 30
)

// Table 是解析后的原始表格（保留字符串，前端按原样展示）。
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// Series 是一条数值列（图例的一项）。
type Series struct {
	Name   string    `json:"name"`
	Values []float64 `json:"values"`
}

// Chart 是前端画图 / 生成代码所需的最小图表描述。
type Chart struct {
	Type       string   `json:"type"` // line | bar | radar
	Title      string   `json:"title"`
	YLabel     string   `json:"yLabel"`
	Categories []string `json:"categories"`
	Series     []Series `json:"series"`
}

// Result 是一次可视化生成的全部产物。
type Result struct {
	Table   Table  `json:"table"`
	Chart   Chart  `json:"chart"`
	Mermaid string `json:"mermaid"`
	TikZ    string `json:"tikz"`
	Caption string `json:"caption"`
}

// Build 解析 CSV 并生成图表描述、Mermaid/TikZ 代码与兜底图注。
//
// caption 的最终版由调用方用 LLM 润色（学术图注需要读得懂数据），这里的
// FallbackCaption 保证"模型不可用也有可用输出"。
func Build(csvText, chartType, title string) (*Result, error) {
	if strings.TrimSpace(csvText) == "" {
		return nil, fmt.Errorf("CSV 内容为空: %w", platform.ErrInvalid)
	}
	if len(csvText) > 2<<20 {
		return nil, fmt.Errorf("CSV 超过 2MB，请只保留要画图的表格: %w", platform.ErrInvalid)
	}
	headers, rows, err := parseCSV(csvText)
	if err != nil {
		return nil, err
	}
	table := Table{Headers: headers, Rows: rows}

	categories, numericCols, err := inferColumns(headers, rows)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = "数据图表"
	}
	chart := Chart{
		Type:       normalizeChartType(chartType, categories),
		Title:      title,
		YLabel:     "值",
		Categories: categories,
	}
	for _, col := range numericCols {
		series := Series{Name: headers[col], Values: make([]float64, 0, len(rows))}
		for _, row := range rows {
			v, ok := parseFloat(row[col])
			if !ok {
				continue
			}
			series.Values = append(series.Values, v)
		}
		if len(series.Values) > 0 {
			chart.Series = append(chart.Series, series)
		}
	}
	if len(chart.Series) == 0 {
		return nil, fmt.Errorf("CSV 里没有可绘制的数值列: %w", platform.ErrInvalid)
	}

	return &Result{
		Table:   table,
		Chart:   chart,
		Mermaid: mermaid(chart),
		TikZ:    tikz(chart),
		Caption: FallbackCaption(chart),
	}, nil
}

// parseCSV 读表并把"首行是否是表头"的猜测做掉。
func parseCSV(text string) ([]string, [][]string, error) {
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\ufeff")))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CSV 失败: %w", platform.ErrInvalid)
	}
	if len(records) == 0 {
		return nil, nil, fmt.Errorf("CSV 没有数据行: %w", platform.ErrInvalid)
	}
	if len(records) > maxRows+1 {
		return nil, nil, fmt.Errorf("CSV 超过 %d 行，请先做汇总: %w", maxRows, platform.ErrInvalid)
	}
	if len(records[0]) > maxCols {
		return nil, nil, fmt.Errorf("CSV 超过 %d 列: %w", maxCols, platform.ErrInvalid)
	}

	first := records[0]
	hasHeader := true
	for _, cell := range first {
		if _, ok := parseFloat(cell); ok {
			hasHeader = false
			break
		}
	}
	headers := make([]string, len(first))
	rows := records
	if hasHeader {
		for i, h := range first {
			h = strings.TrimSpace(h)
			if h == "" {
				h = fmt.Sprintf("列%d", i+1)
			}
			headers[i] = h
		}
		rows = records[1:]
	} else {
		for i := range first {
			headers[i] = fmt.Sprintf("列%d", i+1)
		}
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("CSV 只有表头没有数据行: %w", platform.ErrInvalid)
	}
	// 短行补空，长行截断：CSV 尾逗号/缺列很常见，不该因此整张表失败。
	for i, row := range rows {
		if len(row) < len(headers) {
			padded := make([]string, len(headers))
			copy(padded, row)
			rows[i] = padded
		} else if len(row) > len(headers) {
			rows[i] = row[:len(headers)]
		}
	}
	return headers, rows, nil
}

// inferColumns 选类别列（第一个非数值列）与数值列。
func inferColumns(headers []string, rows [][]string) ([]string, []int, error) {
	categories := make([]string, len(rows))
	numCols := make([]int, 0, len(headers))
	catCol := -1
	for col := range headers {
		numeric := 0
		for _, row := range rows {
			if _, ok := parseFloat(row[col]); ok {
				numeric++
			}
		}
		if numeric == len(rows) {
			numCols = append(numCols, col)
		} else if catCol == -1 && numeric == 0 {
			catCol = col
		}
	}
	if len(numCols) == 0 {
		return nil, nil, fmt.Errorf("CSV 里没有数值列: %w", platform.ErrInvalid)
	}
	if catCol >= 0 {
		for i, row := range rows {
			categories[i] = strings.TrimSpace(row[catCol])
		}
	} else {
		for i := range rows {
			categories[i] = strconv.Itoa(i + 1)
		}
	}
	return categories, numCols, nil
}

// normalizeChartType 把 auto 解析成 line/bar：横轴是有序数字（如年份/迭代步）
// 时折线更能表达趋势，否则柱状更稳。雷达图要求至少 3 个类别。
func normalizeChartType(kind string, categories []string) string {
	switch kind {
	case "line", "bar", "radar":
		if kind == "radar" && len(categories) < 3 {
			return "bar"
		}
		return kind
	}
	numericAxis := true
	prev := math.Inf(-1)
	for _, c := range categories {
		v, err := strconv.ParseFloat(strings.TrimSpace(c), 64)
		if err != nil {
			numericAxis = false
			break
		}
		if v < prev {
			numericAxis = false
			break
		}
		prev = v
	}
	if numericAxis {
		return "line"
	}
	return "bar"
}

// mermaid 生成 Mermaid xychart-beta 代码（多序列时每条序列一段）。
func mermaid(chart Chart) string {
	var b strings.Builder
	for _, s := range chart.Series {
		max := 0.0
		for _, v := range s.Values {
			if v > max {
				max = v
			}
		}
		fmt.Fprintf(&b, "xychart-beta\n    title \"%s - %s\"\n", escapeMermaid(chart.Title), escapeMermaid(s.Name))
		b.WriteString("    x-axis [")
		for i, c := range chart.Categories {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("\"" + escapeMermaid(c) + "\"")
		}
		b.WriteString("]\n")
		fmt.Fprintf(&b, "    y-axis \"%s\" 0 --> %.2f\n", escapeMermaid(chart.YLabel), max*1.1)
		kind := "bar"
		if chart.Type == "line" {
			kind = "line"
		}
		b.WriteString("    " + kind + " [")
		for i, v := range s.Values {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// tikz 生成 pgfplots 柱状图/折线图代码（多序列用 legend 区分）。
func tikz(chart Chart) string {
	var b strings.Builder
	b.WriteString("\\begin{tikzpicture}\n\\begin{axis}[\n")
	b.WriteString("  ybar, width=0.8\\linewidth, height=6cm,\n")
	fmt.Fprintf(&b, "  title={%s}, ylabel={%s},\n", escapeTeX(chart.Title), escapeTeX(chart.YLabel))
	b.WriteString("  symbolic x coords={")
	for i, c := range chart.Categories {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(escapeTeX(c))
	}
	b.WriteString("}, xtick=data, legend pos=north west,\n]\n")
	for _, s := range chart.Series {
		fmt.Fprintf(&b, "\\addplot coordinates {")
		for i, v := range s.Values {
			if i >= len(chart.Categories) {
				break
			}
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "(%s,%.4g)", escapeTeX(chart.Categories[i]), v)
		}
		b.WriteString("};\n")
		fmt.Fprintf(&b, "\\addlegendentry{%s}\n", escapeTeX(s.Name))
	}
	b.WriteString("\\end{axis}\n\\end{tikzpicture}")
	return b.String()
}

// FallbackCaption 是模型不可用时的图注：包含图表类型、变量与极值信息。
func FallbackCaption(chart Chart) string {
	if len(chart.Series) == 0 {
		return ""
	}
	s := chart.Series[0]
	maxIdx, minIdx := 0, 0
	for i, v := range s.Values {
		if v > s.Values[maxIdx] {
			maxIdx = i
		}
		if v < s.Values[minIdx] {
			minIdx = i
		}
	}
	maxCat, minCat := "", ""
	if maxIdx < len(chart.Categories) {
		maxCat = chart.Categories[maxIdx]
	}
	if minIdx < len(chart.Categories) {
		minCat = chart.Categories[minIdx]
	}
	kind := map[string]string{"line": "折线图", "bar": "柱状图", "radar": "雷达图"}[chart.Type]
	if kind == "" {
		kind = "图表"
	}
	return fmt.Sprintf("%s：%s。横轴为类别（%s 至 %s），纵轴为%s；%s 在 %s 处达到最大值 %.4g，在 %s 处为最小值 %.4g%s。",
		kind, chart.Title, firstOrEmpty(chart.Categories), lastOrEmpty(chart.Categories), chart.YLabel,
		s.Name, maxCat, s.Values[maxIdx], minCat, s.Values[minIdx],
		extraSeries(chart.Series))
}

func extraSeries(series []Series) string {
	if len(series) <= 1 {
		return ""
	}
	names := make([]string, 0, len(series)-1)
	for _, s := range series[1:] {
		names = append(names, s.Name)
	}
	return "；同图还包含 " + strings.Join(names, "、")
}

func firstOrEmpty(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
func lastOrEmpty(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func escapeMermaid(s string) string {
	return strings.ReplaceAll(s, "\"", "'")
}
func escapeTeX(s string) string {
	r := strings.NewReplacer("_", "\\_", "&", "\\&", "%", "\\%", "#", "\\#", "$", "\\$", "{", "\\{", "}", "\\}")
	return r.Replace(s)
}
