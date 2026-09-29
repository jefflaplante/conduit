package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// DatabaseMaintenanceTask handles database optimization operations
type DatabaseMaintenanceTask struct {
	db     *sql.DB
	dbPath string
	config DatabaseConfig
	logger *log.Logger
}

// NewDatabaseMaintenanceTask creates a new database maintenance task
func NewDatabaseMaintenanceTask(db *sql.DB, dbPath string, config DatabaseConfig, logger *log.Logger) *DatabaseMaintenanceTask {
	if logger == nil {
		logger = log.Default()
	}

	return &DatabaseMaintenanceTask{
		db:     db,
		dbPath: dbPath,
		config: config,
		logger: logger,
	}
}

// Name returns the task name
func (t *DatabaseMaintenanceTask) Name() string {
	return "database_maintenance"
}

// Description returns the task description
func (t *DatabaseMaintenanceTask) Description() string {
	return "Perform database optimization operations (VACUUM, index optimization, etc.)"
}

// Execute runs the database maintenance task
func (t *DatabaseMaintenanceTask) Execute(ctx context.Context) TaskResult {
	if !t.config.VacuumEnabled && !t.config.OptimizeIndexes {
		return TaskResult{
			Success: true,
			Message: "Database maintenance disabled in configuration",
		}
	}

	start := time.Now()
	result := TaskResult{Success: true}
	var totalSpaceReclaimed int64

	// Check if database meets vacuum threshold
	dbSize, err := t.getDatabaseSize()
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to get database size",
			Error:   err,
		}
	}

	dbSizeMB := dbSize / (1024 * 1024)

	if t.config.DryRun {
		var would []string
		if t.config.VacuumEnabled && dbSizeMB > t.config.VacuumThreshold {
			if t.config.BackupBeforeVacuum {
				would = append(would, "backup")
			}
			would = append(would, "VACUUM")
		}
		if t.config.OptimizeIndexes {
			would = append(would, "ANALYZE")
		}
		msg := fmt.Sprintf("Dry run: database %d MB (VACUUM threshold %d MB)", dbSizeMB, t.config.VacuumThreshold)
		if len(would) > 0 {
			msg += "; would run " + strings.Join(would, ", ")
		}
		return TaskResult{Success: true, Message: msg}
	}

	// Only vacuum if database is above threshold
	if t.config.VacuumEnabled && dbSizeMB > t.config.VacuumThreshold {
		// Create backup if configured
		if t.config.BackupBeforeVacuum {
			backupResult := t.createBackup(ctx)
			if !backupResult.Success {
				return backupResult
			}
		}

		// Perform VACUUM
		vacuumResult := t.performVacuum(ctx)
		if !vacuumResult.Success {
			return vacuumResult
		}

		totalSpaceReclaimed += vacuumResult.SpaceReclaimed
	}

	// Optimize indexes if configured
	if t.config.OptimizeIndexes {
		indexResult := t.optimizeIndexes(ctx)
		if !indexResult.Success {
			return indexResult
		}
	}

	// Update result
	result.Duration = time.Since(start)
	result.SpaceReclaimed = totalSpaceReclaimed
	result.Message = fmt.Sprintf("Database maintenance completed. Database size: %.1f MB",
		float64(dbSizeMB))

	if totalSpaceReclaimed > 0 {
		result.Message += fmt.Sprintf(", Space reclaimed: %.1f MB",
			float64(totalSpaceReclaimed)/(1024*1024))
	}

	return result
}

// ShouldRun determines if the task should run
func (t *DatabaseMaintenanceTask) ShouldRun() bool {
	return t.config.VacuumEnabled || t.config.OptimizeIndexes
}

// NextRun returns when the task should run next
func (t *DatabaseMaintenanceTask) NextRun() time.Time {
	return time.Now().Add(24 * time.Hour)
}

// IsDestructive returns false since VACUUM is generally safe
func (t *DatabaseMaintenanceTask) IsDestructive() bool {
	return false
}

// getDatabaseSize returns the size of the database file in bytes
func (t *DatabaseMaintenanceTask) getDatabaseSize() (int64, error) {
	if t.dbPath == "" {
		// Try to get size from SQLite
		var size int64
		err := t.db.QueryRow("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&size)
		return size, err
	}

	// Get file size directly
	stat, err := os.Stat(t.dbPath)
	if err != nil {
		return 0, err
	}

	return stat.Size(), nil
}

// createBackup writes a VACUUM INTO backup (0600) before VACUUM. There is
// no raw file-copy fallback: copying the main file of a WAL database while
// the gateway runs does not produce a consistent copy. conduit-2cxu
func (t *DatabaseMaintenanceTask) createBackup(ctx context.Context) TaskResult {
	path, err := BackupDatabase(ctx, t.db, t.dbPath, t.config.BackupDir, time.Now())
	if err != nil {
		return TaskResult{Success: false, Message: "Backup before VACUUM failed", Error: err}
	}
	t.logger.Printf("[DatabaseMaintenance] Created backup: %s", path)
	return TaskResult{Success: true, Message: fmt.Sprintf("Created backup: %s", path)}
}

// performVacuum runs a WAL checkpoint first (lighter), then attempts VACUUM.
// If VACUUM fails with a busy error, the checkpoint already reclaimed space.
func (t *DatabaseMaintenanceTask) performVacuum(ctx context.Context) TaskResult {
	// Get initial database size
	initialSize, _ := t.getDatabaseSize()

	// Run WAL checkpoint first — lighter alternative that reclaims WAL space
	t.logger.Println("[DatabaseMaintenance] Running WAL checkpoint...")
	if _, err := t.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.logger.Printf("[DatabaseMaintenance] WAL checkpoint warning: %v", err)
	}

	t.logger.Println("[DatabaseMaintenance] Starting VACUUM operation...")

	// Use a 30-second timeout for VACUUM to avoid blocking indefinitely
	vacuumCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := t.db.ExecContext(vacuumCtx, "VACUUM")
	if err != nil {
		// If VACUUM fails due to busy/timeout, WAL checkpoint already ran — not fatal
		errMsg := err.Error()
		if ctx.Err() != nil || vacuumCtx.Err() != nil ||
			contains(errMsg, "database is locked") ||
			contains(errMsg, "SQLITE_BUSY") {
			t.logger.Printf("[DatabaseMaintenance] VACUUM skipped (database busy), WAL checkpoint already ran")
			return TaskResult{
				Success: true,
				Message: "VACUUM skipped (busy), WAL checkpoint completed",
			}
		}
		return TaskResult{
			Success: false,
			Message: "VACUUM operation failed",
			Error:   err,
		}
	}

	// Get final database size
	finalSize, _ := t.getDatabaseSize()

	spaceReclaimed := initialSize - finalSize
	if spaceReclaimed < 0 {
		spaceReclaimed = 0
	}

	t.logger.Printf("[DatabaseMaintenance] VACUUM completed. Space reclaimed: %.1f MB",
		float64(spaceReclaimed)/(1024*1024))

	return TaskResult{
		Success:        true,
		SpaceReclaimed: spaceReclaimed,
		Message:        "VACUUM operation completed successfully",
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// optimizeIndexes analyzes and optimizes database indexes
func (t *DatabaseMaintenanceTask) optimizeIndexes(ctx context.Context) TaskResult {
	t.logger.Println("[DatabaseMaintenance] Optimizing indexes...")

	// ANALYZE updates the SQLite query planner statistics
	_, err := t.db.ExecContext(ctx, "ANALYZE")
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Index analysis failed",
			Error:   err,
		}
	}

	// PRAGMA optimize performs automatic index analysis
	_, err = t.db.ExecContext(ctx, "PRAGMA optimize")
	if err != nil {
		t.logger.Printf("[DatabaseMaintenance] Warning: PRAGMA optimize failed: %v", err)
		// Don't fail the task for this
	}

	t.logger.Println("[DatabaseMaintenance] Index optimization completed")

	return TaskResult{
		Success: true,
		Message: "Index optimization completed successfully",
	}
}

// CleanupOldBackups removes database backup files older than the specified days
func (t *DatabaseMaintenanceTask) CleanupOldBackups(ctx context.Context, retentionDays int) TaskResult {
	if t.dbPath == "" {
		return TaskResult{
			Success: true,
			Message: "No database path available for backup cleanup",
		}
	}

	// Find backup files
	pattern := t.dbPath + ".backup.*"

	// This is a simplified implementation - in a real system you'd want to
	// use filepath.Glob and check file modification times
	// For now, we'll just log that we would clean up backups

	t.logger.Printf("[DatabaseMaintenance] Would cleanup backup files older than %d days matching pattern: %s",
		retentionDays, pattern)

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("Backup cleanup would process files matching: %s", pattern),
	}
}
