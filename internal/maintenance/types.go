package maintenance

import (
	"context"
	"time"
)

// Task represents a maintenance task that can be scheduled and executed
type Task interface {
	// Name returns the name of the maintenance task
	Name() string

	// Description returns a human-readable description of what the task does
	Description() string

	// Execute runs the maintenance task
	Execute(ctx context.Context) TaskResult

	// ShouldRun determines if the task should run based on its schedule
	ShouldRun() bool

	// NextRun returns when the task should run next
	NextRun() time.Time

	// IsDestructive returns true if the task performs destructive operations
	IsDestructive() bool
}

// TaskResult represents the result of executing a maintenance task
type TaskResult struct {
	Success          bool          `json:"success"`
	Duration         time.Duration `json:"duration"`
	Message          string        `json:"message"`
	RecordsProcessed int           `json:"records_processed,omitempty"`
	SpaceReclaimed   int64         `json:"space_reclaimed,omitempty"`
	Error            error         `json:"error,omitempty"`
}

// TaskStatus represents the status of a maintenance task
type TaskStatus struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	LastRun     time.Time  `json:"last_run"`
	NextRun     time.Time  `json:"next_run"`
	LastResult  TaskResult `json:"last_result"`
	Enabled     bool       `json:"enabled"`
	Schedule    string     `json:"schedule"`
}

// Config represents maintenance configuration
type Config struct {
	Enabled  bool   `json:"enabled"`
	Schedule string `json:"schedule"` // cron expression, default "0 2 * * *" (daily 2 AM)

	// Session cleanup configuration
	Sessions SessionConfig `json:"sessions"`

	// Database maintenance configuration
	Database DatabaseConfig `json:"database"`

	// Maintenance window configuration
	Window WindowConfig `json:"window"`
}

// SessionConfig configures session cleanup and summarization
type SessionConfig struct {
	RetentionDays        int  `json:"retention_days"`         // default 30 days
	SummarizeOld         bool `json:"summarize_old"`          // default true
	SummaryRetentionDays int  `json:"summary_retention_days"` // default 365 days
	CleanupEnabled       bool `json:"cleanup_enabled"`        // default true
}

// DatabaseConfig configures database maintenance operations
type DatabaseConfig struct {
	VacuumEnabled      bool  `json:"vacuum_enabled"`       // default true
	VacuumThreshold    int64 `json:"vacuum_threshold"`     // vacuum when DB > threshold MB
	BackupBeforeVacuum bool  `json:"backup_before_vacuum"` // default true
	OptimizeIndexes    bool  `json:"optimize_indexes"`     // default true
}

// WindowConfig defines maintenance windows to avoid peak usage
type WindowConfig struct {
	StartHour int    `json:"start_hour"` // default 2 (2 AM)
	EndHour   int    `json:"end_hour"`   // default 6 (6 AM)
	TimeZone  string `json:"time_zone"`  // default "UTC"
}

// DefaultConfig returns the default maintenance configuration
func DefaultConfig() Config {
	return Config{
		Enabled:  true,
		Schedule: "0 2 * * *", // Daily at 2 AM
		Sessions: SessionConfig{
			RetentionDays:        30,
			SummarizeOld:         true,
			SummaryRetentionDays: 365,
			CleanupEnabled:       true,
		},
		Database: DatabaseConfig{
			VacuumEnabled:      true,
			VacuumThreshold:    100, // 100 MB
			BackupBeforeVacuum: true,
			OptimizeIndexes:    true,
		},
		Window: WindowConfig{
			StartHour: 2,
			EndHour:   6,
			TimeZone:  "UTC",
		},
	}
}
