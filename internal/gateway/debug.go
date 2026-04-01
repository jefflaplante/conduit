package gateway

import (
	"encoding/json"
	"log"
	"net/http"
)

// handleDebugPrompt serves system prompt debug info as JSON.
func (g *Gateway) handleDebugPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionKey := r.URL.Query().Get("session")

	result, err := g.GetSystemPromptDebug(r.Context(), sessionKey)
	if err != nil {
		log.Printf("[Debug] Failed to get system prompt debug: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}
