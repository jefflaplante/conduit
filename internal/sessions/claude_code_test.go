package sessions

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func setupTestMapper(t *testing.T) *ClaudeCodeSessionMapper {
	t.Helper()
	db := setupTestDB(t)
	mapper := NewClaudeCodeSessionMapper(db)
	require.NoError(t, mapper.EnsureTable())
	return mapper
}

func TestClaudeCodeSessionMapper_SaveAndGet(t *testing.T) {
	mapper := setupTestMapper(t)

	conduitID := "ws_user1_abc123"
	ccID := "cc-session-uuid-1234"

	// Save a mapping
	err := mapper.SaveMapping(conduitID, ccID)
	require.NoError(t, err)

	// Retrieve it
	got, err := mapper.GetClaudeCodeSession(conduitID)
	require.NoError(t, err)
	assert.Equal(t, ccID, got)
}

func TestClaudeCodeSessionMapper_UpdateExisting(t *testing.T) {
	mapper := setupTestMapper(t)

	conduitID := "ws_user1_abc123"
	ccID1 := "cc-session-uuid-1111"
	ccID2 := "cc-session-uuid-2222"

	// Save initial mapping
	require.NoError(t, mapper.SaveMapping(conduitID, ccID1))

	got, err := mapper.GetClaudeCodeSession(conduitID)
	require.NoError(t, err)
	assert.Equal(t, ccID1, got)

	// Overwrite with new CC session ID
	require.NoError(t, mapper.SaveMapping(conduitID, ccID2))

	got, err = mapper.GetClaudeCodeSession(conduitID)
	require.NoError(t, err)
	assert.Equal(t, ccID2, got, "SaveMapping should upsert to the new CC session ID")
}

func TestClaudeCodeSessionMapper_GetNonExistent(t *testing.T) {
	mapper := setupTestMapper(t)

	got, err := mapper.GetClaudeCodeSession("does-not-exist")
	require.NoError(t, err)
	assert.Equal(t, "", got, "non-existent mapping should return empty string, not error")
}

func TestClaudeCodeSessionMapper_Delete(t *testing.T) {
	mapper := setupTestMapper(t)

	conduitID := "ws_user1_abc123"
	ccID := "cc-session-uuid-1234"

	require.NoError(t, mapper.SaveMapping(conduitID, ccID))

	// Verify it exists
	got, err := mapper.GetClaudeCodeSession(conduitID)
	require.NoError(t, err)
	assert.Equal(t, ccID, got)

	// Delete it
	require.NoError(t, mapper.DeleteMapping(conduitID))

	// Verify it's gone
	got, err = mapper.GetClaudeCodeSession(conduitID)
	require.NoError(t, err)
	assert.Equal(t, "", got, "deleted mapping should return empty string")
}

func TestClaudeCodeSessionMapper_DeleteNonExistent(t *testing.T) {
	mapper := setupTestMapper(t)

	// Deleting a non-existent mapping should not error
	err := mapper.DeleteMapping("does-not-exist")
	assert.NoError(t, err)
}

func TestClaudeCodeSessionMapper_UpdateLastUsed(t *testing.T) {
	mapper := setupTestMapper(t)

	conduitID := "ws_user1_abc123"
	ccID := "cc-session-uuid-1234"

	require.NoError(t, mapper.SaveMapping(conduitID, ccID))

	// Read the initial last_used_at
	var initialLastUsed time.Time
	err := mapper.db.QueryRow(`
		SELECT last_used_at FROM claude_code_sessions WHERE conduit_session_id = ?
	`, conduitID).Scan(&initialLastUsed)
	require.NoError(t, err)

	// SQLite CURRENT_TIMESTAMP has second resolution, so we need to wait
	// a bit to ensure the timestamp changes
	time.Sleep(1100 * time.Millisecond)

	// Update last used
	require.NoError(t, mapper.UpdateLastUsed(conduitID))

	// Read updated timestamp
	var updatedLastUsed time.Time
	err = mapper.db.QueryRow(`
		SELECT last_used_at FROM claude_code_sessions WHERE conduit_session_id = ?
	`, conduitID).Scan(&updatedLastUsed)
	require.NoError(t, err)

	assert.True(t, updatedLastUsed.After(initialLastUsed),
		"last_used_at should advance after UpdateLastUsed (initial=%v, updated=%v)",
		initialLastUsed, updatedLastUsed)
}

func TestClaudeCodeSessionMapper_CleanupOld(t *testing.T) {
	mapper := setupTestMapper(t)

	// Insert two mappings
	require.NoError(t, mapper.SaveMapping("old-session", "cc-old"))
	require.NoError(t, mapper.SaveMapping("new-session", "cc-new"))

	// Backdate the "old" session to 48 hours ago
	_, err := mapper.db.Exec(`
		UPDATE claude_code_sessions
		SET last_used_at = datetime('now', '-48 hours')
		WHERE conduit_session_id = ?
	`, "old-session")
	require.NoError(t, err)

	// Cleanup entries older than 24 hours
	deleted, err := mapper.CleanupOld(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "should delete exactly one old mapping")

	// Old session should be gone
	got, err := mapper.GetClaudeCodeSession("old-session")
	require.NoError(t, err)
	assert.Equal(t, "", got)

	// New session should still exist
	got, err = mapper.GetClaudeCodeSession("new-session")
	require.NoError(t, err)
	assert.Equal(t, "cc-new", got)
}

func TestClaudeCodeSessionMapper_EnsureTableIdempotent(t *testing.T) {
	db := setupTestDB(t)
	mapper := NewClaudeCodeSessionMapper(db)

	// Calling EnsureTable multiple times should not error
	require.NoError(t, mapper.EnsureTable())
	require.NoError(t, mapper.EnsureTable())

	// And it should still work
	require.NoError(t, mapper.SaveMapping("s1", "cc1"))
	got, err := mapper.GetClaudeCodeSession("s1")
	require.NoError(t, err)
	assert.Equal(t, "cc1", got)
}
