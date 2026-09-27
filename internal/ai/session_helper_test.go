package ai

import (
	"time"

	"conduit/internal/sessions"
)

// newTestSession returns a minimal in-memory session for router tests.
func newTestSession() *sessions.Session {
	return &sessions.Session{
		Key:       "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}
