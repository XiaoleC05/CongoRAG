// 契约类型与业务类型的显式转换。
//
// 两个类型分开是因为服务对象不同：knowledge.KB 随业务变化，
// api.KnowledgeBase 随接口变化。在这里显式转一次，"哪些字段暴露给前端"
// 就有了唯一一处可看的地方；直接复用的话，业务里加个内部字段会自动漏到
// API 上。
//
// 本文件只做纯函数映射与共享的小工具，不发请求、不碰数据库、不写日志。
// handler_*.go 里每个端点只调用一次对应的 toAPI*，不做二次拼装。
package api

import (
	"encoding/json"
	"strings"

	"github.com/XiaoleC05/CongoRAG/internal/agent"
	"github.com/XiaoleC05/CongoRAG/internal/conversation"
	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/knowledge"
)

// toAPIKB 把业务类型转成契约类型。
//
// 两个类型分开是因为服务对象不同：knowledge.KB 随业务变化，
// api.KnowledgeBase 随接口变化。在这里显式转一次，
// "哪些字段暴露给前端"就有了唯一一处可看的地方。
// 直接复用的话，业务里加个内部字段会自动漏到 API 上。
func toAPIKB(kb *knowledge.KB) KnowledgeBase {
	return KnowledgeBase{
		Id:        kb.ID,
		Name:      kb.Name,
		CreatedAt: kb.CreatedAt,
		UpdatedAt: kb.UpdatedAt,
	}
}

// toAPIKBList 转一批。
//
// 用 make(..., 0, n) 而不是 var out []KnowledgeBase：
// nil 切片序列化成 JSON 是 null，空切片是 []。
// 前端拿到 null 去做 .map() 会直接崩。
func toAPIKBList(kbs []*knowledge.KB) []KnowledgeBase {
	out := make([]KnowledgeBase, 0, len(kbs))
	for _, kb := range kbs {
		out = append(out, toAPIKB(kb))
	}
	return out
}

// toAPIDocument 和 toAPIKB 一样的理由：业务类型和契约类型分开，
// "哪些字段暴露给前端"只有这一处可看。
func toAPIDocument(d *knowledge.Document) Document {
	// 可选的去重告警字段用指针：契约里它们不是 required，生成类型是
	// *string/*int；没有重复时留 nil，JSON 里就不会出现空字段。
	var (
		duplicateOf       *string
		duplicateFilename *string
		duplicateDistance *int
	)
	if d.DuplicateOf != nil {
		id := d.DuplicateOf.String()
		duplicateOf = &id
	}
	if d.DuplicateFilename != "" {
		name := d.DuplicateFilename
		duplicateFilename = &name
	}
	if d.DuplicateDistance != 0 {
		distance := d.DuplicateDistance
		duplicateDistance = &distance
	}
	return Document{
		Id:                d.ID,
		KnowledgeBaseId:   d.KnowledgeBaseID,
		Filename:          d.Filename,
		Status:            DocumentStatus(d.Status),
		ByteSize:          d.ByteSize,
		ChunkCount:        d.ChunkCount,
		PageCount:         d.PageCount,
		Parser:            d.Parser,
		DuplicateOf:       duplicateOf,
		DuplicateFilename: duplicateFilename,
		DuplicateDistance: duplicateDistance,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}

func toAPIDocumentList(docs []*knowledge.Document) []Document {
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		out = append(out, toAPIDocument(d))
	}
	return out
}

// intPtrOrNil 把"0 表示没有"的整数转成契约里的 nullable 字段。
//
// 【为什么不是直接传 int 的地址】0 是一个合法的值（页数从 1 开始，页码 0
// 不是合法页码），但零值语义是"没有页码"。直接取地址会让前端拿到 page=0，
// 分不清"第 0 页"和"没有页"。
func intPtrOrNil(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// floatPtrOrNil 与 intPtrOrNil 同理：0 表示"这一路没命中"。
// 余弦相似度恰好 0 等价于不相关，ts_rank_cd 为 0 不进结果集，
// 所以用 0 当"没有"不会丢真实信息。
func floatPtrOrNil(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func floatSlicePtrOrNil(v []float64) *[]float64 {
	if len(v) == 0 {
		return nil
	}
	out := append([]float64(nil), v...)
	return &out
}

func stringSlicePtrOrNil(v []string) *[]string {
	if len(v) == 0 {
		return nil
	}
	out := append([]string(nil), v...)
	return &out
}

func toAPIConversation(c *conversation.Conversation) Conversation {
	return Conversation{
		Id:              c.ID,
		Title:           c.Title,
		KnowledgeBaseId: c.KnowledgeBaseID,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
	}
}

func toAPIMessage(m *conversation.Message) Message {
	// 引用与 grounding 是科研助手持久化字段（migration 0017）：空切片不是 nil，
	// 契约里 citations 是数组，nil 会序列化成 null（前端 .map() 会崩）。
	citations := make([]MessageCitation, 0, len(m.Citations))
	for _, c := range m.Citations {
		citations = append(citations, MessageCitation{
			ChunkId:     c.ChunkID,
			DocumentId:  c.DocumentID,
			Filename:    c.Filename,
			Snippet:     c.Snippet,
			Score:       c.Score,
			Page:        intPtrOrNil(c.Page),
			PageEnd:     intPtrOrNil(c.PageEnd),
			Bbox:        floatSlice32PtrOrNil(c.BBox),
			SectionPath: stringSlicePtrOrNil(c.SectionPath),
		})
	}

	out := Message{
		Id:             m.ID,
		ConversationId: m.ConversationID,
		Role:           MessageRole(m.Role),
		Content:        m.Content,
		Status:         MessageStatus(m.Status),
		SequenceNo:     m.SequenceNo,
		CreatedAt:      m.CreatedAt,
		Citations:      citations,
	}
	if m.Grounding != nil {
		out.Grounding = &struct {
			Cited       int  `json:"cited"`
			Refs        int  `json:"refs"`
			Sentences   int  `json:"sentences"`
			Unsupported int  `json:"unsupported"`
			Valid       bool `json:"valid"`
			ValidRefs   int  `json:"validRefs"`
		}{
			Cited:       m.Grounding.Cited,
			Refs:        m.Grounding.Refs,
			Sentences:   m.Grounding.Sentences,
			Unsupported: m.Grounding.Unsupported,
			Valid:       m.Grounding.Valid,
			ValidRefs:   m.Grounding.ValidRefs,
		}
	}
	return out
}

// floatSlice32PtrOrNil 与 floatSlicePtrOrNil 相同，只是目标类型是契约里的
// float32（bbox 在 JSON 里是精度无关的归一化坐标，float32 足够）。
func floatSlice32PtrOrNil(v []float64) *[]float32 {
	if len(v) == 0 {
		return nil
	}
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return &out
}

func toAPIMessageList(msgs []*conversation.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toAPIMessage(m))
	}
	return out
}

func toAPIAgent(a *agent.Agent) Agent {
	return Agent{
		Id: a.ID, Name: a.Name, Description: a.Description, Instruction: a.Instruction,
		ToolNames: a.ToolNames, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func toAPIAgentList(agents []*agent.Agent) []Agent {
	out := make([]Agent, len(agents))
	for i, a := range agents {
		out[i] = toAPIAgent(a)
	}
	return out
}

func toAPIAgentRun(r *agent.Run) AgentRun {
	return AgentRun{
		Id: r.ID, AgentId: r.AgentID, Status: AgentRunStatus(r.Status), CurrentStep: r.CurrentStep,
		Input: r.Input, Output: r.Output, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func toAPIAgentRunList(runs []*agent.Run) []AgentRun {
	out := make([]AgentRun, len(runs))
	for i, r := range runs {
		out[i] = toAPIAgentRun(r)
	}
	return out
}

// toAPIRawJSON 把可能为空的 json.RawMessage 转成契约里 interface{}
// 字段期望的形状——nil/空切片必须转成 Go 的 nil,不能是 json.RawMessage(nil)
// 这个具体类型的零值,否则 encoding/json 序列化时会把它当成非 nil 的
// "长度为 0 的原始字节"处理,输出 `""` 而不是契约期望的字段整体消失
// （omitempty 只认 Go 层面的 nil interface{}，不认"底层是空切片的具名类型"）。
func toAPIRawJSON(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func toAPIAgentRunStep(s *agent.Step) AgentRunStep {
	step := AgentRunStep{
		Id: s.ID, RunId: s.RunID, Seq: s.Seq, Type: AgentRunStepType(s.Type),
		Status: AgentRunStepStatus(s.Status), ToolArgs: toAPIRawJSON(s.ToolArgs),
		ToolResult: toAPIRawJSON(s.ToolResult), CreatedAt: s.CreatedAt,
	}
	if s.ToolName != "" {
		step.ToolName = &s.ToolName
	}
	if s.LatencyMS != 0 {
		step.LatencyMs = &s.LatencyMS
	}
	if s.Error != "" {
		step.Error = &s.Error
	}
	return step
}

func toAPIAgentRunStepList(steps []*agent.Step) []AgentRunStep {
	out := make([]AgentRunStep, len(steps))
	for i, s := range steps {
		out[i] = toAPIAgentRunStep(s)
	}
	return out
}

func toAPIToolCatalog(entries []agent.ToolCatalogEntry) []ToolCatalogEntry {
	out := make([]ToolCatalogEntry, len(entries))
	for i, e := range entries {
		out[i] = ToolCatalogEntry{
			Name: e.Name, Description: e.Description,
			SideEffectLevel: ToolCatalogEntrySideEffectLevel(e.SideEffectLevel),
		}
	}
	return out
}

// toAPIKnowledgeHits 把检索结果映射成契约类型。
//
// 【抽出来是因为写作 Copilot 的 citations 用的是同一个 schema】两处
// 各写一份映射，加字段时必然漏一处；而漏掉的那一处不会报错——只是
// 某一条路径上的页码悄悄变成 null。
func toAPIKnowledgeHits(chunks []domain.Chunk) []KnowledgeSearchHit {
	// 空切片不是 nil：契约里 hits 是数组，nil 会序列化成 null
	// （前端 .map() 会崩）——与 toAPIKBList 那条同一个约定。
	hits := make([]KnowledgeSearchHit, 0, len(chunks))
	for _, ch := range chunks {
		hits = append(hits, KnowledgeSearchHit{
			ChunkId:      ch.ID,
			DocumentId:   ch.DocumentID,
			Filename:     ch.Filename,
			Snippet:      ch.Content,
			Score:        ch.Score,
			Page:         intPtrOrNil(ch.PageStart),
			PageEnd:      intPtrOrNil(ch.PageEnd),
			Bbox:         floatSlicePtrOrNil(ch.BBox),
			SectionPath:  stringSlicePtrOrNil(ch.SectionPath),
			VectorScore:  floatPtrOrNil(ch.VectorScore),
			LexicalScore: floatPtrOrNil(ch.LexicalScore),
		})
	}
	return hits
}

// excerptChunks 把分块拼成一段文本并截断到 limit 个 rune。
//
// 【为什么放在这里而不是某个 handler 文件】它被两个资源域用：文档版本差异
// （knowledge）和多篇对比的证据片段（research）。放在其中一边会让另一边
// "反向依赖"一个 handler 文件里的私有函数；它是纯字符串工具，和 toAPI* 一样
// 属于 HTTP 层共享的纯函数，所以落在 convert.go。
func excerptChunks(chunks []domain.Chunk, limit int) string {
	var b strings.Builder
	for _, ch := range chunks {
		b.WriteString(ch.Content)
		b.WriteString("\n")
	}
	runes := []rune(b.String())
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
