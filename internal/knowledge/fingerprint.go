package knowledge

import (
	"hash/fnv"
	"strings"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/tokenize"
)

// fingerprintBits 是 SimHash 的位数。64 位足够区分几十~几百份论文：
// 内容几乎相同的两份文档距离通常 ≤ 12，不相关的一般 > 25。
const fingerprintBits = 64

// duplicateThreshold 是"判为近重复"的最大汉明距离。
//
// 【为什么是 14】同一篇论文的 v1/v7、原文/扫描件正文重合度很高，实测
// 距离明显小于该值；不同论文之间通常远大于它。阈值调低会漏掉扫描件
// （OCR 错字改变 token），调高会把"同一综述的不同章节"误判成重复。
// 调这个值时请同步跑 fingerprint_test.go 与官方 v1/v7 用例。
const duplicateThreshold = 14

// ContentFingerprint 返回一段文本的 64 位 SimHash（十六进制）。
//
// 为什么用 SimHash 而不是直接算 Jaccard：SimHash 对每个文档只需存 16 个
// 十六进制字符，汉明距离可以 O(1) 比较；而真正的 Jaccard 要保留整份
// 5-gram 集合。去重只需要"是不是同一篇"，不需要精确相似度数值。
//
// token 规则复用检索用的 tokenize.Text：中文 bigram + 英文小写词，
// 这样中文论文和英文论文都能得到稳定的指纹。
func ContentFingerprint(text string) string {
	tokens := strings.Fields(tokenize.Text(text))
	if len(tokens) == 0 {
		return ""
	}
	var weights [fingerprintBits]int
	for _, tok := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		sum := h.Sum64()
		for i := 0; i < fingerprintBits; i++ {
			if sum&(1<<uint(i)) != 0 {
				weights[i]++
			} else {
				weights[i]--
			}
		}
	}
	var sum uint64
	for i := 0; i < fingerprintBits; i++ {
		if weights[i] > 0 {
			sum |= 1 << uint(i)
		}
	}
	// %016x 定长：数据库里按字符串比较/展示都不会丢前导 0。
	return strings.ToLower(strings.TrimSpace(fmtFingerprint(sum)))
}

// FingerprintOfChunks 基于一份文档的全部分块内容算指纹。
//
// 拼接分块而不是逐块算再合并：SimHash 对 token 集合成立，分块边界不应
// 影响"是不是同一篇论文"的判断；而跨块重复（页眉页脚、参考文献）在
// 整篇上更容易被平均掉。
func FingerprintOfChunks(chunks []domain.Chunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Content)
		b.WriteString("\n")
	}
	return ContentFingerprint(b.String())
}

// fmtFingerprint 把 64 位值格式化成 16 位十六进制。
func fmtFingerprint(v uint64) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		buf[i] = hex[v&0xf]
		v >>= 4
	}
	return string(buf)
}

// FingerprintDistance 返回两个十六进制指纹的汉明距离；任一为空或格式
// 不合法时返回 -1（调用方据此跳过去重判断）。
func FingerprintDistance(a, b string) int {
	if len(a) != 16 || len(b) != 16 {
		return -1
	}
	var av, bv uint64
	for i := 0; i < 16; i++ {
		da := hexVal(a[i])
		db := hexVal(b[i])
		if da < 0 || db < 0 {
			return -1
		}
		av = av<<4 | uint64(da)
		bv = bv<<4 | uint64(db)
	}
	x := av ^ bv
	dist := 0
	for x != 0 {
		x &= x - 1
		dist++
	}
	return dist
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
