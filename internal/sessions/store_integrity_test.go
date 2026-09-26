package sessions

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newIntegrityStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s, path
}

func countSessions(t *testing.T, s *Store, userID, channelID string) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB().QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND channel_id = ?`, userID, channelID).Scan(&n))
	return n
}

// conduit-31jg.24: a DB error other than "no rows" must be returned, not
// treated as "not found" (which silently forks the conversation into a new
// session). We provoke a non-ErrNoRows error by corrupting the row so Scan
// fails (context NULL cannot scan into string); SQLITE_BUSY takes the same
// code path.
func TestGetOrCreateSession_DBErrorDoesNotCreate(t *testing.T) {
	s, _ := newIntegrityStore(t)
	sess, err := s.GetOrCreateSession("u1", "telegram")
	require.NoError(t, err)

	_, err = s.DB().Exec(`UPDATE sessions SET context = NULL WHERE key = ?`, sess.Key)
	require.NoError(t, err)

	_, err = s.GetOrCreateSession("u1", "telegram")
	require.Error(t, err, "scan error must surface")
	assert.False(t, errors.Is(err, sql.ErrNoRows))
	assert.Equal(t, 1, countSessions(t, s, "u1", "telegram"), "no new session may be created on DB error")
}

func TestGetOrCreateSession_NotFoundCreates(t *testing.T) {
	s, _ := newIntegrityStore(t)
	_, err := s.GetLatestSession("nobody", "telegram")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sql.ErrNoRows), "not-found must wrap sql.ErrNoRows")
	assert.Contains(t, err.Error(), "no session found for user nobody in channel telegram")

	sess, err := s.GetOrCreateSession("nobody", "telegram")
	require.NoError(t, err)
	again, err := s.GetOrCreateSession("nobody", "telegram")
	require.NoError(t, err)
	assert.Equal(t, sess.Key, again.Key)
}

// conduit-31jg.24: updated_at values written in mixed formats (legacy
// CURRENT_TIMESTAMP, Go time.String() with a non-UTC offset, RFC3339) must
// order by actual instant. NewStore normalises legacy rows.
func TestUpdatedAtOrdering_MixedLegacyFormats(t *testing.T) {
	s, path := newIntegrityStore(t)
	ins := func(key, updated string) {
		_, err := s.DB().Exec(`INSERT INTO sessions (key, user_id, channel_id, created_at, updated_at, message_count, context)
			VALUES (?, 'u', 'c', '2026-01-01 00:00:00', ?, 0, '{}')`, key, updated)
		require.NoError(t, err)
	}
	// Actual instants (UTC): a=10:00:00, b=12:00:00.5, c=11:00:00, d=10:00:00.9
	ins("a", "2026-01-01 10:00:00")                             // CURRENT_TIMESTAMP (UTC)
	ins("b", "2026-01-01 08:00:00.5 -0400 EDT m=+12.000000001") // Go String, non-UTC
	ins("c", "2026-01-01T11:00:00Z")                            // RFC3339
	ins("d", "2026-01-01 10:00:00.9 +0000 UTC m=+1.5")          // Go String, UTC

	// Before normalisation, lexicographic ordering puts "c" (the 'T' form)
	// first, which is wrong.
	require.NoError(t, s.Close())
	s2, err := NewStore(path)
	require.NoError(t, err)
	defer s2.Close()

	latest, err := s2.GetLatestSession("u", "c")
	require.NoError(t, err)
	assert.Equal(t, "b", latest.Key)

	list, err := s2.GetSessionsByUser("u", 10)
	require.NoError(t, err)
	var keys []string
	for _, x := range list {
		keys = append(keys, x.Key)
	}
	assert.Equal(t, []string{"b", "c", "d", "a"}, keys)

	// Values are now canonical and parse back to the right instant.
	var raw string
	require.NoError(t, s2.DB().QueryRow(`SELECT CAST(updated_at AS TEXT) FROM sessions WHERE key='b'`).Scan(&raw))
	assert.Equal(t, "2026-01-01 12:00:00.500000000", raw)
	assert.True(t, latest.UpdatedAt.Equal(time.Date(2026, 1, 1, 12, 0, 0, 5e8, time.UTC)))

	// Idempotent: a second open changes nothing.
	require.NoError(t, s2.Close())
	s3, err := NewStore(path)
	require.NoError(t, err)
	defer s3.Close()
	n, err := s3.normalizeUpdatedAt()
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

// conduit-31jg.24: every write path now stores updated_at in the same
// canonical format, so a CURRENT_TIMESTAMP-style write can no longer sort
// wrongly against a Go-written one.
func TestUpdatedAt_NewWritesCanonical(t *testing.T) {
	s, _ := newIntegrityStore(t)
	sess, err := s.GetOrCreateSession("u", "c")
	require.NoError(t, err)
	_, err = s.AddMessage(sess.Key, "user", "hi", nil)
	require.NoError(t, err)
	require.NoError(t, s.SetSessionContext(sess.Key, "k", "v"))
	require.NoError(t, s.SetSessionContextBatch(sess.Key, map[string]string{"a": "b"}))
	s.markSessionActivity(sess.Key)
	require.NoError(t, s.ClearSessionMessages(sess.Key))

	var bad int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE NOT (`+canonicalUpdatedAtGlob+`)`).Scan(&bad))
	assert.Equal(t, 0, bad)
}

// conduit-31jg.24: AddMessage's insert and message_count update are one
// transaction — if the count update fails, the message must not persist.
func TestAddMessage_Atomic(t *testing.T) {
	s, _ := newIntegrityStore(t)
	sess, err := s.GetOrCreateSession("u", "c")
	require.NoError(t, err)

	_, err = s.DB().Exec(`CREATE TRIGGER fail_count BEFORE UPDATE OF message_count ON sessions
		BEGIN SELECT RAISE(ABORT, 'boom'); END;`)
	require.NoError(t, err)

	var added int
	s.SetMessageCallbacks(func(id, key, role, content string) { added++ }, nil)

	_, err = s.AddMessage(sess.Key, "user", "hello", nil)
	require.Error(t, err)

	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE session_key = ?`, sess.Key).Scan(&n))
	assert.Equal(t, 0, n, "message insert must be rolled back when the count update fails")
	assert.Equal(t, 0, added, "search sync callback must not fire for a rolled-back message")

	_, err = s.DB().Exec(`DROP TRIGGER fail_count`)
	require.NoError(t, err)
	_, err = s.AddMessage(sess.Key, "user", "hello", nil)
	require.NoError(t, err)
	got, err := s.GetSession(sess.Key)
	require.NoError(t, err)
	assert.Equal(t, 1, got.MessageCount)
}
