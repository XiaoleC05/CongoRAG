package retrieval

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// referenceShingleSet 是 shingleHashes 的"定义版"实现：直接保留 5-gram 字符串。
//
// 【为什么测试里还留一份慢实现】shingleHashes 为了性能把字符串换成了哈希，
// 那是一次"等价的表示变换"。等价性不能靠读代码保证——这里用最直白的
// 字符串集合实现当参照，对多组语料断言两边的集合大小与 Jaccard 一致。
// 它只在测试里跑，不影响生产路径的性能。
func referenceShingleSet(text string) map[string]struct{} {
	var b strings.Builder
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

func referenceJaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func TestShingleHashes_MatchesStringDefinition(t *testing.T) {
	texts := []string{
		"",
		"   ",
		"abc",
		"abcde",
		"aaaaa",
		"The Transformer relies entirely on self-attention.",
		"我们提出一种新的简单网络架构 Transformer，完全基于注意力机制。",
		"Attention is all you need.\n\n我们提出一种新的简单网络架构 Transformer。",
	}
	for _, text := range texts {
		got := shingleHashes(text)
		wantSet := referenceShingleSet(text)
		require.Len(t, got, len(wantSet), "shingle 数量应与字符串定义一致: %q", text)
		// 有序去重的断言：严格递增。
		for i := 1; i < len(got); i++ {
			require.Less(t, got[i-1], got[i], "哈希切片必须严格递增（已排序去重）")
		}
	}
}

func TestShingleHashes_JaccardMatchesReference(t *testing.T) {
	pairs := [][2]string{
		{
			"The Transformer is the first transduction model relying entirely on self-attention.",
			"The Transformer is the first transduction model relying entirely on self attention", // 去标点后相同
		},
		{
			"Attention is all you need.",
			"Completely different text about convolutional layers and pooling operations.",
		},
		{
			"我们提出一种新的简单网络架构 Transformer，完全基于注意力机制。",
			"我们提出一种新的简单网络架构Transformer完全基于注意力机制",
		},
	}
	for _, pair := range pairs {
		a, b := shingleHashes(pair[0]), shingleHashes(pair[1])
		got := jaccard(a, b)
		want := referenceJaccard(referenceShingleSet(pair[0]), referenceShingleSet(pair[1]))
		require.InDelta(t, want, got, 1e-9)
	}
}

func TestDedupeNearDuplicates_KeepsOrderAndIDs(t *testing.T) {
	// 固定语料上的行为契约：完全相同的块只留第一条，不同的块按原顺序保留。
	base := "Attention mechanisms have become a core building block of modern sequence models."
	in := []domain.Chunk{
		chunkWith(base),
		chunkWith("检索增强生成把外部证据与参数化记忆结合起来，降低事实性错误。"),
		chunkWith(base + " "), // 末尾多一个空格，归一化后与第一条相同
		chunkWith("引用图谱用 PageRank 找出领域里的基石论文。"),
	}
	out := dedupeNearDuplicates(in, 10)
	require.Len(t, out, 3)
	require.Equal(t, []string{in[0].ID.String(), in[1].ID.String(), in[3].ID.String()},
		[]string{out[0].ID.String(), out[1].ID.String(), out[2].ID.String()})
}

// dedupeNearDuplicatesReference 是 v6.1.0 及以前的实现（字符串 shingle 集合）。
//
// 【为什么测试里保留旧实现】性能改造要能被复现地证明"快了"，而不是只在
// commit message 里写一句快了。这里把旧实现当对照跑同一份语料，任何人
// go test -bench Dedupe 都能看到新旧差值；生产路径只走新实现。
func dedupeNearDuplicatesReference(chunks []domain.Chunk, limit int) []domain.Chunk {
	if limit <= 0 || len(chunks) == 0 {
		return nil
	}
	kept := make([]domain.Chunk, 0, limit)
	keptSets := make([]map[string]struct{}, 0, limit)
	for _, c := range chunks {
		set := referenceShingleSet(c.Content)
		if len(set) == 0 {
			kept = append(kept, c)
			keptSets = append(keptSets, set)
			if len(kept) == limit {
				break
			}
			continue
		}
		dup := false
		for _, prev := range keptSets {
			if len(prev) > 0 && referenceJaccard(set, prev) >= dedupeThreshold {
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

// BenchmarkDedupeNearDuplicates_Top20 是"去重后仍要凑 20 条"的场景
// （多篇对比 / 写作 Copilot 的 topK 比问答大）：每保留一条都要和已有的
// 20 条比一次，是 O(limit×candidates) 的主要成本来源。
func BenchmarkDedupeNearDuplicates_Top20(b *testing.B) {
	chunks := dedupeBenchChunks()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := dedupeNearDuplicates(chunks, 20)
		if len(out) != 20 {
			b.Fatalf("got %d chunks, want 20", len(out))
		}
	}
}

// BenchmarkDedupeNearDuplicates_LegacyReference 用旧实现跑同一份语料，
// 作为新实现的对照基线（只在测试里存在）。
func BenchmarkDedupeNearDuplicates_LegacyReference(b *testing.B) {
	chunks := dedupeBenchChunks()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := dedupeNearDuplicatesReference(chunks, 5)
		if len(out) != 5 {
			b.Fatalf("got %d chunks, want 5", len(out))
		}
	}
}

// TestDedupeNearDuplicates_MatchesReferenceImplementation 是"整函数等价"的
// 表驱动测试：同一批输入分别喂给新实现与旧实现（字符串 shingle 版），
// 断言两边保留的分块 **ID 序列完全相同**。
//
// 【为什么需要它】前面两条等价测试只证明了 shingle 表示与 Jaccard 一致，
// 但去重函数还有贪心顺序、提前退出、空内容保留、limit 边界这些行为；
// 只测底层表示的话，"提前退出条件写错"这类缺陷不会被发现。
func TestDedupeNearDuplicates_MatchesReferenceImplementation(t *testing.T) {
	ocrVariant := func(s string) string { return strings.ReplaceAll(s, " ", "") + " OCRnoise" }
	distinct := func(seed string) string {
		return strings.Repeat(seed+" ", 6)
	}
	fixtures := []struct {
		name   string
		chunks []domain.Chunk
		limits []int
	}{
		{
			name:   "空输入",
			chunks: nil,
			limits: []int{0, 1, 5},
		},
		{
			name:   "空内容与纯符号",
			chunks: []domain.Chunk{chunkWith(""), chunkWith("   "), chunkWith("，。！"), chunkWith(distinct("alpha beta gamma"))},
			limits: []int{1, 3, 10},
		},
		{
			name:   "短文本（不足一个 shingle）",
			chunks: []domain.Chunk{chunkWith("ab"), chunkWith("abc"), chunkWith("abcd"), chunkWith(distinct("delta"))},
			limits: []int{2, 10},
		},
		{
			name: "OCR 变体",
			chunks: []domain.Chunk{
				chunkWith(distinct("the Transformer relies entirely on self attention")),
				chunkWith(ocrVariant(distinct("the Transformer relies entirely on self attention"))),
				chunkWith(distinct("completely different content about pooling layers")),
			},
			limits: []int{1, 2, 10},
		},
		{
			name: "多处近重复 + limit 截断",
			chunks: []domain.Chunk{
				chunkWith(distinct("first group about attention mechanisms")),
				chunkWith(distinct("second group about retrieval augmented generation")),
				chunkWith(distinct("first group about attention mechanisms")),
				chunkWith(distinct("third group about citation grounding")),
				chunkWith(distinct("second group about retrieval augmented generation")),
				chunkWith(distinct("fourth group about long document parsing")),
			},
			limits: []int{1, 2, 3, 10, 100},
		},
		{
			name: "中英混排",
			chunks: []domain.Chunk{
				chunkWith(distinct("我们提出一种新的简单网络架构 Transformer 完全基于注意力机制")),
				chunkWith(distinct("我们提出一种新的简单网络架构 Transformer 完全基于注意力机制")),
				chunkWith(distinct("检索增强生成把外部证据与参数化记忆结合起来")),
				chunkWith(distinct("引用图谱用 PageRank 找出领域里的基石论文")),
			},
			limits: []int{2, 5},
		},
	}
	for _, fx := range fixtures {
		for _, limit := range fx.limits {
			t.Run(fmt.Sprintf("%s/limit=%d", fx.name, limit), func(t *testing.T) {
				got := ids(dedupeNearDuplicates(fx.chunks, limit))
				want := ids(dedupeNearDuplicatesReference(fx.chunks, limit))
				require.Equal(t, want, got, "新实现与旧实现保留的分块序列必须一致")
			})
		}
	}
}

// ids 把分块序列压成 ID 字符串，失败信息里能直接看出"哪一条被多留/少留"。
func ids(chunks []domain.Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.ID.String()
	}
	return out
}

// TestDedupeNearDuplicates_MatchesReferenceNearThreshold 把"等价性"从
// "相似度数值一致"推进到"去重决策一致"：构造相似度从低到高扫过 0.85 阈值的
// 成对语料，断言新旧实现每一步保留的 ID 序列完全相同。
//
// 【为什么单靠 Jaccard 数值对照不够】阈值比较是 >=，差一个浮点 ulp、或者
// shingle 集合大小算错一个，都会让某个分块在阈值附近被多留/少留一条；
// 而数值 InDelta 用的是 1e-9 的容差，恰好会把这类边界差异当成"一致"。
func TestDedupeNearDuplicates_MatchesReferenceNearThreshold(t *testing.T) {
	const n = 200
	base := make([]string, n)
	for i := range base {
		base[i] = fmt.Sprintf("w%03d", i)
	}
	baseText := strings.Join(base, " ")
	for keep := 55; keep <= 98; keep += 1 {
		shared := n * keep / 100
		var b strings.Builder
		for i := 0; i < n; i++ {
			if i < shared {
				b.WriteString(base[i])
			} else {
				b.WriteString(fmt.Sprintf("x%03d", i))
			}
			b.WriteString(" ")
		}
		chunks := []domain.Chunk{
			chunkWith(baseText),
			chunkWith(b.String()),
			chunkWith(strings.Repeat("p q r s t u v ", 40)),
		}
		for _, limit := range []int{1, 2, 3} {
			got := ids(dedupeNearDuplicates(chunks, limit))
			want := ids(dedupeNearDuplicatesReference(chunks, limit))
			require.Equal(t, want, got, "keep=%d limit=%d 时新旧实现的去重决策不一致", keep, limit)
		}
	}
}
