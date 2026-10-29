package knowledge

import (
	"strings"
	"testing"
)

func TestContentFingerprintStable(t *testing.T) {
	text := strings.Repeat("Attention is all you need. 注意力机制是 Transformer 的核心。", 20)
	a := ContentFingerprint(text)
	b := ContentFingerprint(text)
	if a == "" || a != b {
		t.Fatalf("fingerprint not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("fingerprint length = %d, want 16", len(a))
	}
	if got := FingerprintDistance(a, b); got != 0 {
		t.Fatalf("distance of identical text = %d, want 0", got)
	}
}

func TestContentFingerprintNearDuplicate(t *testing.T) {
	base := strings.Repeat("We propose a new simple network architecture, the Transformer, based solely on attention mechanisms. 我们提出一种新的简单网络架构 Transformer，完全基于注意力机制。", 30)
	// 模拟 v1/v7 的小幅修订：改几句话 + 加一段。
	revised := strings.Replace(base, "based solely on attention mechanisms", "based solely on attention mechanisms, dispensing with recurrence", 1) +
		" Experiments show these models can be trained significantly faster."
	if FingerprintDistance(ContentFingerprint(base), ContentFingerprint(revised)) > duplicateThreshold {
		t.Fatalf("near-duplicate should be within threshold %d", duplicateThreshold)
	}
}

func TestContentFingerprintDifferentPapers(t *testing.T) {
	a := strings.Repeat("BERT pre-trains deep bidirectional representations by jointly conditioning on both left and right context.", 30)
	b := strings.Repeat("We study overfitting and double descent in deep learning models on long documents.", 30)
	if FingerprintDistance(ContentFingerprint(a), ContentFingerprint(b)) <= duplicateThreshold {
		t.Fatalf("different papers should exceed threshold %d", duplicateThreshold)
	}
}

func TestContentFingerprintEmpty(t *testing.T) {
	if got := ContentFingerprint("   "); got != "" {
		t.Fatalf("empty text fingerprint = %q, want empty", got)
	}
	if FingerprintDistance("", "") != -1 {
		t.Fatal("empty fingerprints should return -1")
	}
	if FingerprintDistance("zzzz", "0000") != -1 {
		t.Fatal("invalid fingerprints should return -1")
	}
}
