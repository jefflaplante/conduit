package monitoring

import (
	"context"
	"database/sql"

	"conduit/internal/sessions"
)

// SessionStoreInterface defines the session store methods used by the metrics collector
// This allows for easy mocking in tests
type SessionStoreInterface interface {
	// GetSessionStateMetrics returns current session state metrics
	GetSessionStateMetrics() sessions.SessionStateMetrics

	// DetectStuckSessions identifies sessions that appear stuck
	DetectStuckSessions(config sessions.StuckSessionConfig) []sessions.StuckSessionInfo

	// DB returns a database interface for health checks
	DB() *sql.DB

	// ListActiveSessions lists sessions for reporting
	ListActiveSessions(limit int) ([]sessions.Session, error)
}

// DBInterface defines the database methods needed for health checks (for mocking)
type DBInterface interface {
	PingContext(ctx context.Context) error
	QueryRowContext(ctx context.Context, query string, args ...interface{}) RowInterface
}

// RowInterface defines the row scanning interface
type RowInterface interface {
	Scan(dest ...interface{}) error
}
