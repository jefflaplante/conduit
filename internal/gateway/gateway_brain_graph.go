package gateway

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"conduit/internal/brain"
)

// handleBrainGraph serves GET /api/brain/graph for the memory-graph dashboard.
//
//	GET /api/brain/graph?source_prefix=&min_salience=&min_confidence=&truncate=&limit=
//
// Returns 503 when brain is disabled, 404 when the dashboard is gated off.
func (g *Gateway) handleBrainGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !g.config.Brain.DashboardEnabled {
		writeJSONError(w, http.StatusNotFound, "brain dashboard not enabled")
		return
	}
	if g.brainService == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "brain disabled")
		return
	}

	q := r.URL.Query()
	opts := brain.GraphOptions{
		SourcePrefix:  q.Get("source_prefix"),
		ValueTruncate: 200,
		NodeLimit:     5000,
	}
	if v := q.Get("min_salience"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			opts.MinSalience = f
		}
	}
	if v := q.Get("min_confidence"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			opts.MinConfidence = f
		}
	}
	if v := q.Get("truncate"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.ValueTruncate = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			opts.NodeLimit = n
		}
	}

	graph, err := g.brainService.ListGraph(r.Context(), opts)
	if err != nil {
		log.Printf("[BrainGraph] ListGraph failed: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "list graph failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": graph.Nodes,
		"edges": graph.Edges,
		"counts": map[string]int{
			"nodes": len(graph.Nodes),
			"edges": len(graph.Edges),
		},
		"generated_at": time.Now().UTC(),
	})
}
