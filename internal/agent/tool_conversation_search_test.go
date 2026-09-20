package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/domain"
)

type fakeConversationSearcher struct {
	result    []domain.MessageSnippet
	err       error
	lastConv  uuid.UUID
	lastQuery string
}

func (f *fakeConversationSearcher) SearchMessages(ctx context.Context, convID uuid.UUID, query string) ([]domain.MessageSnippet, error) {
	f.lastConv, f.lastQuery = convID, query
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestConversationSearch_Invoke_ReturnsSnippetsAsJSON(t *testing.T) {
	convID := uuid.New()
	fake := &fakeConversationSearcher{result: []domain.MessageSnippet{
		{Role: domain.RoleUser, Content: "我喜欢简洁的回答", SequenceNo: 3},
	}}
	tool := NewConversationSearch(fake)

	args, _ := json.Marshal(map[string]string{"conversation_id": convID.String(), "query": "简洁"})
	raw, err := tool.Invoke(context.Background(), args)
	require.NoError(t, err)

	var results []conversationSearchResultItem
	require.NoError(t, json.Unmarshal(raw, &results))
	require.Len(t, results, 1)
	assert.Equal(t, "我喜欢简洁的回答", results[0].Content)
	assert.Equal(t, int64(3), results[0].SequenceNo)
	assert.Equal(t, convID, fake.lastConv)
	assert.Equal(t, "简洁", fake.lastQuery)
}

func TestConversationSearch_Invoke_InvalidUUID_Errors(t *testing.T) {
	tool := NewConversationSearch(&fakeConversationSearcher{})
	args, _ := json.Marshal(map[string]string{"conversation_id": "not-a-uuid", "query": "x"})

	_, err := tool.Invoke(context.Background(), args)
	require.Error(t, err)
}

func TestConversationSearch_Invoke_SearchFails_ErrorPropagates(t *testing.T) {
	fake := &fakeConversationSearcher{err: errors.New("search boom")}
	tool := NewConversationSearch(fake)
	args, _ := json.Marshal(map[string]string{"conversation_id": uuid.New().String(), "query": "x"})

	_, err := tool.Invoke(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search boom")
}
