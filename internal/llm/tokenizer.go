package llm

import (
	"fmt"
	"sync"

	"github.com/weaviate/tiktoken-go"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// tiktoken.GetEncoding 首次调用要下载/解析几百 KB 的 BPE 词表；即使命中
// 本地缓存文件，反序列化整张表也不是免费的。conversation.Send 每次请求
// 都要问一遍 Registry.Tokenizer，所以在进程内缓存*编码实例*——这和
// eino.go 的 resolve() 每次都重新查 Provider/Key 不矛盾：Key 会变，但
// "cl100k_base 编码表长什么样" 在进程生命周期内不会变。
var (
	tiktokenMu    sync.Mutex
	tiktokenCache = map[string]*tiktoken.Tiktoken{}
)

func getTiktokenEncoding(name string) (*tiktoken.Tiktoken, error) {
	tiktokenMu.Lock()
	defer tiktokenMu.Unlock()

	if enc, ok := tiktokenCache[name]; ok {
		return enc, nil
	}
	enc, err := tiktoken.GetEncoding(name)
	if err != nil {
		return nil, fmt.Errorf("load tiktoken encoding %q: %w", name, err)
	}
	tiktokenCache[name] = enc
	return enc, nil
}

type tiktokenTokenizer struct {
	enc *tiktoken.Tiktoken
}

func (t tiktokenTokenizer) Count(text string) int {
	return len(t.enc.Encode(text, nil, nil))
}

// newTokenizer 按 llm_models.tokenizer_type 建 Tokenizer 实例。
//
// 【只认这两个值】引导页表单目前只提供 cl100k_base/o200k_base 两个选项
// （开发文档 §6.2 的示例截图）。遇到别的字符串直接报错，而不是回退到
// 近似估算——静默用错编码表比明确拒绝更危险，预算阶梯的正确性完全
// 建立在这个数字准的前提上。
func newTokenizer(tokenizerType string) (Tokenizer, error) {
	switch tokenizerType {
	case "cl100k_base", "o200k_base":
		enc, err := getTiktokenEncoding(tokenizerType)
		if err != nil {
			return nil, err
		}
		return tiktokenTokenizer{enc: enc}, nil
	default:
		return nil, fmt.Errorf("unsupported tokenizer_type %q: %w", tokenizerType, platform.ErrInvalid)
	}
}
