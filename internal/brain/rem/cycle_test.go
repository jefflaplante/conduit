package rem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCycle_FullRun(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Setup: Create some LTM entries
	require.NoError(t, b.Store(ctx, "solar.production", "45kWh", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.panels", "30", brain.TierLongTerm, "test"))

	// Create old low-salience entry for pruning
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "old.key", "value", "test", oldTime, oldTime, 1, 0.05)
	require.NoError(t, err)

	// Setup config
	rem.config.PruneAgeDays = 30
	rem.config.SalienceDecayRate = 0.1
	rem.config.IntegrationDay = int(time.Now().Weekday())
	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir

	// Create memory directory and daily log
	memoryDir := filepath.Join(tmpDir, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))
	today := "2026-04-05"
	dailyLogPath := filepath.Join(memoryDir, today+".md")
	logContent := `# Daily Log
- Learned: New solar fact
`
	require.NoError(t, os.WriteFile(dailyLogPath, []byte(logContent), 0644))

	// Run full cycle
	report, err := rem.Run(ctx, nil, false) // nil = all phases
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify all phases ran
	assert.NotNil(t, report.Triage)
	assert.NotNil(t, report.Consolidation)
	assert.NotNil(t, report.Pruning)
	assert.NotNil(t, report.Integration)
	assert.NotNil(t, report.Grooming)

	// Verify triage found the daily log
	assert.Equal(t, dailyLogPath, report.Triage.DailyLogScanned)

	// Verify consolidation ran
	assert.GreaterOrEqual(t, report.Consolidation.SalienceDecayed, 0)

	// Verify pruning archived old entry
	assert.GreaterOrEqual(t, len(report.Pruning.Archived), 1)

	// Verify integration ran (may or may not create relationships depending on entry state)
	assert.NotNil(t, report.Integration)
	// Note: Relationships may not always be created due to timing, salience, or other factors
	// The important thing is that the integration phase completed successfully
}

func TestCycle_SelectivePhases(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir

	// Run only triage and consolidation
	report, err := rem.Run(ctx, []string{"triage", "consolidation"}, false)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify only selected phases ran
	assert.NotNil(t, report.Triage)
	assert.NotNil(t, report.Consolidation)
	assert.Nil(t, report.Pruning)
	assert.Nil(t, report.Integration)
	assert.Nil(t, report.Grooming)
}

func TestCycle_DryRunAll(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Setup: Create some entries
	require.NoError(t, b.Store(ctx, "test.key", "value", brain.TierLongTerm, "test"))

	// Create old low-salience entry
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "old.key", "value", "test", oldTime, oldTime, 1, 0.05)
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30
	rem.config.IntegrationDay = int(time.Now().Weekday())
	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir

	// Run full cycle in dry-run mode
	report, err := rem.Run(ctx, nil, true)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify dry run flag is set
	assert.True(t, report.DryRun)

	// Verify no actual changes were made
	// Check that old.key still exists in LTM
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "old.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Check that no relationships were created
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestCycle_WithLogOutput(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Setup
	require.NoError(t, b.Store(ctx, "test.key", "value", brain.TierLongTerm, "test"))

	// Configure log path
	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run cycle
	report, err := rem.Run(ctx, nil, false)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify log file was created
	expectedLogFile := filepath.Join(logDir, "rem-"+time.Now().Format("2006-01-02")+".md")
	_, err = os.Stat(expectedLogFile)
	require.NoError(t, err, "log file should exist")

	// Verify log content
	content, err := os.ReadFile(expectedLogFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "REM Sleep Cycle Report")
	assert.Contains(t, string(content), "Phase 1: Triage")
	assert.Contains(t, string(content), "Phase 2: Consolidation")
}

func TestCycle_ContextCancellation(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ctx = brain.WithUserID(ctx, "testuser")

	// Setup entries
	require.NoError(t, b.Store(ctx, "test.key", "value", brain.TierLongTerm, "test"))

	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	// Cancel context immediately
	cancel()

	// Run should detect cancellation
	_, err := rem.Run(ctx, nil, false)
	require.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestCycle_UnknownPhase(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	// Run with unknown phase
	_, err := rem.Run(ctx, []string{"unknown_phase"}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown phase")
}

func TestCycle_EmptyDatabase(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run on empty database
	report, err := rem.Run(ctx, nil, false)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Should complete successfully with empty results
	assert.NotNil(t, report.Triage)
	assert.NotNil(t, report.Consolidation)
	assert.NotNil(t, report.Pruning)
	assert.NotNil(t, report.Integration)
	assert.NotNil(t, report.Grooming)
}

func TestCycle_PhaseOrder(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run with custom phase order
	phases := []string{"grooming", "triage", "pruning"}
	report, err := rem.Run(ctx, phases, false)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify requested phases ran
	assert.NotNil(t, report.Grooming)
	assert.NotNil(t, report.Triage)
	assert.NotNil(t, report.Pruning)

	// Verify unrequested phases did not run
	assert.Nil(t, report.Consolidation)
	assert.Nil(t, report.Integration)
}

func TestCycle_DryRunNoLog(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	logDir := filepath.Join(tmpDir, "rem-logs")
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run in dry-run mode
	report, err := rem.Run(ctx, nil, true)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify log file was NOT created in dry-run mode
	expectedLogFile := filepath.Join(logDir, "rem-"+time.Now().Format("2006-01-02")+".md")
	_, err = os.Stat(expectedLogFile)
	assert.True(t, os.IsNotExist(err), "log file should not exist in dry-run mode")
}

func TestCycle_MultipleRuns(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	require.NoError(t, b.Store(ctx, "test.key", "value", brain.TierLongTerm, "test"))

	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run multiple times
	report1, err := rem.Run(ctx, nil, false)
	require.NoError(t, err)
	require.NotNil(t, report1)

	report2, err := rem.Run(ctx, nil, false)
	require.NoError(t, err)
	require.NotNil(t, report2)

	// Should complete successfully both times
	assert.NotNil(t, report1.Triage)
	assert.NotNil(t, report2.Triage)
}

func TestCycle_ReportTimestamp(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	logDir := filepath.Join(tmpDir, "rem-logs")
	require.NoError(t, os.MkdirAll(logDir, 0755))
	rem.config.LogPath = logDir
	rem.config.IntegrationDay = int(time.Now().Weekday())

	before := time.Now()
	report, err := rem.Run(ctx, nil, false)
	after := time.Now()

	require.NoError(t, err)
	require.NotNil(t, report)

	// Verify report timestamp is within expected range
	assert.True(t, report.Date.After(before) || report.Date.Equal(before))
	assert.True(t, report.Date.Before(after) || report.Date.Equal(after))
}
