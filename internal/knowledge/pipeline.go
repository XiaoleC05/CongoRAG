// pipeline.go 是文档处理管道里属于 knowledge 的那一半：parse → chunk。
//
// 【为什么 embed 不在这里】代码架构设计 §2.1 明确警告过：knowledge 里没有
// `llm`，管线在这里被切成两段——knowledge 只做 parse → chunk，产出
// []domain.Chunk 交给 ChunkIndexer；embed 和落库都在 retrieval（它才有
// llm.Registry）。这个文件如果调用了任何 embedding API，就等于新加一条
// `knowledge → llm` 的依赖边，得先改依赖表 + 记 ADR。
package knowledge

import (
	"strings"
	"unicode/utf8"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

// maxChunkChars 是一个分块的目标上限（按字符数，不是字节数——理由同
// usecase.go 的 maxNameLen：中文一个字三字节，按字节算的话上限会随语言
// 变化）。这是 M1 起步的粗粒度切分，不是最终的切分策略——技术方案 §4.1
// 把"Chunk 切分策略：按文档类型选择"列为策略模式的应用位置，MD/TXT 之外的
// 格式、更聪明的切分算法（按标题分层、语义边界）留给之后需要时再加，
// 现在只做"够用、可预测"的版本：按空行分段，贪心地把段落装进不超过
// maxChunkChars 的块里，单个段落本身超限就硬切。
const maxChunkChars = 1200

// parseAndChunk 把原始文档内容切成若干个 domain.Chunk。
//
// 【MD/TXT 起步】两种格式都当纯文本处理——不解析 Markdown 的标题/列表
// 结构，只按空行分段。技术方案附录 A 把"更深入的文档解析"列为故意不做
// 的事，M1 的验收只要求"文档上传落盘 + 入队链路可用"，不要求解析质量。
//
// 返回的 Chunk 只填 Content 一个字段——ID/DocumentID/Score 由调用方
// （retrieval.Usecase.IndexDocument）在落库时补上，这里不需要知道
// 文档是哪一个、也不需要关心相关性分数（那是检索时才有意义的字段）。
func parseAndChunk(content []byte) []domain.Chunk {
	text := normalizeNewlines(string(content))
	paragraphs := splitParagraphs(text)

	var out []domain.Chunk
	var current strings.Builder

	flush := func() {
		s := strings.TrimSpace(current.String())
		if s != "" {
			out = append(out, domain.Chunk{Content: s})
		}
		current.Reset()
	}

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// 单个段落本身就超过上限：先把攒到一半的块吐出去，
		// 再把这个超长段落自己硬切成若干块。
		if utf8.RuneCountInString(p) > maxChunkChars {
			flush()
			for _, part := range hardSplit(p, maxChunkChars) {
				out = append(out, domain.Chunk{Content: part})
			}
			continue
		}

		// 加上这一段会不会超限？会的话先把当前块吐出去，重新开始累积。
		if current.Len() > 0 && utf8.RuneCountInString(current.String())+utf8.RuneCountInString(p)+2 > maxChunkChars {
			flush()
		}

		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(p)
	}
	flush()

	return out
}

// normalizeNewlines 把 Windows 的 \r\n 和旧 Mac 的 \r 都统一成 \n，
// 否则"空行分段"这条规则在不同换行风格的文件上会得到不同结果——
// 同一份内容因为保存时用的编辑器不同，切出来的分块数量都会不一样。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// splitParagraphs 按"一个或多个空行"分段。
func splitParagraphs(text string) []string {
	var paragraphs []string
	var current strings.Builder

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if current.Len() > 0 {
				paragraphs = append(paragraphs, current.String())
				current.Reset()
			}
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		paragraphs = append(paragraphs, current.String())
	}
	return paragraphs
}

// hardSplit 把一个超长段落按 maxChars（rune 数）切成若干块，不管语义边界——
// 这是"总要有个不失败的兜底"，用户上传一个没有任何空行的超长文件时
// （比如压缩过的日志、单行 CSV）触发。按 rune 切避免在多字节字符中间
// 切断，产出无效的 UTF-8。
func hardSplit(s string, maxChars int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		n := maxChars
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}
