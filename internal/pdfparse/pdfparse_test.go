package pdfparse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParsePDF_MapsBlocksWithPageAndBBox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/parse", r.URL.Path)
		require.NoError(t, r.ParseMultipartForm(8<<20))
		_, _, err := r.FormFile("file")
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(response{
			PageCount:     15,
			ParserVersion: "pdf-service-0.1.0",
			Blocks: []block{
				{ID: "p1-b0", Page: 1, BBox: []float64{0.1, 0.2, 0.9, 0.3}, Type: "heading", Text: "Attention Is All You Need", SectionPath: []string{"Attention Is All You Need"}},
				{ID: "p4-b7", Page: 4, BBox: []float64{0.1, 0.4, 0.5, 0.5}, Type: "paragraph", Text: "self-attention"},
				{ID: "p9-b2", Page: 9, Type: "paragraph", Text: "   "},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	pages, parser, chunks, err := c.ParsePDF(context.Background(), "1706.03762v1.pdf", []byte("%PDF-1.7"))
	require.NoError(t, err)
	require.Equal(t, 15, pages)
	require.Equal(t, "pdf-service-0.1.0", parser)
	require.Len(t, chunks, 2, "空文本块必须被丢弃")

	require.Equal(t, "Attention Is All You Need", chunks[0].Content)
	require.Equal(t, 1, chunks[0].PageStart)
	require.Equal(t, "heading", chunks[0].BlockType)
	require.Equal(t, []float64{0.1, 0.2, 0.9, 0.3}, chunks[0].BBox)
	require.True(t, chunks[0].IsAcademic())

	require.Equal(t, 4, chunks[1].PageStart)
	require.Equal(t, []string(nil), chunks[1].SectionPath)
}

func TestParsePDF_SplitsTooLongBlock(t *testing.T) {
	long := strings.Repeat("字", maxBlockRunes+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(response{
			PageCount: 1,
			Blocks:    []block{{Page: 1, Type: "paragraph", Text: long}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	_, _, chunks, err := c.ParsePDF(context.Background(), "a.pdf", []byte("%PDF"))
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	require.Len(t, []rune(chunks[0].Content), maxBlockRunes)
	require.Equal(t, 1, chunks[1].PageStart)
}

func TestParsePDF_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("not configured", func(t *testing.T) {
		c := NewClient("", time.Second)
		_, _, _, err := c.ParsePDF(ctx, "a.pdf", nil)
		require.ErrorContains(t, err, "not configured")
	})

	t.Run("non 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		c := NewClient(srv.URL, 5*time.Second)
		_, _, _, err := c.ParsePDF(ctx, "a.pdf", nil)
		require.ErrorContains(t, err, "returned 500")
	})

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()
		c := NewClient(srv.URL, 5*time.Second)
		_, _, _, err := c.ParsePDF(ctx, "a.pdf", nil)
		require.ErrorContains(t, err, "decode")
	})

	t.Run("page count missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"blocks":[]}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL, 5*time.Second)
		_, _, _, err := c.ParsePDF(ctx, "a.pdf", nil)
		require.ErrorContains(t, err, "page_count")
	})
}
