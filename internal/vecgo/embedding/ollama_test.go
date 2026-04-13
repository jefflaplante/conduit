package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestOllamaEmbedder creates an OllamaEmbedder pointed at the given test server URL.
func newTestOllamaEmbedder(serverURL string) *OllamaEmbedder {
	e := NewOllamaEmbedder(serverURL, "nomic-embed-text", 3)
	return e
}

func TestOllamaEmbedder_Embed_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embed", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req ollamaEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Len(t, req.Input, 1)
		assert.Equal(t, "hello world", req.Input[0])

		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: [][]float32{{0.1, 0.2, 0.3}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestOllamaEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"hello world"})
	require.NoError(t, err)
	require.Len(t, vectors, 1)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vectors[0])
}

func TestOllamaEmbedder_Embed_Batch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Len(t, req.Input, 3)

		resp := ollamaEmbedResponse{
			Model: "nomic-embed-text",
			Embeddings: [][]float32{
				{0.1, 0.2, 0.3},
				{0.4, 0.5, 0.6},
				{0.7, 0.8, 0.9},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestOllamaEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"one", "two", "three"})
	require.NoError(t, err)
	require.Len(t, vectors, 3)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vectors[0])
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, vectors[1])
	assert.Equal(t, []float32{0.7, 0.8, 0.9}, vectors[2])
}

func TestOllamaEmbedder_Embed_RetryOnServerError(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal error"}`))
			return
		}
		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: [][]float32{{1.0, 2.0, 3.0}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestOllamaEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"retry me"})
	require.NoError(t, err)
	require.Len(t, vectors, 1)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, vectors[0])
	assert.GreaterOrEqual(t, callCount.Load(), int32(2), "should have retried at least once")
}

func TestOllamaEmbedder_Embed_ClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "model not found"}`))
	}))
	defer server.Close()

	e := newTestOllamaEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"will fail"})
	assert.Error(t, err)
	assert.Nil(t, vectors)
	assert.Contains(t, err.Error(), "API error 404")
}

func TestOllamaEmbedder_Embed_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	e := newTestOllamaEmbedder(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	vectors, err := e.Embed(ctx, []string{"cancel me"})
	assert.Error(t, err)
	assert.Nil(t, vectors)
}

func TestOllamaEmbedder_Embed_EmptyInput(t *testing.T) {
	e := NewOllamaEmbedder("", "", 0)
	vectors, err := e.Embed(context.Background(), []string{})
	require.NoError(t, err)
	assert.Nil(t, vectors)
}

func TestOllamaEmbedder_Name(t *testing.T) {
	e := NewOllamaEmbedder("", "mxbai-embed-large", 1024)
	assert.Equal(t, "ollama:mxbai-embed-large", e.Name())

	e2 := NewOllamaEmbedder("", "", 0)
	assert.Equal(t, "ollama:nomic-embed-text", e2.Name())
}

func TestOllamaEmbedder_Dimensions(t *testing.T) {
	e := NewOllamaEmbedder("", "", 512)
	assert.Equal(t, 512, e.Dimensions())

	e2 := NewOllamaEmbedder("", "", 0)
	assert.Equal(t, 768, e2.Dimensions())
}

func TestOllamaEmbedder_Defaults(t *testing.T) {
	e := NewOllamaEmbedder("", "", 0)
	assert.Equal(t, "nomic-embed-text", e.model)
	assert.Equal(t, 768, e.dimensions)
	assert.Equal(t, defaultOllamaHost, e.host)
}
