package llm

import (
	"fmt"
	"sync"

	"github.com/weaviate/tiktoken-go"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

// tiktoken.GetEncoding 首次调用要解析几百 KB 的 BPE 词表（词表从哪来、
// 离线怎么办见 vocab.go）；即使命中本地缓存文件，反序列化整张表也不是免费的。
// conversation.Send 每次请求都要问一遍 Registry.Tokenizer，所以在进程内缓存
// *编码实例*——这和 eino.go 的 resolve() 每次都重新查 Provider/Key 不矛盾：
// Key 会变，但"cl100k_base 编码表长什么样" 在进程生命周期内不会变。
//
// 【这把锁会覆盖整次解析，包括可能的下载】启动时的预热是单线程的，所以
// 无害；但如果将来有人在服务运行期间触发一次新的编码加载，那 30 秒的下载
// 会把每一个聊天请求挡在这把锁上（Send 每次都走这里）。要加运行期预热的话，
// 先解决这件事。
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

// ValidTokenizerTypes 是 newTokenizer 唯一接受的 tokenizer_type 集合。
//
// 【为什么要导出、为什么要在这里就挡住】引导页表单上 tokenizerType 是一个
// 自由文本输入框，不是选择框：o200k（少写 _base）、p50k_base / r50k_base
// （tiktoken 里真实存在的另外两个编码名）都会被用户填进来。如果只有
// newTokenizer 这一个校验点，非法值会先被 Bootstrap 落进 llm_models、
// 引导页返回 201 成功，之后每一条消息才在聊天路径上失败——错误还被归因到
// 用户刚发的那条消息上，用户没有任何线索怀疑是存下来的配置。
// 所以校验点有两个：保存时的 validateBootstrapRequest 和消费时的
// newTokenizer，两处共用这一个集合定义，不会再出现"表单、契约、校验
// 各写一份"的漂移。
var ValidTokenizerTypes = map[string]bool{
	"cl100k_base": true,
	"o200k_base":  true,
}

// newTokenizer 按 llm_models.tokenizer_type 建 Tokenizer 实例。
//
// 遇到集合外的字符串直接报错，而不是回退到近似估算——静默用错编码表比
// 明确拒绝更危险，预算阶梯的正确性完全建立在这个数字准的前提上。
func newTokenizer(tokenizerType string) (Tokenizer, error) {
	if !ValidTokenizerTypes[tokenizerType] {
		return nil, fmt.Errorf("unsupported tokenizer_type %q: %w", tokenizerType, platform.ErrInvalid)
	}
	enc, err := getTiktokenEncoding(tokenizerType)
	if err != nil {
		return nil, err
	}
	return tiktokenTokenizer{enc: enc}, nil
}
