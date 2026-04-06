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

func TestTriage_EmptyLog(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	// Override config to point to tmpDir for memory directory
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	ctx := context.Background()

	result, err := rem.Triage(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Daily log won't exist, so NewFacts should be empty
	assert.Empty(t, result.NewFacts)
	assert.Empty(t, result.UpdatedFacts)
	assert.Empty(t, result.StaleCandidates)
}

func TestTriage_WithDailyLog(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Create memory directory and daily log file
	memoryDir := filepath.Join(tmpDir, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))

	// Override config to use tmpDir
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	// Create today's daily log with facts
	today := time.Now().Format("2006-01-02")
	dailyLogPath := filepath.Join(memoryDir, today+".md")
	logContent := `# Daily Log - ` + today + `

## Activities
- Learned: Jeff's favorite bourbon is Maker's Mark
- Noted: Solar production today was 45kWh
- Updated: panel_count is now 32 (was 30)
- Remembered: Theo is a golden retriever
`
	require.NoError(t, os.WriteFile(dailyLogPath, []byte(logContent), 0644))

	result, err := rem.Triage(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect new facts
	assert.GreaterOrEqual(t, len(result.NewFacts), 3, "should find at least 3 new facts")
	assert.Contains(t, result.NewFacts, "Jeff's favorite bourbon is Maker's Mark")
	assert.Contains(t, result.NewFacts, "Solar production today was 45kWh")
	assert.Contains(t, result.NewFacts, "Theo is a golden retriever")

	// Should detect updated facts
	assert.GreaterOrEqual(t, len(result.UpdatedFacts), 1, "should find at least 1 updated fact")
	assert.Contains(t, result.UpdatedFacts, "panel_count is now 32 (was 30)")

	// Should have scanned the daily log
	assert.Equal(t, dailyLogPath, result.DailyLogScanned)
}

func TestTriage_WithWorkingMemory(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store some entries in working memory
	require.NoError(t, b.Store(ctx, "solar.production", "45kWh", brain.TierWorking, "test"))
	require.NoError(t, b.Store(ctx, "pets.name", "Theo", brain.TierWorking, "test"))
	require.NoError(t, b.Store(ctx, "bourbon.favorite", "Maker's Mark", brain.TierWorking, "test"))

	// Create memory directory and daily log that mentions some WM keys
	memoryDir := filepath.Join(tmpDir, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	today := time.Now().Format("2006-01-02")
	dailyLogPath := filepath.Join(memoryDir, today+".md")
	logContent := `# Daily Log - ` + today + `

- Learned: solar.production reached new high
- Updated: bourbon.favorite confirmed
`
	require.NoError(t, os.WriteFile(dailyLogPath, []byte(logContent), 0644))

	result, err := rem.Triage(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should find WM entries
	assert.Equal(t, 3, result.WMKeysFound)

	// Should detect facts from log
	assert.GreaterOrEqual(t, len(result.NewFacts), 1)
	assert.GreaterOrEqual(t, len(result.UpdatedFacts), 1)
}

func TestTriage_StaleCandidates(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store an old entry in LTM by directly inserting into DB
	// (simulating an entry not accessed in 40 days)
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, datetime('now', '-40 days'), datetime('now', '-40 days'), 1, 0.5)
	`, "old.key", "old value", "test")
	require.NoError(t, err)

	// Store a recent entry
	require.NoError(t, b.Store(ctx, "new.key", "new value", brain.TierLongTerm, "test"))

	// Set PruneAgeDays to 30
	rem.config.PruneAgeDays = 30
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	result, err := rem.Triage(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should find the old key as a stale candidate
	assert.GreaterOrEqual(t, len(result.StaleCandidates), 1)
	assert.Contains(t, result.StaleCandidates, "old.key")
}

func TestTriage_DryRun(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Create memory directory and daily log
	memoryDir := filepath.Join(tmpDir, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	today := time.Now().Format("2006-01-02")
	dailyLogPath := filepath.Join(memoryDir, today+".md")
	logContent := `# Daily Log
- Learned: test fact
`
	require.NoError(t, os.WriteFile(dailyLogPath, []byte(logContent), 0644))

	// Dry run should still scan and report findings
	result, err := rem.Triage(ctx, true)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect facts even in dry run
	assert.GreaterOrEqual(t, len(result.NewFacts), 1)
}

func TestTriage_NoStaleCandidates(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store recent entries only
	require.NoError(t, b.Store(ctx, "recent.key1", "value1", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "recent.key2", "value2", brain.TierLongTerm, "test"))

	rem.config.PruneAgeDays = 30
	rem.config.LogPath = filepath.Join(tmpDir, "rem-logs")

	result, err := rem.Triage(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should not find any stale candidates
	assert.Empty(t, result.StaleCandidates)
}
