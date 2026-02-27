package gateway

import (
	"encoding/json"
	"net/http"

	vecgoservice "conduit/internal/vecgo"
)

// VectorAPI handles HTTP endpoints for vector search operations.
type VectorAPI struct {
	vectorService *vecgoservice.Service
}

// handleSearch handles POST /api/vector/search
// Request: {"query": "search text", "limit": 10}
// Response: {"results": [...]}
func (v *VectorAPI) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if v.vectorService == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "vector search not enabled")
		return
	}

	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Query == "" {
		writeJSONError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}

	results, err := v.vectorService.Search(r.Context(), req.Query, req.Limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "search failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
	})
}

// handleIndex handles POST /api/vector/index
// Request: {"id": "doc-1", "content": "document text", "metadata": {"key": "value"}}
// Response: {"status": "indexed"}
func (v *VectorAPI) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if v.vectorService == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "vector search not enabled")
		return
	}

	var req struct {
		ID       string            `json:"id"`
		Content  string            `json:"content"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ID == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Content == "" {
		writeJSONError(w, http.StatusBadRequest, "content is required")
		return
	}

	if err := v.vectorService.Index(r.Context(), req.ID, req.Content, req.Metadata); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "index failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "indexed",
	})
}

// handleDelete handles DELETE /api/vector/delete
// Request: {"id": "doc-1"}
// Response: {"status": "deleted"}
func (v *VectorAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if v.vectorService == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "vector search not enabled")
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ID == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}

	if err := v.vectorService.Remove(r.Context(), req.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "deleted",
	})
}

// handleStatus handles GET /api/vector/status
// Response: {"enabled": true/false}
func (v *VectorAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resp := map[string]interface{}{
		"enabled": v.vectorService != nil,
	}

	writeJSON(w, http.StatusOK, resp)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
