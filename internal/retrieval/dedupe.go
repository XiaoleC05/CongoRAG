package retrieval

import (
	"strings"
	"unicode"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// 近重复判定参数（参赛版 M2 的"语义去重"）。
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
	keptSets := make([]map[string]struct{}, 0, limit)
	for _, c := range chunks {
		set := shingleSet(c.Content)
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
			if len(prev) > 0 && jaccard(set, prev) >= dedupeThreshold {
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

// shingleSet 把文本归一化成字符 5-gram 集合。
//
// 归一化：小写、只保留字母/数字（去掉空格、标点、换行）——OCR 与排版差异
// 大多落在这些字符上，去掉它们能让同一段文字的不同版对齐。
func shingleSet(text string) map[string]struct{} {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	runes := []rune(b.String())
	if len(runes) < dedupeShingleRunes {
		return map[string]struct{}{}
	}
	set := make(map[string]struct{}, len(runes)-dedupeShingleRunes+1)
	for i := 0; i+dedupeShingleRunes <= len(runes); i++ {
		set[string(runes[i:i+dedupeShingleRunes])] = struct{}{}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	// 遍历较小的一侧，减少比较次数。
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for k := range small {
		if _, ok := large[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
