package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/logging"
	"conduit/internal/middleware"
	"conduit/internal/tools"
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

	userID, ok := httpTestUserID(middleware.GetAuthInfo(r.Context()), req.UserID)
	if !ok {
		writeJSONError(w, http.StatusForbidden, "automation tokens may only act as their own client name")
		return
	}
	req.UserID = userID

	// Get or create session.
	session, err := g.sessions.GetOrCreateSession(req.UserID, "test")
	if err != nil {
		g.logger.Error("test message: error creating session", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Generate AI response.
	if g.ai == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	// conduit-31jg.35: shared TurnRunner (user + reply persisted inside the
	// turn lock, conduit-31jg.22). Test endpoint turns are non-interactive.
	// bd-tgy6: the turn runs for minutes, but the server's WriteTimeout is
	// 60s — extend the write deadline for the duration of the turn so the
	// server does not close the connection mid-flight. Without this, both a
	// provider error and a successful-but-slow turn end up writing to a
	// connection the server already gave up on, and the client sees an
	// empty reply (curl 52) instead of the JSON response. The 20-minute
	// budget is deliberately longer than the script clients' curl timeouts
	// (-m 900/300): a hung turn ends with a clean client-side timeout.
	writeDeadline := time.Now().Add(testMessageTurnBudget())
	if rc := http.NewResponseController(w); rc != nil {
		if err := rc.SetWriteDeadline(writeDeadline); err != nil {
			// Non-fatal: the JSON error mapping below still applies; only
			// very long turns risk hitting the original 60s deadline.
			g.logger.Warn("test message: cannot extend write deadline", "error", err)
		}
	}

	res := g.turns().Run(r.Context(), TurnRequest{
		Session:              session,
		ChannelID:            session.ChannelID,
		UserID:               req.UserID,
		Text:                 req.Message,
		NonInteractiveSource: "http_test",
	}, discardTurnSink{})
	if res.Err != nil {
		g.logger.Error("test message: error generating AI response", "error", res.Err)
		// bd-tgy6: return a structured JSON error so POST clients can
		// distinguish a failed turn from other failures. Turns that never
		// ran (client disconnect, /stop, shutdown drain) are not the
		// caller's fault and are not provider errors.
		if res.Cancelled || res.Dropped || errors.Is(res.Err, context.Canceled) || errors.Is(res.Err, context.DeadlineExceeded) {
			code := "turn_cancelled"
			if res.Dropped {
				code = "turn_dropped"
			}
			writeJSONErrorWithDetails(w, http.StatusServiceUnavailable, code,
				"agent turn did not complete", res.Err.Error())
			return
		}
		writeJSONErrorWithDetails(w, http.StatusBadGateway, "provider_error",
			"agent turn failed mid-flight", res.Err.Error())
		return
	}

	// Return response.
	var steps int
	if res.Response != nil {
		steps = res.Response.GetSteps()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"response": res.Raw,
		"usage":    res.Usage,
		"steps":    steps,
	})
}

// discardTurnSink is a TurnSink for callers that only need Run's result.
type discardTurnSink struct{}

func (discardTurnSink) Queued(context.Context)                         {}
func (discardTurnSink) Begin(context.Context) ai.StreamCallback        { return nil }
func (discardTurnSink) Progress(string)                                {}
func (discardTurnSink) ToolEvent(context.Context, tools.ToolEventInfo) {}
func (discardTurnSink) Finish(context.Context, *TurnResult)            {}

// testMessageTurnBudget is the write deadline handleTestMessage grants a
// synchronous agent turn (bd-tgy6). It exceeds the longest legitimate turn
// (LLM chain + tools) and is deliberately LONGER than the script clients'
// own curl timeouts (-m 900 / -m 300): a hung turn ends with the client
// timing out on its own — with a clear, client-side error — rather than
// the server cutting the connection mid-flight (the curl-52 failure mode).
func testMessageTurnBudget() time.Duration {
	// 20 minutes: above any observed turn duration.
	return 20 * time.Minute
}

// writeJSONErrorWithDetails writes a JSON error body with a machine-readable
// code and a free-form detail string (bd-tgy6).
func writeJSONErrorWithDetails(w http.ResponseWriter, status int, code, message, details string) {
	writeJSON(w, status, map[string]string{
		"error":   message,
		"code":    code,
		"details": details,
	})
}
