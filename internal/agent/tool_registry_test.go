package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/XiaoleC05/CongoRAG/internal/platform"
)

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	r := NewToolRegistry()
	r.Register(NewCalculator())

	got, err := r.Get("calculator")
	require.NoError(t, err)
	assert.Equal(t, "calculator", got.Name())
}

func TestToolRegistry_Get_UnknownTool_ReturnsNotFound(t *testing.T) {
	r := NewToolRegistry()

	_, err := r.Get("does-not-exist")
	require.ErrorIs(t, err, platform.ErrNotFound)
}

func TestToolRegistry_List_ReturnsAllRegistered(t *testing.T) {
	r := NewToolRegistry()
	r.Register(NewCalculator())
	r.Register(NewKnowledgeSearch(nil))
	r.Register(NewConversationSearch(nil))

	list := r.List()
	assert.Len(t, list, 3)
}
