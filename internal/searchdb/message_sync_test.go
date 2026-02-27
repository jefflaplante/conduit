package searchdb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageSyncerSyncSingleMessage(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)

	// Sync a single message
	err = syncer.SyncSingleMessage("msg-1", "session-1", "user", "Hello world")
	require.NoError(t, err)

	// Verify it's in the FTS index
	var count int
	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE message_id = 'msg-1'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestMessageSyncerDeleteSessionMessages(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)

	// Add some messages to different sessions
	require.NoError(t, syncer.SyncSingleMessage("msg-1", "session-1", "user", "Hello"))
	require.NoError(t, syncer.SyncSingleMessage("msg-2", "session-1", "assistant", "Hi there"))
	require.NoError(t, syncer.SyncSingleMessage("msg-3", "session-2", "user", "Different session"))

	// Delete session-1 messages
	err = syncer.DeleteSessionMessages("session-1")
	require.NoError(t, err)

	// Verify session-1 messages are gone
	var countSession1 int
	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE session_key = 'session-1'").Scan(&countSession1)
	require.NoError(t, err)
	assert.Equal(t, 0, countSession1)

	// Verify session-2 messages still exist
	var countSession2 int
	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE session_key = 'session-2'").Scan(&countSession2)
	require.NoError(t, err)
	assert.Equal(t, 1, countSession2)
}

func TestMessageSyncerFullSync(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	// Add some messages to gateway.db
	_, err = gatewayDB.Exec(`INSERT INTO messages (id, session_key, role, content) VALUES
		('msg-1', 'session-1', 'user', 'First message'),
		('msg-2', 'session-1', 'assistant', 'Second message'),
		('msg-3', 'session-2', 'user', 'Third message')`)
	require.NoError(t, err)

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)

	// Run full sync
	ctx := context.Background()
	err = syncer.FullSync(ctx)
	require.NoError(t, err)

	// Verify all messages are synced
	var count int
	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestMessageSyncerFullSyncSkipsIfAlreadySynced(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	// Add messages to gateway.db
	_, err = gatewayDB.Exec(`INSERT INTO messages (id, session_key, role, content) VALUES
		('msg-1', 'session-1', 'user', 'Hello')`)
	require.NoError(t, err)

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)
	ctx := context.Background()

	// First sync
	require.NoError(t, syncer.FullSync(ctx))

	// Get the sync time
	stats := syncer.GetStats()
	firstSyncTime := stats["last_full_sync"]

	// Second sync should be quick (skip)
	require.NoError(t, syncer.FullSync(ctx))

	// Times should be different (second sync updated the time)
	stats = syncer.GetStats()
	secondSyncTime := stats["last_full_sync"]
	assert.NotEqual(t, firstSyncTime, secondSyncTime)
}

func TestMessageSyncerValidateSync(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	// Add messages to gateway.db
	_, err = gatewayDB.Exec(`INSERT INTO messages (id, session_key, role, content) VALUES
		('msg-1', 'session-1', 'user', 'Hello'),
		('msg-2', 'session-1', 'assistant', 'Hi')`)
	require.NoError(t, err)

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)
	ctx := context.Background()

	// Before sync - should be out of sync
	gwCount, ftsCount, inSync, err := syncer.ValidateSync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, gwCount)
	assert.Equal(t, 0, ftsCount)
	assert.False(t, inSync)

	// After sync - should be in sync
	require.NoError(t, syncer.FullSync(ctx))
	gwCount, ftsCount, inSync, err = syncer.ValidateSync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, gwCount)
	assert.Equal(t, 2, ftsCount)
	assert.True(t, inSync)
}

func TestMessageSyncerCallbacks(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)

	// Get callback functions
	addedCallback := syncer.MessageAddedCallback()
	clearedCallback := syncer.SessionClearedCallback()

	// Test added callback
	addedCallback("msg-1", "session-1", "user", "Test message")

	var count int
	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE message_id = 'msg-1'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Test cleared callback
	clearedCallback("session-1")

	err = sdb.DB().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE session_key = 'session-1'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestMessageSyncerGetStats(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)

	// Sync some messages
	require.NoError(t, syncer.SyncSingleMessage("msg-1", "session-1", "user", "Hello"))
	require.NoError(t, syncer.SyncSingleMessage("msg-2", "session-1", "assistant", "Hi"))

	stats := syncer.GetStats()
	assert.Contains(t, stats, "synced_count")
	assert.Contains(t, stats, "last_sync_time")
	assert.Contains(t, stats, "last_full_sync")
	assert.Equal(t, int64(2), stats["synced_count"])
}
