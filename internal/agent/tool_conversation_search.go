package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

var _ Tool = (*ConversationSearch)(nil)

// ConversationSearch 包一层 ConversationSearcher（由
// conversation.Usecase.SearchMessages 实现),让模型能在指定会话的
// 历史消息里按关键词找相关内容。
type ConversationSearch struct {
	search ConversationSearcher
}

func NewConversationSearch(search ConversationSearcher) *ConversationSearch {
	return &ConversationSearch{search: search}
}

func (t *ConversationSearch) Name() string { return "conversation_search" }
func (t *ConversationSearch) Description() string {
	return "在指定会话的历史消息里按关键词查找相关内容"
}

func (t *ConversationSearch) Metadata() Metadata {
	return Metadata{SideEffectLevel: ReadOnly, RetryPolicy: RetrySafe}
}

const conversationSearchSchema = `{
	"type": "object",
	"properties": {
		"conversation_id": {"type": "string", "description": "要搜索的会话 id（uuid）"},
		"query": {"type": "string", "description": "要查找的关键词"}
	},
	"required": ["conversation_id", "query"]
}`

func (t *ConversationSearch) Spec() ToolSpec {
	return ToolSpec{Name: t.Name(), Description: t.Description(), Schema: json.RawMessage(conversationSearchSchema)}
}

type conversationSearchArgs struct {
	ConversationID string `json:"conversation_id"`
	Query          string `json:"query"`
}

type conversationSearchResultItem struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	SequenceNo int64  `json:"sequenceNo"`
}

func (t *ConversationSearch) Invoke(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a conversationSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("parse conversation_search args: %w: %v", platform.ErrInvalid, err)
	}
	convID, err := uuid.Parse(a.ConversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation_id %q is not a valid uuid: %w", a.ConversationID, platform.ErrInvalid)
	}

	snippets, err := t.search.SearchMessages(ctx, convID, a.Query)
	if err != nil {
		return nil, fmt.Errorf("search messages of conversation %s: %w", convID, err)
	}

	out := make([]conversationSearchResultItem, len(snippets))
	for i, s := range snippets {
		out[i] = conversationSearchResultItem{Role: string(s.Role), Content: s.Content, SequenceNo: s.SequenceNo}
	}
	return json.Marshal(out)
}
