package tokenize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestText_MixedChineseAndEnglish(t *testing.T) {
	got := Text("Attention 机制中的过拟合是什么？")
	require.Contains(t, got, "attention")
	require.Contains(t, got, "机")
	require.Contains(t, got, "机制")
	// 三字词"过拟合"拆成两个 bigram：过拟 / 拟合（查询与文档同一套规则，
	// 所以这两个 token 的 OR 命中即可）。单字不出现——单字是噪声源。
	require.Contains(t, got, "过拟")
	require.Contains(t, got, "拟合")
	require.NotContains(t, strings.Fields(got), "机")
	// 标点不能变成 token
	require.NotContains(t, strings.Fields(got), "？")
}

func TestText_NumbersAndDots(t *testing.T) {
	got := Text("arXiv:1706.03762v7 与 GPT-3")
	require.Contains(t, got, "arxiv")
	require.Contains(t, got, "1706.03762v7")
	require.Contains(t, got, "gpt-3")
}

func TestText_Dedupes(t *testing.T) {
	got := Text("过拟合 过拟合")
	n := 0
	for _, f := range strings.Fields(got) {
		if f == "过拟" {
			n++
		}
	}
	require.Equal(t, 1, n, "重复出现的 bigram 只保留一次")
}

func TestText_Empty(t *testing.T) {
	require.Equal(t, "", Text(""))
	require.Equal(t, "", Text("，。！？ ")) // 纯标点
}

func TestQuery_ORJoinedAndSafe(t *testing.T) {
	q := Query("深度 学习")
	require.Equal(t, "深度 | 学习", q)
	// 空查询返回空串，调用方据此跳过词法检索
	require.Equal(t, "", Query("，。"))
	// 表达式里不能出现 tsquery 的元字符
	require.NotContains(t, q, "&")
	require.NotContains(t, q, "!")
}
