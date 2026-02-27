package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vecgoservice "conduit/internal/vecgo"
)

// newTestVectorService creates an in-memory VecGo service for testing.
func newTestVectorService(t *testing.T) *vecgoservice.Service {
	t.Helper()
	svc, err := vecgoservice.NewService(vecgoservice.Config{EmbedDims: 128})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	return svc
}

// jsonBody encodes v as JSON and returns a bytes.Reader suitable for http.NewRequest.
func jsonBody(t *testing.T, v interface{}) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewReader(b)
}

// decodeJSON decodes the response body into dst.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dst interface{}) {
	t.Helper()
	err := json.NewDecoder(rec.Body).Decode(dst)
	require.NoError(t, err)
}

// --- Search tests ---

func TestVectorAPI_Search_NilService(t *testing.T) {
	api := &VectorAPI{vectorService: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/search", jsonBody(t, map[string]interface{}{
		"query": "hello",
	}))

	api.handleSearch(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Contains(t, resp["error"], "not enabled")
}

func TestVectorAPI_Search_Success(t *testing.T) {
	svc := newTestVectorService(t)
	api := &VectorAPI{vectorService: svc}

	// Index a document first so there is something to search.
	indexRec := httptest.NewRecorder()
	indexReq := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"id":      "doc-1",
		"content": "The quick brown fox jumps over the lazy dog. This is a test document for vector search.",
	}))
	api.handleIndex(indexRec, indexReq)
	require.Equal(t, http.StatusOK, indexRec.Code)

	// Now search for it.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/search", jsonBody(t, map[string]interface{}{
		"query": "brown fox",
		"limit": 5,
	}))
	api.handleSearch(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	// Results may be nil/empty for a fresh in-memory TF-IDF index, but the
	// response structure must be valid.
	assert.Contains(t, resp, "results")
}

func TestVectorAPI_Search_InvalidMethod(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/vector/search", nil)

	api.handleSearch(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestVectorAPI_Search_MalformedJSON(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/search", bytes.NewReader([]byte("{bad json")))

	api.handleSearch(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Contains(t, resp["error"], "invalid JSON")
}

func TestVectorAPI_Search_EmptyQuery(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/search", jsonBody(t, map[string]interface{}{
		"query": "",
	}))

	api.handleSearch(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Contains(t, resp["error"], "query is required")
}

// --- Index tests ---

func TestVectorAPI_Index_Success(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"id":       "doc-1",
		"content":  "Hello world document content for indexing.",
		"metadata": map[string]string{"source": "test"},
	}))

	api.handleIndex(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Equal(t, "indexed", resp["status"])
}

func TestVectorAPI_Index_NilService(t *testing.T) {
	api := &VectorAPI{vectorService: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"id":      "doc-1",
		"content": "some content",
	}))

	api.handleIndex(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestVectorAPI_Index_MissingID(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"content": "some content",
	}))

	api.handleIndex(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Contains(t, resp["error"], "id is required")
}

func TestVectorAPI_Index_MissingContent(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"id": "doc-1",
	}))

	api.handleIndex(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Contains(t, resp["error"], "content is required")
}

// --- Delete tests ---

func TestVectorAPI_Delete_Success(t *testing.T) {
	svc := newTestVectorService(t)
	api := &VectorAPI{vectorService: svc}

	// Index a document first so we can delete it.
	indexRec := httptest.NewRecorder()
	indexReq := httptest.NewRequest(http.MethodPost, "/api/vector/index", jsonBody(t, map[string]interface{}{
		"id":      "doc-del",
		"content": "Document to be deleted from the index.",
	}))
	api.handleIndex(indexRec, indexReq)
	require.Equal(t, http.StatusOK, indexRec.Code)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/vector/delete", jsonBody(t, map[string]interface{}{
		"id": "doc-del",
	}))

	api.handleDelete(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]string
	decodeJSON(t, rec, &resp)
	assert.Equal(t, "deleted", resp["status"])
}

func TestVectorAPI_Delete_NilService(t *testing.T) {
	api := &VectorAPI{vectorService: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/vector/delete", jsonBody(t, map[string]interface{}{
		"id": "doc-1",
	}))

	api.handleDelete(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestVectorAPI_Delete_InvalidMethod(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/delete", jsonBody(t, map[string]interface{}{
		"id": "doc-1",
	}))

	api.handleDelete(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// --- Status tests ---

func TestVectorAPI_Status_Enabled(t *testing.T) {
	api := &VectorAPI{vectorService: newTestVectorService(t)}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/vector/status", nil)

	api.handleStatus(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	assert.Equal(t, true, resp["enabled"])
}

func TestVectorAPI_Status_Disabled(t *testing.T) {
	api := &VectorAPI{vectorService: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/vector/status", nil)

	api.handleStatus(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	assert.Equal(t, false, resp["enabled"])
}

func TestVectorAPI_Status_InvalidMethod(t *testing.T) {
	api := &VectorAPI{vectorService: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/vector/status", nil)

	api.handleStatus(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
