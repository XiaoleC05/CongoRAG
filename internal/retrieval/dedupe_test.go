package retrieval

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

func chunkWith(content string) domain.Chunk {
	return domain.Chunk{ID: uuid.New(), Content: content}
}

func TestDedupeNearDuplicates_RemovesIdentical(t *testing.T) {
	text := "The Transformer is the first transduction model relying entirely on self-attention."
	in := []domain.Chunk{chunkWith(text), chunkWith(text), chunkWith("Completely different content about convolutional layers and pooling operations.")}
	out := dedupeNearDuplicates(in, 5)
	require.Len(t, out, 2, "完全相同的正文只保留第一条")
	require.Equal(t, in[0].ID, out[0].ID, "保留排名更靠前的那条")
}

func TestDedupeNearDuplicates_RemovesOCRVariants(t *testing.T) {
	// 同一段文字的两个版本：一个带空格/标点，一个是 OCR 粘连版——归一化后
	// 5-gram 重合度应超过阈值。
	a := chunkWith("The goal of reducing sequential computation also forms the foundation of the Extended Neural GPU, ByteNet and ConvS2S.")
	b := chunkWith("ThegoalofreducingsequentialcomputationalsoformsthefoundationoftheExtendedNeuralGPU ByteNetandConvS2S")
	c := chunkWith("Attention mechanisms have become a core building block of modern sequence models.")
	out := dedupeNearDuplicates([]domain.Chunk{a, b, c}, 5)
	require.Len(t, out, 2, "OCR 变体应被判为近重复，保留第一条")
	require.Equal(t, a.ID, out[0].ID)
	require.Equal(t, c.ID, out[1].ID)
}

func TestDedupeNearDuplicates_KeepsDistinctAndLimit(t *testing.T) {
	in := []domain.Chunk{
		chunkWith("alpha beta gamma delta epsilon zeta eta theta"),
		chunkWith("iota kappa lambda mu nu xi omicron pi"),
		chunkWith("rho sigma tau upsilon phi chi psi omega"),
	}
	out := dedupeNearDuplicates(in, 2)
	require.Len(t, out, 2, "去重后仍要凑满 topK")
	require.Equal(t, in[0].ID, out[0].ID)
	require.Equal(t, in[1].ID, out[1].ID)
}

func TestDedupeNearDuplicates_EmptyContentKept(t *testing.T) {
	in := []domain.Chunk{chunkWith(""), chunkWith("   "), chunkWith(strings.Repeat("x", 100))}
	out := dedupeNearDuplicates(in, 3)
	require.Len(t, out, 3, "空内容没有可比性，直接保留")
}

func TestDedupeNearDuplicates_ZeroLimit(t *testing.T) {
	require.Nil(t, dedupeNearDuplicates([]domain.Chunk{chunkWith("abc")}, 0))
}
