// Package tokenize 把一段文本切成"同时适合中文和英文"的检索词。
//
// 【为什么需要自己切】Postgres 的 to_tsvector('simple', ...) 只按非字母
// 字符切词：英文正好（空格分词），中文整段会被当成一个巨大的词——于是
// "过拟合" 这个查询永远匹配不到 "过拟合是模型在训练集上表现好……" 这句，
// 因为后者被切成了一个几百字的词。zhparser/pg_jieba 这类中文分词扩展
// 在目标机器上不一定装得上，所以这里用「CJK 单字 + 双字 bigram」的
// 近似切分：查询和文档用同一套规则，中文子串匹配就能命中。
//
// 【代价与取舍】bigram 会放大索引体积（每个汉字进两个 token），也会带来
// 一定误报；对"科研问答"这种短查询 + 精确术语的场景，召回比误报重要。
// 真正上线要换成正经中文分词，这一点写进技术文档的「已知限制」。
package tokenize

import (
	"strings"
	"unicode"
)

// Text 返回以空格分隔的检索词序列（英文小写、中文单字 + 双字）。
//
// 规则：
//   - 拉丁字母/数字：连续串算一个词，转小写（保留 1706.03762 这类编号里的点）
//   - 汉字：每个字算一个 token；相邻两个字再算一个 bigram
//   - 其他字符（标点、数学符号）作分隔符
func Text(s string) string {
	if s == "" {
		return ""
	}
	var tokens []string
	var latin strings.Builder
	var han []rune

	flushLatin := func() {
		if latin.Len() > 0 {
			tokens = append(tokens, strings.ToLower(latin.String()))
			latin.Reset()
		}
	}
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		// 【只出 bigram，不出单字】单字（的/了/多/用）在中文里出现频率极高，
		// OR 进 tsquery 后几乎每个分块都命中，词法一路的排序就变成噪声。
		// 单字只在"这个汉字前后都是非汉字"（孤立单字）时保留——那种情况下
		// 它是真实查询词（例如"β 与 α"里的汉字符号附近）。
		if len(han) == 1 {
			tokens = append(tokens, string(han[0]))
		} else {
			for i := 0; i+1 < len(han); i++ {
				tokens = append(tokens, string(han[i:i+2]))
			}
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			flushLatin()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_':
			flushHan()
			latin.WriteRune(r)
		default:
			flushLatin()
			flushHan()
		}
	}
	flushLatin()
	flushHan()

	// 去重：同一段文字里重复的 bigram 对 tsquery 没有意义，还会让 OR
	// 查询变长。保留首次出现顺序，方便调试时读。
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t == "." || t == "-" || t == "_" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// Query 把用户查询变成 Postgres to_tsquery('simple', ...) 能吃的表达式
// （用 | 连接，任意词命中即召回）。
//
// 【为什么是 OR 不是 AND】AND 会把"中文 bigram + 英文词"混排的查询几乎
// 全部打死（文档里不一定同时出现所有 bigram）。召回交给 OR，排序交给
// ts_rank_cd + 后面的 RRF。
func Query(s string) string {
	tokens := strings.Fields(Text(s))
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " | ")
}
