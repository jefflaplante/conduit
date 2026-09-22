package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createDailyFiles writes dated files for offsets 0..n-1 (0 = today) and
// returns their expected bundle keys.
func createDailyFiles(t *testing.T, workspace string, offsets ...int) map[int]string {
	t.Helper()
	memoryDir := filepath.Join(workspace, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))
	now := time.Now()
	keys := make(map[int]string)
	for _, off := range offsets {
		name := now.AddDate(0, 0, -off).Format("2006-01-02") + ".md"
		body := "day-" + name
		require.NoError(t, os.WriteFile(filepath.Join(memoryDir, name), []byte(body), 0644))
		keys[off] = filepath.Join("memory", name)
	}
	return keys
}

func loadBundle(t *testing.T, wc *WorkspaceContext) map[string]string {
	t.Helper()
	ctx := SecurityContext{SessionType: "main", SessionID: "test"}
	bundle, err := wc.LoadContext(context.Background(), ctx)
	require.NoError(t, err)
	return bundle.Files
}

// lookback 0 disables daily memory files entirely (config: daily_lookback_days: 0).
func TestWorkspaceContext_Lookback_ZeroDisablesDailyFiles(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)
	keys := createDailyFiles(t, workspace, 0, 1)

	wc := NewWorkspaceContextWithLookback(workspace, 0)
	files := loadBundle(t, wc)

	assert.NotContains(t, files, keys[0])
	assert.NotContains(t, files, keys[1])
	// Core files still load
	assert.Contains(t, files, "MEMORY.md")
}

// lookback 1 = today only.
func TestWorkspaceContext_Lookback_OneIncludesTodayOnly(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)
	keys := createDailyFiles(t, workspace, 0, 1, 2)

	wc := NewWorkspaceContextWithLookback(workspace, 1)
	files := loadBundle(t, wc)

	assert.Contains(t, files, keys[0])
	assert.NotContains(t, files, keys[1])
	assert.NotContains(t, files, keys[2])
}

// lookback 2 = today + yesterday (matches the legacy hardcoded behavior and
// the config default of 2).
func TestWorkspaceContext_Lookback_TwoMatchesLegacyDefault(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)
	keys := createDailyFiles(t, workspace, 0, 1, 2)

	wc := NewWorkspaceContextWithLookback(workspace, 2)
	files := loadBundle(t, wc)

	assert.Contains(t, files, keys[0])
	assert.Contains(t, files, keys[1])
	assert.NotContains(t, files, keys[2])

	// Legacy constructor must behave identically to lookback=2.
	legacy := NewWorkspaceContext(workspace)
	legacyFiles := loadBundle(t, legacy)
	assert.Contains(t, legacyFiles, keys[0])
	assert.Contains(t, legacyFiles, keys[1])
	assert.NotContains(t, legacyFiles, keys[2])
}

// lookback N spans the last N days including today.
func TestWorkspaceContext_Lookback_NDays(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)
	keys := createDailyFiles(t, workspace, 0, 1, 2, 3, 4)

	wc := NewWorkspaceContextWithLookback(workspace, 3)
	files := loadBundle(t, wc)

	assert.Contains(t, files, keys[0])
	assert.Contains(t, files, keys[1])
	assert.Contains(t, files, keys[2])
	assert.NotContains(t, files, keys[3])
	assert.NotContains(t, files, keys[4])
}

// Negative lookback falls back to the default of 2 (matches Validate()).
func TestWorkspaceContext_Lookback_NegativeFallsBackToDefault(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)
	keys := createDailyFiles(t, workspace, 0, 1, 2)

	wc := NewWorkspaceContextWithLookback(workspace, -3)
	files := loadBundle(t, wc)

	assert.Contains(t, files, keys[0])
	assert.Contains(t, files, keys[1])
	assert.NotContains(t, files, keys[2])
}

// The config-level default must remain 2 (today + yesterday).
func TestConfig_DefaultDailyLookbackIsTwo(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 2, cfg.Files.Memory.DailyLookbackDays)
}
