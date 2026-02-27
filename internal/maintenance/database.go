package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
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

// createBackup creates a backup of the database before performing VACUUM
func (t *DatabaseMaintenanceTask) createBackup(ctx context.Context) TaskResult {
	if t.dbPath == "" {
		return TaskResult{
			Success: false,
			Message: "Cannot create backup: database path not available",
		}
	}

	// Generate backup filename with timestamp
	backupPath := fmt.Sprintf("%s.backup.%s", t.dbPath, time.Now().Format("20060102-150405"))

	// Use SQLite's built-in backup API if available, otherwise copy file
	backupQuery := fmt.Sprintf("VACUUM INTO '%s'", backupPath)

	_, err := t.db.ExecContext(ctx, backupQuery)
	if err != nil {
		// Fallback to file copy
		return t.copyFileBackup(backupPath)
	}

	t.logger.Printf("[DatabaseMaintenance] Created backup: %s", backupPath)

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("Created backup: %s", backupPath),
	}
}

// copyFileBackup creates a backup by copying the database file
func (t *DatabaseMaintenanceTask) copyFileBackup(backupPath string) TaskResult {
	input, err := os.ReadFile(t.dbPath)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to read database file for backup",
			Error:   err,
		}
	}

	err = os.WriteFile(backupPath, input, 0644)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to write backup file",
			Error:   err,
		}
	}

	t.logger.Printf("[DatabaseMaintenance] Created file backup: %s", backupPath)

	return TaskResult{
		Success: true,
		Message: fmt.Sprintf("Created backup: %s", backupPath),
	}
}

// performVacuum executes the VACUUM operation
func (t *DatabaseMaintenanceTask) performVacuum(ctx context.Context) TaskResult {
	// Get initial database size
	initialSize, _ := t.getDatabaseSize()

	t.logger.Println("[DatabaseMaintenance] Starting VACUUM operation...")

	// Execute VACUUM
	_, err := t.db.ExecContext(ctx, "VACUUM")
	if err != nil {
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
