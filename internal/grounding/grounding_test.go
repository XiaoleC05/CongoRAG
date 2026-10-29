package grounding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerify_AllSentencesCited(t *testing.T) {
	answer := "Transformer 使用多头注意力机制[1]。训练使用 8 块 P100 GPU[2]。"
	r := Verify(answer, 2)
	require.Equal(t, 2, r.Sentences)
	require.Equal(t, 2, r.Cited)
	require.Zero(t, r.Unsupported)
	require.Equal(t, 2, r.Refs)
	require.Equal(t, 2, r.ValidRefs)
	require.True(t, r.Valid)
}

func TestVerify_MissingCitationOnSentence(t *testing.T) {
	answer := "Transformer 使用多头注意力机制[1]。它还能写诗。"
	r := Verify(answer, 2)
	require.Equal(t, 1, r.Cited)
	require.Equal(t, 1, r.Unsupported)
	require.False(t, r.Valid)
}

func TestVerify_FabricatedRefNumber(t *testing.T) {
	// 只给了 2 条 citation，回答里却出现 [7]：编号越界，不算合法引用。
	answer := "这个结论来自论文第七页[7]。"
	r := Verify(answer, 2)
	require.Equal(t, 1, r.Refs)
	require.Zero(t, r.ValidRefs)
	require.Equal(t, 1, r.Unsupported)
	require.False(t, r.Valid)
}

func TestVerify_IgnoresShortFragments(t *testing.T) {
	// 引导短语、列表符号不算句子；真正的事实句带引用 -> valid。
	answer := "总结如下：\n- 第一点是注意力[1]\n- 第二点是并行化[2]"
	r := Verify(answer, 2)
	require.Equal(t, 2, r.Sentences)
	require.True(t, r.Valid)
}

func TestVerify_EmptyAnswer(t *testing.T) {
	r := Verify("   ", 0)
	require.Equal(t, 0, r.Sentences)
	require.False(t, r.Valid)
}
