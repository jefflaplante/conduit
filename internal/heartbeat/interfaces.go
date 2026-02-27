package heartbeat

import (
	"context"

	"conduit/internal/scheduler"
	"conduit/internal/sessions"
)

// HeartbeatIntegrationInterface defines the interface for heartbeat integration.
// This allows for mock implementations in tests.
type HeartbeatIntegrationInterface interface {
	// ExecuteHeartbeat executes a heartbeat job
	ExecuteHeartbeat(ctx context.Context, job *scheduler.Job) error

	// ScheduleHeartbeatJob schedules a new heartbeat job in the scheduler
	ScheduleHeartbeatJob(schedule, target, model string, enabled bool) error

	// GetHeartbeatJobCount returns the number of active heartbeat jobs
	GetHeartbeatJobCount() int

	// RemoveHeartbeatJobs removes all heartbeat jobs from the scheduler
	RemoveHeartbeatJobs() error
}

// SessionStoreInterface defines the session store methods needed by heartbeat executor
type SessionStoreInterface interface {
	GetOrCreateSession(userID, channelID string) (*sessions.Session, error)
}

// Verify that GatewayIntegration implements HeartbeatIntegrationInterface
var _ HeartbeatIntegrationInterface = (*GatewayIntegration)(nil)
