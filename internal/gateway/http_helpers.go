package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"conduit/internal/logging"
)

// checkOrigin returns a function that validates WebSocket Origin headers.
// If allowedOrigins is non-empty, only those origins (case-insensitive) are accepted.
// If allowedOrigins is empty, requests with no Origin header or localhost origins are accepted.
func checkOrigin(allowedOrigins []string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// No Origin header means same-origin (non-browser or same-origin browser request).
		if origin == "" {
			return true
		}

		originLower := strings.ToLower(origin)

		// If explicit allowlist is configured, check against it.
		if len(allowedOrigins) > 0 {
			for _, allowed := range allowedOrigins {
				if strings.EqualFold(origin, allowed) {
					return true
				}
			}
			logging.Warn(r.Context(), "WebSocket origin rejected",
				"origin", origin,
				"reason", "not in allowed origins")
			return false
		}

		// Default policy: allow localhost origins only.
		for _, prefix := range []string{
			"http://localhost",
			"https://localhost",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://[::1]",
			"https://[::1]",
		} {
			if originLower == prefix || strings.HasPrefix(originLower, prefix+":") {
				return true
			}
		}

		logging.Warn(r.Context(), "WebSocket origin rejected",
			"origin", origin,
			"reason", "only localhost permitted")
		return false
	}
}

// limitRequestBody wraps a handler to enforce a maximum request body size.
// Requests that exceed the limit will receive a 413 Payload Too Large error.
func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// handleChannelStatus provides channel status information.
func (g *Gateway) handleChannelStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := g.channelManager.GetStatus()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Simple JSON encoding (in production, use json.Marshal).
	response := "{\n"
	first := true
	for id, channelStatus := range status {
		if !first {
			response += ",\n"
		}
		response += fmt.Sprintf(`  "%s": {
    "status": "%s",
    "message": "%s",
    "timestamp": "%s"
  }`, id, channelStatus.Status, channelStatus.Message, channelStatus.Timestamp.Format(time.RFC3339))
		first = false
	}
	response += "\n}"

	w.Write([]byte(response))
}

// handleTestMessage provides a test endpoint for sending messages without Telegram.
func (g *Gateway) handleTestMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body.
	var req struct {
		Message string `json:"message"`
		UserID  string `json:"user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		req.UserID = "test_user"
	}

	// Get or create session.
	session, err := g.sessions.GetOrCreateSession(req.UserID, "test")
	if err != nil {
		g.logger.Error("test message: error creating session", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add user message to session.
	_, err = g.sessions.AddMessage(session.Key, "user", req.Message, nil)
	if err != nil {
		g.logger.Error("test message: error saving user message", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Generate AI response.
	if g.ai == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	// Use GenerateResponseWithTools to enable tool execution.
	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]
	convResponse, err := g.ai.GenerateResponseWithTools(ctx, session, req.Message, providerOverride, modelOverride)
	if err != nil {
		g.logger.Error("test message: error generating AI response", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add AI response to session.
	_, err = g.sessions.AddMessage(session.Key, "assistant", convResponse.GetContent(), nil)
	if err != nil {
		g.logger.Error("error saving AI message", "error", err)
	}

	// Return response.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"response": convResponse.GetContent(),
		"usage":    convResponse.GetUsage(),
		"steps":    convResponse.GetSteps(),
	})
}
