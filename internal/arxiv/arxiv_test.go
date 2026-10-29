package arxiv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleAtom = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/1706.03762v7</id>
    <published>2017-06-12T17:57:34Z</published>
    <title>Attention Is All
      You Need</title>
    <summary>We propose a new simple network architecture, the Transformer,
      based solely on attention mechanisms.</summary>
    <author><name>Ashish Vaswani</name></author>
    <author><name>Noam Shazeer</name></author>
    <link href="https://arxiv.org/abs/1706.03762v7" rel="alternate" type="text/html"/>
  </entry>
</feed>`

func TestSearch_ParsesAtom(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("search_query")
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(sampleAtom))
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	papers, err := c.Search(context.Background(), "all:transformer", 5)
	require.NoError(t, err)
	require.Len(t, papers, 1)
	assert.Equal(t, "all:transformer", gotQuery)
	assert.Equal(t, "1706.03762v7", papers[0].ID)
	assert.Equal(t, "Attention Is All You Need", papers[0].Title)
	assert.Equal(t, []string{"Ashish Vaswani", "Noam Shazeer"}, papers[0].Authors)
	assert.Contains(t, papers[0].Abstract, "based solely on attention mechanisms")
	assert.Equal(t, "https://arxiv.org/abs/1706.03762v7", papers[0].URL)
}

func TestSearch_ClampsMaxResults(t *testing.T) {
	var gotMax string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMax = r.URL.Query().Get("max_results")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"></feed>`))
	}))
	defer srv.Close()
	c := NewClient()
	c.baseURL = srv.URL

	_, err := c.Search(context.Background(), "all:rag", 100)
	require.NoError(t, err)
	assert.Equal(t, "25", gotMax)
}

func TestSearch_RejectsEmptyQuery(t *testing.T) {
	c := NewClient()
	_, err := c.Search(context.Background(), "   ", 5)
	require.Error(t, err)
}

func TestSearch_UpstreamStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := NewClient()
	c.baseURL = srv.URL
	_, err := c.Search(context.Background(), "all:rag", 5)
	require.Error(t, err)
}
