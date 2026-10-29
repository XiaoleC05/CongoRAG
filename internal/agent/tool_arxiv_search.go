package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/XiaoleC05/CongoRAG/internal/arxiv"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Tool = (*ArxivSearch)(nil)

// ArxivSearcher 是工具依赖的检索能力（*arxiv.Client 实现）。
//
// 【为什么工具层不直接依赖 arxiv.Client】和知识库那个工具同一个理由：
// 测试要能在不联网的情况下验证参数解析/结果序列化；真实客户端只负责
// 网络与解析，两层各自可测。
type ArxivSearcher interface {
	Search(ctx context.Context, query string, maxResults int) ([]arxiv.Paper, error)
}

// ArxivSearch 让 Agent 能检索 arXiv 预印本（参赛版 M4）。
//
// 【为什么是独立工具而不是塞进 knowledge_search】两者的数据源、可靠性、
// 语义都不同：知识库检索的是"用户已上传的文档"，arXiv 检索的是"互联网
// 上的最新预印本"。模型需要自己决定"这个问题应该查本地资料还是查 arXiv"，
// 混成一个工具就拿掉了这个选择。
type ArxivSearch struct {
	client ArxivSearcher
}

func NewArxivSearch(client ArxivSearcher) *ArxivSearch {
	return &ArxivSearch{client: client}
}

func (t *ArxivSearch) Name() string { return "arxiv_search" }

func (t *ArxivSearch) Description() string {
	return "在 arXiv 上检索论文（按标题/作者/摘要关键词），返回标题、作者、摘要和链接"
}

func (t *ArxivSearch) Metadata() Metadata {
	// 只读外网检索：失败重试是安全的，但不会改变任何服务端状态。
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetrySafe}
}

const arxivSearchSchema = `{
	"type": "object",
	"properties": {
		"query": {
			"type": "string",
			"description": "arXiv 查询表达式。建议带字段前缀：all:transformer、ti:attention、au:Vaswani；纯关键词也行"
		},
		"max_results": {
			"type": "integer",
			"description": "返回条数（1~10，默认 5）"
		}
	},
	"required": ["query"]
}`

func (t *ArxivSearch) Spec() ToolSpec {
	return ToolSpec{Name: t.Name(), Description: t.Description(), Schema: json.RawMessage(arxivSearchSchema)}
}

type arxivSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

func (t *ArxivSearch) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a arxivSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("parse arxiv_search args: %w: %v", platform.ErrInvalid, err)
	}
	papers, err := t.client.Search(ctx, a.Query, a.MaxResults)
	if err != nil {
		return nil, fmt.Errorf("search arxiv: %w", err)
	}
	// 空结果返回 [] 而不是 null：模型对 null 的处理经常是"工具坏了"。
	if papers == nil {
		papers = []arxiv.Paper{}
	}
	return json.Marshal(papers)
}
