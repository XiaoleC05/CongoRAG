package llm

import (
	"crypto/sha1"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ════════════════════════════════════════════════════════════════
// tiktoken 词表的取用（issue #44）
//
// 【这一组必须是密闭的】tokenizer_test.go 需要网络（它加载真实的词表去对
// 手算过的 token 数），所以它证明不了"离线可用"。这里反过来：网络被显式
// 打断，证明的是"没有网络时会发生什么"。
// ════════════════════════════════════════════════════════════════

const (
	cl100kURL = "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"
	o200kURL  = "https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken"
)

// failingTransport 的每一次请求都失败，并记下被调用了几次。
//
// 【为什么要计数】"缓存命中时不联网"这件事只有能观察"联网发生了没有"
// 才能证明——只看返回值是区分不出来的。
type failingTransport struct {
	calls int
}

func (t *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, fmt.Errorf("network is not available in this test")
}

func newOfflineLoader(t *testing.T, cacheDir string) (*vocabLoader, *failingTransport) {
	t.Helper()
	tr := &failingTransport{}
	return &vocabLoader{cacheDir: cacheDir, client: &http.Client{Transport: tr}}, tr
}

// 一份最小的合法 .tiktoken：每行「base64(token) 空格 rank」。
func tinyVocab() []byte {
	return []byte("aGVsbG8= 0\nIHdvcmxk 1\n")
}

func vocabCachePath(cacheDir, url string) string {
	return filepath.Join(cacheDir, fmt.Sprintf("%x", sha1.Sum([]byte(url))))
}

// 【这是 issue #44 的核心回归测试】缓存里已经有词表时，取用它一次网络
// 调用都不该发生。
func TestVocabLoader_CacheHitDoesNotTouchTheNetwork(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(vocabCachePath(dir, cl100kURL), tinyVocab(), 0o644))

	l, tr := newOfflineLoader(t, dir)
	ranks, err := l.LoadTiktokenBpe(cl100kURL)

	require.NoError(t, err)
	assert.Equal(t, map[string]int{"hello": 0, " world": 1}, ranks)
	assert.Zero(t, tr.calls, "缓存命中时不该有任何网络调用——这正是这条 issue 要的行为")
}

// 缓存目录里没有、网络又不通时，错误必须能照着做：说清缓存目录、那个
// 下载地址、以及文件名怎么来的。
//
// 【为什么连 sha1 文件名也断言】它是地址的十六进制 sha1，不是原始文件名，
// 用户猜不出来。缺了它，离线用户只能去读源码。
func TestVocabLoader_OfflineFailureSaysWhereToPutTheFile(t *testing.T) {
	dir := t.TempDir()
	l, tr := newOfflineLoader(t, dir)

	_, err := l.LoadTiktokenBpe(cl100kURL)

	require.Error(t, err)
	assert.Equal(t, 1, tr.calls, "缓存里没有就得试一次下载")
	assert.Contains(t, err.Error(), dir, "要说清缓存目录")
	assert.Contains(t, err.Error(), cl100kURL, "要说清下载地址")
	assert.Contains(t, err.Error(), fmt.Sprintf("%x", sha1.Sum([]byte(cl100kURL))),
		"要说清文件名怎么来的——它是地址的 sha1，不是原始文件名")
	assert.Contains(t, err.Error(), "sha1", "要解释一遍那个十六进制串是什么")
}

// 库里有、但本项目不支持的编码（p50k_base 之类）要直接报错，不联网。
func TestVocabLoader_UnknownURLIsRejectedWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	l, tr := newOfflineLoader(t, dir)

	_, err := l.LoadTiktokenBpe("https://openaipublic.blob.core.windows.net/encodings/p50k_base.tiktoken")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported vocabulary url")
	assert.Zero(t, tr.calls, "认不出的地址不该去联网试一次")
}

// 每个合法的 tokenizer_type 都要能在 vocabURLs 里找到对应的一张表。
//
// 【它守的是什么】那张表抄的是 tiktoken-go 内部的字面量。库升级改了地址
// 的话，这里会在构建期红——而不是让进程在启动时以一句"unsupported
// vocabulary url"拒绝启动，让人以为是配置问题。
func TestVocabURLs_CoversEveryValidTokenizerType(t *testing.T) {
	for name := range ValidTokenizerTypes {
		count := 0
		for url, mapped := range vocabURLs {
			if mapped == name {
				count++
				assert.True(t, strings.HasSuffix(url, name+".tiktoken"),
					"地址 %s 与编码名 %s 对不上", url, name)
			}
		}
		assert.Equal(t, 1, count, "编码 %s 必须恰好映射到一张词表", name)
	}
}

// 解析器本身的往返：手写一小段，断言解析结果。
func TestParseVocabulary(t *testing.T) {
	ranks, err := parseVocabulary(tinyVocab())

	require.NoError(t, err)
	assert.Equal(t, map[string]int{"hello": 0, " world": 1}, ranks)
}

// 坏行要报错而不是静默丢——静默丢会让词表少几个 token，症状是
// token 计数莫名其妙偏小。
func TestParseVocabulary_RejectsMalformedLines(t *testing.T) {
	for _, bad := range []string{
		"onlyonetoken\n",        // 没有 rank
		"!!!notbase64!!! 0\n",   // base64 解不开
		"aGVsbG8= notanumber\n", // rank 不是数字
	} {
		_, err := parseVocabulary([]byte(bad))
		assert.Error(t, err, "坏行 %q 必须报错", bad)
	}
}

// 两张表的地址都要真的能映射（防止只写一张）。
func TestVocabURLs_HasBothEncodings(t *testing.T) {
	assert.Equal(t, "cl100k_base", vocabURLs[cl100kURL])
	assert.Equal(t, "o200k_base", vocabURLs[o200kURL])
}
