package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Tool = (*KnowledgeSearch)(nil)

// KnowledgeSearch 包一层 KnowledgeSearcher（由 retrieval.Usecase 实现）,
// 让模型能在指定知识库里做向量检索。
//
// 【knowledge_base_id 是工具参数,不是 Run 级别的配置】和会话可选关联
// 一个知识库不同,Agent Run 这一轮没有"这次执行绑定哪个知识库"的概念
// （见 migrations/0005_agents.up.sql 的注释:Run 不挂 message_id,
// 本身就是独立于会话的一等实体)——模型需要知道要搜哪个库时,
// 由 Instruction/用户输入告诉它具体的知识库 id,工具本身不做任何默认。
type KnowledgeSearch struct {
	search KnowledgeSearcher
}

func NewKnowledgeSearch(search KnowledgeSearcher) *KnowledgeSearch {
	return &KnowledgeSearch{search: search}
}

func (t *KnowledgeSearch) Name() string { return "knowledge_search" }
func (t *KnowledgeSearch) Description() string {
	return "在指定知识库里做向量检索，找出和查询最相关的文档片段"
}

func (t *KnowledgeSearch) Metadata() Metadata {
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetrySafe}
}

const knowledgeSearchSchema = `{
	"type": "object",
	"properties": {
		"knowledge_base_id": {"type": "string", "description": "要搜索的知识库 id（uuid）"},
		"query": {"type": "string", "description": "搜索的查询文本"}
	},
	"required": ["knowledge_base_id", "query"]
}`

func (t *KnowledgeSearch) Spec() ToolSpec {
	return ToolSpec{Name: t.Name(), Description: t.Description(), Schema: json.RawMessage(knowledgeSearchSchema)}
}

type knowledgeSearchArgs struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Query           string `json:"query"`
}

type knowledgeSearchResultItem struct {
	Filename string  `json:"filename"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

func (t *KnowledgeSearch) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a knowledgeSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("parse knowledge_search args: %w: %v", platform.ErrInvalid, err)
	}
	kbID, err := uuid.Parse(a.KnowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("knowledge_base_id %q is not a valid uuid: %w", a.KnowledgeBaseID, platform.ErrInvalid)
	}

	chunks, err := t.search.Search(ctx, domain.SearchRequest{KnowledgeBaseID: kbID, Text: a.Query})
	if err != nil {
		return nil, fmt.Errorf("search knowledge base %s: %w", kbID, err)
	}

	out := make([]knowledgeSearchResultItem, len(chunks))
	for i, c := range chunks {
		out[i] = knowledgeSearchResultItem{Filename: c.Filename, Content: c.Content, Score: c.Score}
	}
	return json.Marshal(out)
}
