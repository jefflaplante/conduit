package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/approval"
	"conduit/internal/channels"
)

// handleReflectiveSessionEnd handles /goodbye and /end commands: it sends the
// reflection prompt to the model so it can assess the session, then writes
// Go-computed metrics and clears the session.
//
// conduit-31jg.66: the reflection turn runs on the shared TurnRunner, so the
// prompt and reply are persisted inside the session's turn lock, the turn is
// /stop-able, and the metrics + session clear happen inside the same lock
// (goodbyeTurnSink.Finish) — a message queued behind /goodbye starts on the
// cleared session instead of racing the clear.
func (g *Gateway) handleReflectiveSessionEnd(ctx context.Context, client *Client, sessionKey string, sendResponse func(string)) {
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		sendResponse("Could not retrieve session.")
		return
	}

	// If reflection is available and the session has enough history, let
	// the model reflect before we tear down the context.
	if g.cognition.ReflectionEnabled() && session.MessageCount > 2 {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			// Short timeout to avoid blocking the client if the model is slow.
			reflCtx, reflCancel := context.WithTimeout(ctx, 30*time.Second)
			defer reflCancel()

			userID := client.UserID
			if userID == "" {
				userID = client.Role
			}
			sink := &goodbyeTurnSink{wsTurnSink: wsTurnSink{
				g: g, client: client, sessionKey: sessionKey,
				requestID: fmt.Sprintf("refl_%d", time.Now().UnixNano()),
			}}
			g.turns().Run(reflCtx, TurnRequest{
				Session:   session,
				ChannelID: session.ChannelID,
				UserID:    userID,
				Text:      reflPrompt,
				// Transcript shows what the user typed, not the internal
				// prompt (matters if the reflection is stopped and the
				// session continues).
				StoreText: "/goodbye",
				Origin: &approval.Origin{ // conduit-31jg.43: the user typed /goodbye
					Source: "websocket", ChannelID: session.ChannelID, UserID: userID,
					SessionKey: sessionKey, Notify: g.wsApprovalNotifier(client, sessionKey),
				},
				SanitizeStored: true,
				SkipReflection: true, // this turn IS the reflection
			}, sink)
			switch {
			case !sink.ran:
				sendResponse("Session end cancelled.")
			case sink.clearErr != nil:
				sendResponse("Session reflection complete, but failed to clear session.")
			default:
				sendResponse("Session reflection complete. Goodbye!")
			}
			return
		}
	} else if g.cognition.ReflectionEnabled() {
		// Session too short for model reflection — write Go-only metrics
		reflCtx, reflCancel := context.WithTimeout(ctx, 5*time.Second)
		g.reflectOnSessionEnd(reflCtx, sessionKey)
		reflCancel()
	}

	if err := g.clearEndedSession(sessionKey); err != nil {
		sendResponse("Session reflection complete, but failed to clear session.")
		return
	}
	sendResponse("Session reflection complete. Goodbye!")
}

// clearEndedSession clears the transcript and per-session usage context
// (same as /reset) after /goodbye.
func (g *Gateway) clearEndedSession(sessionKey string) error {
	if err := g.sessions.ClearSessionMessages(sessionKey); err != nil {
		log.Printf("Error clearing session after /goodbye: %v", err)
		return err
	}
	_ = g.sessions.SetSessionContextBatch(sessionKey, map[string]string{
		"last_prompt_tokens":        "",
		"last_completion_tokens":    "",
		"last_total_tokens":         "",
		"session_total_cost":        "",
		"session_request_count":     "",
		"session_unpriced_requests": "", // conduit-31jg.57
	})
	return nil
}

// goodbyeTurnSink renders the /goodbye reflection turn like a WS chat turn
// and, inside the turn lock, writes the session metrics and clears the
// session (conduit-31jg.66). As before, a failed (or timed-out) reflection
// still ends the session; a turn dropped before it ran (/stop while queued,
// shutdown) or stopped with /stop does not.
type goodbyeTurnSink struct {
	wsTurnSink
	ran      bool
	clearErr error
}

func (s *goodbyeTurnSink) Finish(ctx context.Context, res *TurnResult) {
	if res.Dropped {
		return
	}
	if res.Cancelled {
		s.streamEnd("", nil) // Begin sent StreamStart; close the client's stream
		return
	}
	s.ran = true
	content := ""
	if res.Delivered() {
		content = channels.SanitizeOutgoingText(res.Content)
	} else if res.Err != nil {
		log.Printf("SPAR reflection: session-end reflection failed for %s: %v", s.sessionKey, res.Err)
	}
	s.streamEnd(content, res)

	// Compute and write session metrics (after the reply was stored).
	metricsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	if updated, err := s.g.sessions.GetSession(s.sessionKey); err == nil {
		s.g.reflectHighConfidencePost(metricsCtx, updated)
	}
	cancel()
	log.Printf("SPAR reflection: session-end reflection completed for %s", s.sessionKey)

	s.clearErr = s.g.clearEndedSession(s.sessionKey)
}
