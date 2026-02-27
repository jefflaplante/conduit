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

// newTestEmbedder creates an OpenAIEmbedder pointed at the given test server URL.
func newTestEmbedder(serverURL string) *OpenAIEmbedder {
	e := NewOpenAIEmbedder("test-api-key", "text-embedding-3-small", 3)
	e.baseURL = serverURL + "/v1/embeddings"
	return e
}

func TestOpenAIEmbedder_Embed_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req openAIEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "text-embedding-3-small", req.Model)
		assert.Len(t, req.Input, 1)
		assert.Equal(t, "hello world", req.Input[0])

		resp := openAIEmbedResponse{
			Data: []openAIEmbedData{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"hello world"})
	require.NoError(t, err)
	require.Len(t, vectors, 1)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vectors[0])
}

func TestOpenAIEmbedder_Embed_Batch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Len(t, req.Input, 3)

		resp := openAIEmbedResponse{
			Data: []openAIEmbedData{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
				{Embedding: []float32{0.7, 0.8, 0.9}, Index: 2},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"one", "two", "three"})
	require.NoError(t, err)
	require.Len(t, vectors, 3)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, vectors[0])
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, vectors[1])
	assert.Equal(t, []float32{0.7, 0.8, 0.9}, vectors[2])
}

func TestOpenAIEmbedder_Embed_RateLimit(t *testing.T) {
	var callCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count == 1 {
			// First call: return 429
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error": "rate limited"}`))
			return
		}
		// Subsequent calls: success
		resp := openAIEmbedResponse{
			Data: []openAIEmbedData{
				{Embedding: []float32{1.0, 2.0, 3.0}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	e := newTestEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"retry me"})
	require.NoError(t, err)
	require.Len(t, vectors, 1)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, vectors[0])
	assert.GreaterOrEqual(t, callCount.Load(), int32(2), "should have retried at least once")
}

func TestOpenAIEmbedder_Embed_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	e := newTestEmbedder(server.URL)
	vectors, err := e.Embed(context.Background(), []string{"will fail"})
	assert.Error(t, err)
	assert.Nil(t, vectors)
	assert.Contains(t, err.Error(), "API error 401")
}

func TestOpenAIEmbedder_Embed_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response — the context should cancel before this returns.
		<-r.Context().Done()
	}))
	defer server.Close()

	e := newTestEmbedder(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	vectors, err := e.Embed(ctx, []string{"cancel me"})
	assert.Error(t, err)
	assert.Nil(t, vectors)
}

func TestOpenAIEmbedder_Embed_EmptyInput(t *testing.T) {
	e := NewOpenAIEmbedder("key", "", 0)
	vectors, err := e.Embed(context.Background(), []string{})
	require.NoError(t, err)
	assert.Nil(t, vectors)
}

func TestOpenAIEmbedder_Name(t *testing.T) {
	e := NewOpenAIEmbedder("key", "text-embedding-3-large", 3072)
	assert.Equal(t, "openai:text-embedding-3-large", e.Name())

	e2 := NewOpenAIEmbedder("key", "", 0)
	assert.Equal(t, "openai:text-embedding-3-small", e2.Name())
}

func TestOpenAIEmbedder_Dimensions(t *testing.T) {
	e := NewOpenAIEmbedder("key", "", 512)
	assert.Equal(t, 512, e.Dimensions())

	e2 := NewOpenAIEmbedder("key", "", 0)
	assert.Equal(t, 1536, e2.Dimensions())
}

func TestOpenAIEmbedder_Defaults(t *testing.T) {
	e := NewOpenAIEmbedder("key", "", 0)
	assert.Equal(t, "text-embedding-3-small", e.model)
	assert.Equal(t, 1536, e.dimensions)
	assert.Equal(t, openAIEmbedURL, e.baseURL)
}
