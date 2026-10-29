// Package grounding 做回答的"引用落地"检查：模型说出的每一句，是否都能
// 指回这次检索给的某一条 citation。
//
// 【它解决的是"看起来有引用"和"引用真的对得上"之间的差距】提示词里写
// "每个事实句都要带 [n]"只是要求，模型可能整段不带、带一个不存在的编号、
// 或者只在最后一句糊一个 [1]。这些都是幻觉的变体，靠肉眼在长回答里
// 很难逐句核对。这里用纯规则做第一道闸：句子级扫描 [n]、检查编号是否在
// citations 范围内，产出可展示、可评测的覆盖率指标。
//
// 【为什么不做 NLI/语义一致性判断】那需要又一次模型调用（延迟 + 成本），
// 而且在流式回答已经吐给用户之后无法回撤。这里的定位是"可观测的闸门"：
// 覆盖率低就明确告诉用户"部分结论未溯源"，把不确定性暴露出来，而不是
// 假装验证过。语义级校验是后续工作（写进 Roadmap）。
package grounding

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// citationRef 匹配回答里的 [n] 形式的引用标记（n 为 1~3 位数字）。
var citationRef = regexp.MustCompile(`\[([0-9]{1,3})\]`)

// Report 是一次引用落地检查的结果。字段都进 SSE 事件，前端直接显示。
type Report struct {
	// Sentences 是切出来的句子总数（过滤掉过短片段，见 splitSentences）。
	Sentences int `json:"sentences"`
	// Cited 是至少带一个合法 [n] 的句子数。
	Cited int `json:"cited"`
	// Unsupported 是没有任何合法引用的句子数（= Sentences - Cited）。
	Unsupported int `json:"unsupported"`
	// Refs 是回答里出现的 [n] 总数；ValidRefs 是其中编号在 citations
	// 范围内的数量。模型编造 [7] 但只给了 5 条 citation 时，两者不相等。
	Refs      int `json:"refs"`
	ValidRefs int `json:"validRefs"`
	// Valid 是"这份回答是否可以标记为已溯源"：非空回答且每句都有合法引用，
	// 且至少出现过一个引用。空回答（模型什么都没说）不算 valid。
	Valid bool `json:"valid"`
}

// Verify 扫描回答文本，返回检查报告。
//
// citations 是这次随流下发的引用条数（domain.Citation 的数量）——编号从 1
// 开始，所以合法范围是 [1, citations]。
func Verify(answer string, citations int) Report {
	sentences := splitSentences(answer)
	report := Report{Sentences: len(sentences)}
	for _, s := range sentences {
		refs := citationRef.FindAllStringSubmatch(s, -1)
		report.Refs += len(refs)
		cited := false
		for _, m := range refs {
			n := 0
			for _, r := range m[1] {
				n = n*10 + int(r-'0')
			}
			if n >= 1 && n <= citations {
				report.ValidRefs++
				cited = true
			}
		}
		if cited {
			report.Cited++
		} else {
			report.Unsupported++
		}
	}
	report.Valid = len(sentences) > 0 && report.Unsupported == 0 && report.Cited > 0
	return report
}

// splitSentences 按中英文句末标点与换行切句，并丢掉过短的碎片。
//
// 【为什么丢短片段】模型常写"总结："这种引导短语、列表符号、单个标点。
// 把它们算成"没引用的句子"会让覆盖率永远达不到 100%，指标就失去意义。
// 阈值 6 个 rune 大致对应"半句话"；真正的事实句都长于此。
func splitSentences(answer string) []string {
	var out []string
	var cur strings.Builder

	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if utf8.RuneCountInString(s) >= 6 {
			out = append(out, s)
		}
	}

	for _, r := range answer {
		switch r {
		case '。', '！', '？', '!', '?', ';', '；':
			cur.WriteRune(r)
			flush()
		case '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}
