package sessions

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-385r: DeleteSessionIfPromptOnly.

func newDeleteTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestDeleteSessionIfPromptOnly_DeletesPromptOnlySessionAndDependents(t *testing.T) {
	s := newDeleteTestStore(t)
	mapper := NewClaudeCodeSessionMapper(s.DB())
	require.NoError(t, mapper.EnsureTable())
	_, err := s.db.Exec(`CREATE TABLE session_summaries (session_key TEXT, summary TEXT)`)
	require.NoError(t, err)

	var gotKey string
	var gotIDs []string
	s.SetMessagesDeletedCallback(func(key string, ids []string) { gotKey, gotIDs = key, ids })

	sess, err := s.GetOrCreateSession("cron", "cron_job_1")
	require.NoError(t, err)
	require.NoError(t, s.SetSessionContextBatch(sess.Key, map[string]string{"model": "m", "skill_filter": "x"}))
	m1, err := s.AddMessage(sess.Key, "user", "run the job", nil)
	require.NoError(t, err)
	m2, err := s.AddMessage(sess.Key, "user", "run the job (retry)", map[string]string{})
	require.NoError(t, err)
	require.NoError(t, mapper.SaveMapping(sess.Key, "cc-1"))
	_, err = s.db.Exec(`INSERT INTO session_summaries VALUES (?, 's')`, sess.Key)
	require.NoError(t, err)
	require.NoError(t, s.UpdateSessionState(sess.Key, SessionStateIdle, nil))

	// An unrelated session is untouched.
	other, err := s.GetOrCreateSession("42", "telegram")
	require.NoError(t, err)
	_, err = s.AddMessage(other.Key, "user", "hi", nil)
	require.NoError(t, err)

	deleted, err := s.DeleteSessionIfPromptOnly(context.Background(), sess.Key)
	require.NoError(t, err)
	assert.True(t, deleted)

	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE key = ?`, sess.Key))
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, sess.Key))
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM messages_fts WHERE session_key = ?`, sess.Key), "gateway.db FTS follows the delete")
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM claude_code_sessions WHERE conduit_session_id = ?`, sess.Key))
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM session_summaries WHERE session_key = ?`, sess.Key))
	_, tracked := s.GetSessionState(sess.Key)
	assert.False(t, tracked, "state tracker entry removed")

	assert.Equal(t, sess.Key, gotKey)
	sort.Strings(gotIDs)
	want := []string{m1.ID, m2.ID}
	sort.Strings(want)
	assert.Equal(t, want, gotIDs, "search.db mirror told which messages went")

	assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, other.Key))
}

func TestDeleteSessionIfPromptOnly_EmptySessionDeleted(t *testing.T) {
	s := newDeleteTestStore(t)
	called := false
	s.SetMessagesDeletedCallback(func(string, []string) { called = true })
	sess, err := s.GetOrCreateSession("heartbeat", "heartbeat_1")
	require.NoError(t, err)

	deleted, err := s.DeleteSessionIfPromptOnly(context.Background(), sess.Key)
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.False(t, called, "no messages, no search.db callback")
	_, err = s.GetSession(sess.Key)
	assert.Error(t, err)
}

func TestDeleteSessionIfPromptOnly_KeepsSessionsWithTranscript(t *testing.T) {
	cases := []struct {
		name string
		add  func(s *Store, key string) error
	}{
		{"assistant reply", func(s *Store, key string) error {
			_, err := s.AddMessage(key, "assistant", "the answer", nil)
			return err
		}},
		{"inter-session delivery", func(s *Store, key string) error {
			_, err := s.AddMessage(key, "user", "sub-agent result", map[string]string{"source": "inter_session", "wake_source": "sub_agent_silent"})
			return err
		}},
		{"restart resume note", func(s *Store, key string) error {
			_, err := s.AddMessage(key, "user", "resume", map[string]string{"source": "restart_resume"})
			return err
		}},
		{"system row", func(s *Store, key string) error {
			_, err := s.AddMessage(key, "system", "note", nil)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newDeleteTestStore(t)
			sess, err := s.GetOrCreateSession("cron", "cron_job_1")
			require.NoError(t, err)
			_, err = s.AddMessage(sess.Key, "user", "run the job", nil)
			require.NoError(t, err)
			require.NoError(t, tc.add(s, sess.Key))

			deleted, err := s.DeleteSessionIfPromptOnly(context.Background(), sess.Key)
			require.NoError(t, err)
			assert.False(t, deleted)
			assert.Equal(t, 2, countRows(t, s, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, sess.Key))
			assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE key = ?`, sess.Key))
		})
	}
}

func TestDeleteSessionIfPromptOnly_MissingSession(t *testing.T) {
	s := newDeleteTestStore(t)
	deleted, err := s.DeleteSessionIfPromptOnly(context.Background(), "cron_nope")
	require.NoError(t, err)
	assert.False(t, deleted)
}

// A write into a deleted session must not resurrect it: messages fail on the
// foreign key, context/state writes touch no row.
func TestDeleteSessionIfPromptOnly_LateWritesDoNotResurrect(t *testing.T) {
	s := newDeleteTestStore(t)
	sess, err := s.GetOrCreateSession("cron", "cron_job_1")
	require.NoError(t, err)
	deleted, err := s.DeleteSessionIfPromptOnly(context.Background(), sess.Key)
	require.NoError(t, err)
	require.True(t, deleted)

	_, err = s.AddMessage(sess.Key, "user", "late delivery", map[string]string{"source": "inter_session"})
	assert.Error(t, err, "message into a deleted session fails")
	assert.Error(t, s.SetSessionContext(sess.Key, "wake_depth", "0"))
	s.MarkSessionActivity(sess.Key) // best-effort UPDATE of no row
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE key = ?`, sess.Key))
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, sess.Key))
}

// Concurrent deliveries and deletes: every outcome is consistent — either
// the session was deleted and the delivery failed, or the delivery landed
// and the session was kept.
func TestDeleteSessionIfPromptOnly_ConcurrentDelivery(t *testing.T) {
	s := newDeleteTestStore(t)
	for i := 0; i < 20; i++ {
		sess, err := s.GetOrCreateSession("cron", "cron_race_"+string(rune('a'+i)))
		require.NoError(t, err)
		_, err = s.AddMessage(sess.Key, "user", "prompt", nil)
		require.NoError(t, err)

		var wg sync.WaitGroup
		var deleted bool
		var delErr, addErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			deleted, delErr = s.DeleteSessionIfPromptOnly(context.Background(), sess.Key)
		}()
		go func() {
			defer wg.Done()
			_, addErr = s.AddMessage(sess.Key, "user", "delivery", map[string]string{"source": "inter_session"})
		}()
		wg.Wait()
		require.NoError(t, delErr)

		rows := countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE key = ?`, sess.Key)
		msgs := countRows(t, s, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, sess.Key)
		if deleted {
			assert.Error(t, addErr, "delivery after delete must fail")
			assert.Zero(t, rows)
			assert.Zero(t, msgs, "no orphan message")
		} else {
			require.NoError(t, addErr)
			assert.Equal(t, 1, rows)
			assert.Equal(t, 2, msgs)
		}
	}
}
