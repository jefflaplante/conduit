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

func TestWorkspaceContext_LoadContext_SecurityFiltering(t *testing.T) {
	// Setup test workspace
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)

	wsContext := NewWorkspaceContext(workspace)

	// Test main session - should include MEMORY.md
	mainCtx := SecurityContext{
		SessionType: "main",
		SessionID:   "test-main",
	}
	bundle, err := wsContext.LoadContext(context.Background(), mainCtx)
	assert.NoError(t, err)
	assert.Contains(t, bundle.Files, "MEMORY.md")
	assert.Contains(t, bundle.Files, "SOUL.md")
	assert.Contains(t, bundle.Files, "USER.md")
	assert.Contains(t, bundle.Files, "AGENTS.md")
	assert.Equal(t, "main", bundle.Metadata["session_type"])

	// Test shared session - should exclude MEMORY.md and HEARTBEAT.md
	sharedCtx := SecurityContext{
		SessionType: "shared",
		SessionID:   "test-shared",
	}
	bundle, err = wsContext.LoadContext(context.Background(), sharedCtx)
	assert.NoError(t, err)
	assert.NotContains(t, bundle.Files, "MEMORY.md")
	assert.NotContains(t, bundle.Files, "HEARTBEAT.md")
	assert.Contains(t, bundle.Files, "SOUL.md")
	assert.Contains(t, bundle.Files, "USER.md")
	assert.Equal(t, "shared", bundle.Metadata["session_type"])
}

func TestWorkspaceContext_MemoryFiles(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)

	wsContext := NewWorkspaceContext(workspace)

	// Create memory directory and files
	memoryDir := filepath.Join(workspace, "memory")
	require.NoError(t, os.MkdirAll(memoryDir, 0755))

	// Create today's and yesterday's memory files
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	todayFile := filepath.Join(memoryDir, today+".md")
	yesterdayFile := filepath.Join(memoryDir, yesterday+".md")

	require.NoError(t, os.WriteFile(todayFile, []byte("Today's memories"), 0644))
	require.NoError(t, os.WriteFile(yesterdayFile, []byte("Yesterday's memories"), 0644))

	mainCtx := SecurityContext{SessionType: "main", SessionID: "test"}
	bundle, err := wsContext.LoadContext(context.Background(), mainCtx)
	assert.NoError(t, err)

	// Should include both memory files
	assert.Contains(t, bundle.Files, filepath.Join("memory", today+".md"))
	assert.Contains(t, bundle.Files, filepath.Join("memory", yesterday+".md"))
}

func TestWorkspaceContext_Caching(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)

	wsContext := NewWorkspaceContext(workspace)

	mainCtx := SecurityContext{SessionType: "main", SessionID: "test"}

	// First load - should read from file
	bundle1, err := wsContext.LoadContext(context.Background(), mainCtx)
	assert.NoError(t, err)

	// Second load - should use cache
	bundle2, err := wsContext.LoadContext(context.Background(), mainCtx)
	assert.NoError(t, err)

	// Content should be identical
	assert.Equal(t, bundle1.Files["SOUL.md"], bundle2.Files["SOUL.md"])

	// Verify cache stats
	stats := wsContext.GetCacheStats()
	assert.Greater(t, stats["entries"].(int), 0)
}

func TestWorkspaceContext_InvalidateCache(t *testing.T) {
	workspace := setupTestWorkspace(t)
	defer cleanup(workspace)

	wsContext := NewWorkspaceContext(workspace)

	// Load context to populate cache
	mainCtx := SecurityContext{SessionType: "main", SessionID: "test"}
	_, err := wsContext.LoadContext(context.Background(), mainCtx)
	assert.NoError(t, err)

	// Verify file is cached
	content, cached := wsContext.cache.Get("SOUL.md")
	assert.True(t, cached)
	assert.NotEmpty(t, content)

	// Invalidate cache
	wsContext.InvalidateCache("SOUL.md")

	// Verify file is no longer cached
	_, cached = wsContext.cache.Get("SOUL.md")
	assert.False(t, cached)
}

func TestWorkspaceContext_MissingFiles(t *testing.T) {
	// Create empty workspace
	workspace, err := os.MkdirTemp("", "conduit-test-empty-*")
	require.NoError(t, err)
	defer cleanup(workspace)

	wsContext := NewWorkspaceContext(workspace)

	mainCtx := SecurityContext{SessionType: "main", SessionID: "test"}
	bundle, err := wsContext.LoadContext(context.Background(), mainCtx)

	// Should not error on missing files
	assert.NoError(t, err)
	assert.Empty(t, bundle.Files)
	assert.Equal(t, 0, bundle.Metadata["file_count"])
}

func TestSecurityManager_AccessRules(t *testing.T) {
	sm := NewSecurityManager()

	// Test MEMORY.md access
	mainCtx := SecurityContext{SessionType: "main"}
	sharedCtx := SecurityContext{SessionType: "shared"}

	assert.True(t, sm.IsAccessible("MEMORY.md", mainCtx))
	assert.False(t, sm.IsAccessible("MEMORY.md", sharedCtx))

	// Test SOUL.md access (should be available to all)
	assert.True(t, sm.IsAccessible("SOUL.md", mainCtx))
	assert.True(t, sm.IsAccessible("SOUL.md", sharedCtx))

	// Test memory files access (should be available to all)
	memoryFile := "memory/2024-01-01.md"
	assert.True(t, sm.IsAccessible(memoryFile, mainCtx))
	assert.True(t, sm.IsAccessible(memoryFile, sharedCtx))
}

func TestSecurityManager_CustomRules(t *testing.T) {
	sm := NewSecurityManager()

	// Add custom rule
	customRule := FileAccess{
		Pattern: "SECRET.md",
		Condition: func(sc SecurityContext) bool {
			return sc.UserID == "admin"
		},
		Description: "SECRET.md only for admin user",
		Enabled:     true,
	}
	sm.AddRule(customRule)

	adminCtx := SecurityContext{UserID: "admin"}
	userCtx := SecurityContext{UserID: "user"}

	assert.True(t, sm.IsAccessible("SECRET.md", adminCtx))
	assert.False(t, sm.IsAccessible("SECRET.md", userCtx))

	// Test rule removal
	sm.RemoveRule("SECRET.md")

	// Should now default to allow (since no specific rule matches)
	assert.True(t, sm.IsAccessible("SECRET.md", userCtx))
}

func TestSecurityManager_ValidationError(t *testing.T) {
	sm := NewSecurityManager()

	// Test missing session type
	emptyCtx := SecurityContext{}
	err := sm.ValidateSecurityContext(emptyCtx)
	assert.Equal(t, ErrMissingSessionType, err)

	// Test invalid session type
	invalidCtx := SecurityContext{SessionType: "invalid"}
	err = sm.ValidateSecurityContext(invalidCtx)
	assert.Equal(t, ErrInvalidSessionType, err)

	// Test valid session type
	validCtx := SecurityContext{SessionType: "main"}
	err = sm.ValidateSecurityContext(validCtx)
	assert.NoError(t, err)
}

func TestFileCache_TTLExpiration(t *testing.T) {
	cache := NewFileCache(50*time.Millisecond, 1024*1024) // 50ms TTL

	// Add content
	cache.Set("test.md", "test content")

	// Should be available immediately
	content, ok := cache.Get("test.md")
	assert.True(t, ok)
	assert.Equal(t, "test content", content)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should be expired
	_, ok = cache.Get("test.md")
	assert.False(t, ok)
}

func TestFileCache_SizeLimit(t *testing.T) {
	cache := NewFileCache(5*time.Minute, 2*1024*1024) // 2MB limit

	// Add content that exceeds size limit
	largeContent := make([]byte, 1024*1024) // 1MB
	cache.Set("file1.md", string(largeContent))
	cache.Set("file2.md", string(largeContent))
	cache.Set("file3.md", string(largeContent)) // This should trigger eviction

	stats := cache.Stats()
	assert.LessOrEqual(t, stats["current_size_mb"].(int64), int64(2))
}

// Helper functions

func setupTestWorkspace(t *testing.T) string {
	workspace, err := os.MkdirTemp("", "conduit-test-*")
	require.NoError(t, err)

	// Create test files
	files := map[string]string{
		"SOUL.md":      "# Agent Personality\nI am Jules, your helpful AI assistant.",
		"USER.md":      "# User Information\nUser is a developer working on Conduit.",
		"AGENTS.md":    "# Agent Instructions\nBe helpful and follow workspace conventions.",
		"TOOLS.md":     "# Local Tools\nLocal tool configuration and notes.",
		"MEMORY.md":    "# Long-term Memory\nImportant memories and context.",
		"HEARTBEAT.md": "# Heartbeat Instructions\nCheck email and calendar regularly.",
	}

	for filename, content := range files {
		filePath := filepath.Join(workspace, filename)
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))
	}

	return workspace
}

func cleanup(workspace string) {
	os.RemoveAll(workspace)
}
