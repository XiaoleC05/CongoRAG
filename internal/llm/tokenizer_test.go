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

// ValidTokenizerTypes 是"保存时就拒绝"的依据（validateBootstrapRequest 用它），
// newTokenizer 是"消费时拒绝"的依据——两者必须恰好是同一个集合。
// 一旦漂移（例如集合里多了一个 newTokenizer 建不出来的值），坏配置又会
// 从引导页溜进去，然后每条消息才失败。
func TestValidTokenizerTypes_MatchesNewTokenizer(t *testing.T) {
	// 集合里每个值都必须真的能建出 tokenizer。这条要加载 BPE 词表，
	// 前提和 TestTiktokenTokenizer_MatchesReferenceCounts 一样。
	for name := range ValidTokenizerTypes {
		if _, err := newTokenizer(name); err != nil {
			t.Errorf("ValidTokenizerTypes 收了 %q,但 newTokenizer 拒绝了它: %v", name, err)
		}
	}

	// 集合外的值必须被拒，且不在集合里。这些编码名在 tiktoken 里真实存在，
	// 正是用户会手填进来的那些；newTokenizer 在加载词表之前就返回，不需要网络。
	for _, name := range []string{"p50k_base", "r50k_base", "gpt2", "o200k", "llama3"} {
		if _, err := newTokenizer(name); err == nil {
			t.Errorf("newTokenizer(%q) 应该报错,却成功建出了 tokenizer", name)
		}
		if ValidTokenizerTypes[name] {
			t.Errorf("ValidTokenizerTypes 不该收 %q", name)
		}
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
