package retrieval

import (
	"sort"
	"strings"
	"unicode"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// 近重复判定参数（科研助手 M2 的"语义去重"）。
//
// 【为什么需要它】同一篇论文常常以多个版本进入知识库（PDF 原文 + 扫描件、
// v1 + v7、同一篇的英文与中文版）。这些版本的内容高度重合，混合检索会
// 让它们在 top-k 里占掉多个名额：读者看到的"三个来源"其实是一篇论文，
// 引用列表的多样性被稀释，模型也拿不到真正互补的证据。
//
// 【为什么用字符 shingle 的 Jaccard，而不是 embedding 相似度】去重发生在
// 检索主路径上，每多一次模型调用就多几百毫秒；而"同文不同版"的字符重合度
// 极高（OCR 只错几个字），shingle 足够。embedding 相似度留给更难的"语义
// 近义但表述不同"场景——那是 rerank/MMR 的工作，见技术文档已知限制。
const (
	dedupeShingleRunes = 5
	// 0.85 是"同一段文字的不同版本"与"相关但不同的段落"之间的经验分界：
	// OCR 噪声通常只让重合度掉几个百分点，而不同段落的 5-gram 重合度
	// 远低于此。阈值写进技术文档，调整时要用评测集复测。
	dedupeThreshold = 0.85
)

// dedupeNearDuplicates 按顺序保留分块，跳过高相似度的近重复项，凑满 limit 为止。
//
// 【为什么按顺序贪心】输入已经按 RRF 分数降序，贪心保留"排名更高、内容
// 尚未出现"的块即可：不改变原有排序语义，只去掉冗余。
func dedupeNearDuplicates(chunks []domain.Chunk, limit int) []domain.Chunk {
	if limit <= 0 || len(chunks) == 0 {
		return nil
	}
	kept := make([]domain.Chunk, 0, limit)
	keptSets := make([][]uint64, 0, limit)
	for _, c := range chunks {
		set := shingleHashes(c.Content)
		if len(set) == 0 {
			// 空/纯符号分块：保留，但不参与去重比较（没有可比的内容）。
			kept = append(kept, c)
			keptSets = append(keptSets, set)
			if len(kept) == limit {
				break
			}
			continue
		}
		dup := false
		for _, prev := range keptSets {
			if len(prev) > 0 && jaccard(prev, set) >= dedupeThreshold {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		kept = append(kept, c)
		keptSets = append(keptSets, set)
		if len(kept) == limit {
			break
		}
	}
	return kept
}

// shingleHashes 把文本归一化成 5-gram 的【排序去重后的 64 位哈希】切片。
//
// 归一化：小写、只保留字母/数字（去掉空格、标点、换行）——OCR 与排版差异
// 大多落在这些字符上，去掉它们能让同一段文字的不同版对齐。
//
// 【为什么存哈希而不是字符串】v6.2.0 的性能改造。旧实现给每个 5-gram 建一个
// 新字符串（一块 ~1200 字的中文分块 → ~1200 个短字符串）再塞进 map，一次
// top-50 去重要分配十几万个对象；改成"每窗口算一个 uint64、排序后去重"之后，
// 每个分块只剩一个 []uint64，集合交并也退化成两个有序切片的双指针合并。
// 对 0.85 这种粗粒度阈值来说，64 位哈希碰撞带来的误差（同一次去重里量级
// 约 1e-10）远小于阈值本身的不确定性，而分配和 GC 的收益是数量级的。
func shingleHashes(text string) []uint64 {
	runes := normalizedRunes(text)
	n := len(runes)
	if n < dedupeShingleRunes {
		return nil
	}
	out := make([]uint64, 0, n-dedupeShingleRunes+1)
	for i := 0; i+dedupeShingleRunes <= n; i++ {
		out = append(out, hashShingle(runes[i:i+dedupeShingleRunes]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	// 去重：同一段文字里重复的 5-gram 对集合没有意义，留着只会让后面的
	// 双指针合并多走几轮。
	w := 1
	for r := 1; r < len(out); r++ {
		if out[r] != out[w-1] {
			out[w] = out[r]
			w++
		}
	}
	return out[:w]
}

// normalizedRunes 返回只含字母/数字的小写 rune 序列。
func normalizedRunes(text string) []rune {
	out := make([]rune, 0, len(text))
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, r)
		}
	}
	return out
}

// hashShingle 对固定长度的 rune 窗口做 FNV-1a。
//
// 窗口长度固定为 5，循环展开没有收益，直接相乘即可；FNV 的分布对
// "只差一个字符"的窗口足够散——那正是 OCR 变体最常见的情况。
func hashShingle(window []rune) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, r := range window {
		h ^= uint64(r)
		h *= prime64
	}
	return h
}

// jaccard 计算两个【有序去重】哈希切片的 Jaccard 相似度。
//
// 双指针合并，O(len(a)+len(b))，不分配。
func jaccard(a, b []uint64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			inter++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
