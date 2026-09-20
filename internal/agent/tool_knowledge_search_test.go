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

type fakeKnowledgeSearcher struct {
	result  []domain.Chunk
	err     error
	lastReq domain.SearchRequest
}

func (f *fakeKnowledgeSearcher) Search(ctx context.Context, req domain.SearchRequest) ([]domain.Chunk, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestKnowledgeSearch_Invoke_ReturnsChunksAsJSON(t *testing.T) {
	kbID := uuid.New()
	fake := &fakeKnowledgeSearcher{result: []domain.Chunk{
		{Filename: "a.md", Content: "关于 pgvector", Score: 0.9},
	}}
	tool := NewKnowledgeSearch(fake)

	args, _ := json.Marshal(map[string]string{"knowledge_base_id": kbID.String(), "query": "pgvector 是什么"})
	raw, err := tool.Invoke(context.Background(), args)
	require.NoError(t, err)

	var results []knowledgeSearchResultItem
	require.NoError(t, json.Unmarshal(raw, &results))
	require.Len(t, results, 1)
	assert.Equal(t, "a.md", results[0].Filename)
	assert.Equal(t, kbID, fake.lastReq.KnowledgeBaseID)
	assert.Equal(t, "pgvector 是什么", fake.lastReq.Text)
}

func TestKnowledgeSearch_Invoke_InvalidUUID_Errors(t *testing.T) {
	tool := NewKnowledgeSearch(&fakeKnowledgeSearcher{})
	args, _ := json.Marshal(map[string]string{"knowledge_base_id": "not-a-uuid", "query": "x"})

	_, err := tool.Invoke(context.Background(), args)
	require.Error(t, err)
}

func TestKnowledgeSearch_Invoke_SearchFails_ErrorPropagates(t *testing.T) {
	fake := &fakeKnowledgeSearcher{err: errors.New("search boom")}
	tool := NewKnowledgeSearch(fake)
	args, _ := json.Marshal(map[string]string{"knowledge_base_id": uuid.New().String(), "query": "x"})

	_, err := tool.Invoke(context.Background(), args)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search boom")
}
