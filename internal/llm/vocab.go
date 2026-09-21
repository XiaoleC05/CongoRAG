package llm

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/weaviate/tiktoken-go"
)

// ════════════════════════════════════════════════════════════════
// tiktoken 词表的取用（issue #44）
//
// 问题：weaviate/tiktoken-go 首次解析某个编码时会去
// openaipublic.blob.core.windows.net 下载词表，而它的下载是**裸的
// http.Get**——没有超时、不接 ctx。于是「全新安装 + 离线环境」下第一次
// 提问会卡住或失败，而且错误来自 tiktoken 而不是用户配的模型。
//
// 这个文件接管整条取用路径：自己看缓存、自己下载（带超时）、自己解析。
// 进程启动时用 WarmupTokenizers 把两张表都准备好，失败就拒绝启动——把
// 问题暴露在启动那一刻，而不是用户敲完第一句话之后。
// ════════════════════════════════════════════════════════════════

// vocabURLs 把编码名映射到 tiktoken-go 内部硬编码的那个下载地址。
//
// 【为什么要把库里的字面量抄一份】那两个 URL 写死在 weaviate/tiktoken-go
// 的 encoding.go 的 o200k_base() / cl100k_base() 里，库交出来的唯一口子是
// bpeLoader 这个包级变量。要在自己的 loader 里认出"这次请求的是哪张表"，
// 就只能按 URL 认。
//
// 库升级把这些地址改了的话，vocab_test.go 里那条「每个
// ValidTokenizerTypes 都能映射到一张表」的断言会红——失败方式是响亮的
// 启动拒绝，不是静默用错表。
var vocabURLs = map[string]string{
	"https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken": "cl100k_base",
	"https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken":  "o200k_base",
}

const (
	// vocabDownloadTimeout：词表在 OpenAI 的公开 blob 上，正常一两秒。
	// 30 秒还没下来就是不通。
	vocabDownloadTimeout = 30 * time.Second

	// vocabFileMode 和别的缓存文件一致。
	vocabFileMode = 0o644
)

type vocabLoader struct {
	cacheDir string
	// client 可注入，方便测试离线路径而不动机器的网络设置。
	// 为 nil 时用带超时的默认 client。
	client *http.Client
}

// cachePath 是某张词表在缓存目录里的文件名。
//
// 【必须是 URL 的 sha1】这是 tiktoken-go 自己的缓存键规则，沿用它的好处是
// 已有的缓存文件继续命中（本机 data/tiktoken-cache/9b5ad71b... 就是这样一份），
// 而且"手工放置"的说明只有一种写法。换成自定义命名会让所有已有缓存失效、
// 大家重新下一遍。
func (l *vocabLoader) cachePath(url string) string {
	return filepath.Join(l.cacheDir, fmt.Sprintf("%x", sha1.Sum([]byte(url))))
}

// LoadTiktokenBpe 实现 tiktoken.BpeLoader。
func (l *vocabLoader) LoadTiktokenBpe(url string) (map[string]int, error) {
	if _, ok := vocabURLs[url]; !ok {
		// p50k_base / r50k_base 这些库里有、本项目不支持的编码也会走到这里
		// （它们只出现在 ValidTokenizerTypes 之外的场景）。明确报错，不联网。
		return nil, fmt.Errorf("unsupported vocabulary url %q", url)
	}

	path := l.cachePath(url)
	// 【先看缓存，再下载】缓存命中时一次网络调用都不会发生——这正是
	// issue #44 要的行为，也是 vocab_test.go 里那条测试钉的东西。
	if _, err := os.Stat(path); err != nil {
		if err := l.download(url, path); err != nil {
			return nil, fmt.Errorf("%s: %w", vocabPlacementHint(l.cacheDir, url), err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read vocabulary %s: %w", path, err)
	}
	return parseVocabulary(data)
}

// download 把词表取回来并原子落盘。
func (l *vocabLoader) download(url, dest string) error {
	if err := os.MkdirAll(l.cacheDir, 0o755); err != nil {
		return fmt.Errorf("create vocabulary cache dir %s: %w", l.cacheDir, err)
	}

	client := l.client
	if client == nil {
		// 【必须带超时】库那边是裸的 http.Get；这里要一个上限。
		//
		// 不自定义 Transport：默认的 Transport 认 HTTPS_PROXY，这让"离线"
		// 这条路径可以用 HTTPS_PROXY=http://127.0.0.1:9 复现，不用改机器的
		// 网络设置。
		client = &http.Client{Timeout: vocabDownloadTimeout}
	}

	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	// 【原子落盘：临时文件 + rename】直接写目标路径的话，中途失败会留下一个
	// 半截文件——而它是"存在"的，下一次启动会把它当成有效缓存读进来，
	// 解析失败且再也自愈不了。
	tmp, err := os.CreateTemp(l.cacheDir, "vocab-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", l.cacheDir, err)
	}
	defer os.Remove(tmp.Name()) // rename 成功之后这里删不到东西，是失败路径的兜底

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write vocabulary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close vocabulary temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), vocabFileMode); err != nil {
		return fmt.Errorf("chmod vocabulary temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("move vocabulary into place: %w", err)
	}
	return nil
}

// parseVocabulary 解析 .tiktoken 文件：每行「base64(token) 空格 rank」。
//
// 【为什么不用库的 NewDefaultBpeLoader 解析】它对非 http 入参会走
// readFileCached，那条路会：
//   - 在缓存目录里**再写一份**以「路径的 sha1」为名的副本（~1.7 MB），
//     文件名随检出目录而变，纯属垃圾；
//   - 缓存目录不可写时（只读挂载、权限不对）那份 WriteFile 的错误会直接
//     让解析失败——而字节明明已经在本地了。
//
// 这段格式是 OpenAI 公开的 .tiktoken 文件格式，只有十来行，自己解析换来的是
// 没有这两个副作用、也不依赖库的内部实现。
func parseVocabulary(data []byte) (map[string]int, error) {
	ranks := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		// 【用 SplitN 而不是 Split】token 的 base64 里不会出现空格，但
		// 用 SplitN 明确表达"只切第一个空格"，不会因为行尾多了空白而错位。
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed vocabulary line %q", line)
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, fmt.Errorf("decode token on line %q: %w", line, err)
		}
		rank, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("parse rank on line %q: %w", line, err)
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}

// vocabPlacementHint 是「缓存里没有、又下不下来」时给用户看的说明。
//
// 【必须有三样东西】缓存目录、那个下载地址、以及文件名怎么来的。
// 第三样最容易被漏掉：缓存文件名是地址的 sha1 十六进制，不是原始文件名，
// 猜不出来。缺了任意一样，离线的用户就只能去读源码。
func vocabPlacementHint(cacheDir, url string) string {
	// 用反引号原始字符串，避免转义把这份说明弄得难读——它既会打到 stderr，
	// 也可能出现在一次 SSE 错误的 detail 里。
	return `tiktoken 词表既不在本地缓存里，也下载不下来。
缓存目录：` + cacheDir + `
下载地址：` + url + `
离线安装：在任意能联网的机器上下载上面那个地址，保存为
  ` + filepath.Join(cacheDir, fmt.Sprintf("%x", sha1.Sum([]byte(url)))) + `
（文件名是那个地址的 sha1 十六进制，不是原始文件名——这是 tiktoken-go 的缓存键规则；
 扩展名没有意义，文件名必须一字不差）`
}

// WarmupTokenizers 在进程启动时把 ValidTokenizerTypes 里的词表全部准备好。
//
// 【为什么在启动时做】聊天路径上（conversation.Usecase.Send）才第一次问
// Registry.Tokenizer；在那之前谁都不会碰词表。等到第一条消息才去下载，用户
// 看到的是"发了消息、卡住、然后一个看不懂的错误"。放到启动时，同一个问题
// 变成一条能照着做的启动失败。
//
// 【为什么整个集合都要预热】用户能配的 tokenizer_type 就是这个集合，而
// llm_models 在首次启动、引导页跑之前是空的——按库里已有的模型去预热，恰恰
// 漏掉最需要预热的那一次。集合只有两个元素，代价可控。
//
// 【必须在任何 GetEncoding 之前调用，且不要并发】SetBpeLoader 写的是一个
// 包级变量，而库一旦把某个编码解析过就再也不问 loader 了。本项目所有
// tokenizer 访问都在启动完成之后，所以只有一个调用点、不会竞争；
// 不要把它挪进 goroutine，也不要放进 init()。
//
// 【预热失败必须让调用方拒绝启动】没有词表时聊天功能 100% 不可用（每条
// 消息都在同一步失败），一个"起来了但每条消息都报错"的进程比一个明确拒绝
// 启动的进程难排查得多。这与装配根已有的做法一致：数据库连不上时它也不启动。
func WarmupTokenizers(cacheDir string) error {
	tiktoken.SetBpeLoader(&vocabLoader{cacheDir: cacheDir})

	// 遍历 ValidTokenizerTypes 而不是抄一份名字列表，保持单一来源——
	// tokenizer_test.go 里那条"每个合法编码都能加载"的断言守的就是它。
	//
	// 走 getTiktokenEncoding 而不是 tiktoken.GetEncoding：前者是进程内的
	// 实例缓存，预热过的编码在聊天路径上就被直接命中，不会再解析一遍。
	for name := range ValidTokenizerTypes {
		if _, err := getTiktokenEncoding(name); err != nil {
			return err
		}
	}
	return nil
}
