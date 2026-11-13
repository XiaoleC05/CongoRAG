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

// Tokens 返回切分后的检索词序列（英文小写、中文单字 + 双字），保留重复。
//
// 规则：
//   - 拉丁字母/数字：连续串算一个词，转小写（保留 1706.03762 这类编号里的点）
//   - 汉字：每个字算一个 token；相邻两个字再算一个 bigram
//   - 其他字符（标点、数学符号）作分隔符
//
// 【为什么导出这一层，而不是让调用方各自抄一遍】两个调用方需要同一套切分
// 规则、不同的后处理：
//   - Text / Query（检索）要去重：重复的 bigram 只会让 OR 查询变长；
//   - knowledge.ContentFingerprint（SimHash）必须保留词频：去重之后，
//     "只改了某个词出现次数"的版本修订在指纹上完全看不出来。
//
// 规则只能有一份实现。过去 knowledge 包里抄了一份同样逻辑的私有函数，
// 两份各自演进时不会报错——只会让检索和去重对同一段文字给出不同的理解。
// 这份实现是从 fingerprint.go 那份搬回来合并的（v6.2.0）。
func Tokens(s string) []string {
	if s == "" {
		return nil
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
	return tokens
}

// Text 返回以空格分隔的检索词序列（英文小写、中文单字 + 双字），已去重。
//
// 去重的原因：同一段文字里重复的 bigram 对 tsquery 没有意义，还会让 OR
// 查询变长。保留首次出现顺序，方便调试时读。纯分隔符 token（"." / "-" /
// "_"）也在这里丢掉——它们是拉丁词边界被切开后留下的碎片，进 tsquery 只会
// 变成噪声。
func Text(s string) string {
	tokens := Tokens(s)
	if len(tokens) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(tokens))
	var out strings.Builder
	for _, t := range tokens {
		if t == "." || t == "-" || t == "_" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(t)
	}
	return out.String()
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
