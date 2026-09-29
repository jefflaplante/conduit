package maintenance

import (
	"context"
	"encoding/json"
	"time"

	"conduit/internal/config"
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
	// Details carries task-specific output (session_cleanup: *PruneReport).
	Details any `json:"details,omitempty"`
}

// MarshalJSON renders Error as its message (an error value marshals as {}).
func (r TaskResult) MarshalJSON() ([]byte, error) {
	type plain TaskResult
	out := struct {
		plain
		Error string `json:"error,omitempty"`
	}{plain: plain(r)}
	if r.Error != nil {
		out.Error = r.Error.Error()
	}
	return json.Marshal(out)
}

// TaskStatus represents the status of a maintenance task
type TaskStatus struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	LastRun     time.Time  `json:"last_run"`
	NextRun     time.Time  `json:"next_run"`
	LastResult  TaskResult `json:"last_result"`
	Enabled     bool       `json:"enabled"`
}

// Config represents maintenance configuration. There is no schedule or
// maintenance window: tasks run when `conduit maintenance run` is invoked
// (conduit-3kgo).
type Config struct {
	// Session cleanup configuration
	Sessions SessionConfig `json:"sessions"`

	// Database maintenance configuration
	Database DatabaseConfig `json:"database"`
}

// SessionConfig configures session cleanup (conduit-2cxu): only sessions
// whose key starts with one of PrunablePrefixes are deleted.
type SessionConfig struct {
	RetentionDays     int      `json:"retention_days"`      // default 30 days
	PrunablePrefixes  []string `json:"prunable_prefixes"`   // default cron_, heartbeat_, subagent_, test_
	BatchSize         int      `json:"batch_size"`          // sessions per transaction, default 500
	CleanupEnabled    bool     `json:"cleanup_enabled"`     // default true
	BackupBeforePrune bool     `json:"backup_before_prune"` // default true
	BackupDir         string   `json:"backup_dir,omitempty"`
	DryRun            bool     `json:"-"` // plan only (--dry-run)
}

// DatabaseConfig configures database maintenance operations
type DatabaseConfig struct {
	VacuumEnabled      bool   `json:"vacuum_enabled"`       // default true
	VacuumThreshold    int64  `json:"vacuum_threshold"`     // vacuum when DB > threshold MB
	BackupBeforeVacuum bool   `json:"backup_before_vacuum"` // default true
	OptimizeIndexes    bool   `json:"optimize_indexes"`     // default true
	BackupDir          string `json:"backup_dir,omitempty"`
	DryRun             bool   `json:"-"` // report only (--dry-run)
}

// DefaultConfig returns the default maintenance configuration
func DefaultConfig() Config {
	return Config{
		Sessions: SessionConfig{
			RetentionDays:     DefaultRetentionDays,
			PrunablePrefixes:  append([]string(nil), config.DefaultPrunableSessionPrefixes...),
			BatchSize:         DefaultPruneBatchSize,
			CleanupEnabled:    true,
			BackupBeforePrune: true,
		},
		Database: DatabaseConfig{
			VacuumEnabled:      true,
			VacuumThreshold:    100, // 100 MB
			BackupBeforeVacuum: true,
			OptimizeIndexes:    true,
		},
	}
}
