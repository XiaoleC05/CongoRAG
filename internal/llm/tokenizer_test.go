package llm

import "testing"

// 开发文档 §6.2：token_cost 算错，整个预算阶梯都是错的，所以这三条不是
// "能跑就行"的冒烟测试——期望值是拿真实字符串喂给官方 Python tiktoken
// （pip install tiktoken，tiktoken.get_encoding(...).encode(...)）跑出来的
// token 数，逐条人工核对过，不是估出来的。
//
// 【需要网络】weaviate/tiktoken-go 首次加载某个 encoding 要从
// openaipublic.blob.core.windows.net 下载词表；本机之前跑过一次后
// 会缓存到 TIKTOKEN_CACHE_DIR（测试没设这个环境变量时用它的默认值），
// 之后的运行不再依赖网络。
func TestTiktokenTokenizer_MatchesReferenceCounts(t *testing.T) {
	cases := []struct {
		tokenizerType string
		text          string
		wantCount     int
	}{
		{"cl100k_base", "Hello, world! This is a test of the tokenizer.", 12},
		{"cl100k_base", "ConGoRAG is a local-first knowledge base and RAG platform, using PostgreSQL for vector storage.", 21},
		{"cl100k_base", "The quick brown fox jumps over 42 lazy dogs.", 11},
		{"o200k_base", "Hello, world! This is a test of the tokenizer.", 12},
		{"o200k_base", "ConGoRAG is a local-first knowledge base and RAG platform, using PostgreSQL for vector storage.", 22},
		{"o200k_base", "The quick brown fox jumps over 42 lazy dogs.", 11},
	}

	for _, tc := range cases {
		tok, err := newTokenizer(tc.tokenizerType)
		if err != nil {
			t.Fatalf("newTokenizer(%q): %v", tc.tokenizerType, err)
		}
		if got := tok.Count(tc.text); got != tc.wantCount {
			t.Errorf("[%s] Count(%q) = %d, want %d", tc.tokenizerType, tc.text, got, tc.wantCount)
		}
	}
}

func TestNewTokenizer_UnsupportedType_Errors(t *testing.T) {
	if _, err := newTokenizer("not-a-real-encoding"); err == nil {
		t.Fatal("expected an error for an unsupported tokenizer_type, got nil")
	}
}

func TestNewTokenizer_EmptyText_CountsZero(t *testing.T) {
	tok, err := newTokenizer("cl100k_base")
	if err != nil {
		t.Fatalf("newTokenizer: %v", err)
	}
	if got := tok.Count(""); got != 0 {
		t.Errorf("Count(\"\") = %d, want 0", got)
	}
}
