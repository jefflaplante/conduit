package searchdb

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.73 (from .50): DeleteMessages removes exactly the given IDs
// from the FTS index (compaction replaces them with a summary), leaves
// other messages of the same and other sessions, tolerates unknown IDs and
// is a no-op for an empty list. The callback wrapper does the same.
func TestMessageSyncerDeleteMessages(t *testing.T) {
	tmpDir := t.TempDir()
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(filepath.Join(tmpDir, "search.db"), gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	syncer := NewMessageSyncer(sdb.DB(), gatewayDB)
	for _, m := range []struct{ id, session, content string }{
		{"m1", "s1", "alpha compacted"},
		{"m2", "s1", "bravo compacted"},
		{"m3", "s1", "charlie kept"},
		{"m4", "s2", "delta other session"},
		{"m5", "s2", "echo via callback"},
	} {
		require.NoError(t, syncer.SyncSingleMessage(m.id, m.session, "user", m.content))
	}

	ids := func() []string {
		rows, err := sdb.DB().Query(`SELECT message_id FROM messages_fts ORDER BY message_id`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		require.NoError(t, rows.Err())
		return out
	}

	require.NoError(t, syncer.DeleteMessages(nil))
	assert.Equal(t, []string{"m1", "m2", "m3", "m4", "m5"}, ids(), "empty list must be a no-op")

	require.NoError(t, syncer.DeleteMessages([]string{"m1", "m2", "does-not-exist"}))
	assert.Equal(t, []string{"m3", "m4", "m5"}, ids())

	// Deleted content is no longer searchable; kept content still is.
	var n int
	require.NoError(t, sdb.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'compacted'`).Scan(&n))
	assert.Equal(t, 0, n)
	require.NoError(t, sdb.DB().QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'kept'`).Scan(&n))
	assert.Equal(t, 1, n)

	syncer.MessagesDeletedCallback()("s2", []string{"m5"})
	assert.Equal(t, []string{"m3", "m4"}, ids())
}
