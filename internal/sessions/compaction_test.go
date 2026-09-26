package sessions

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.21: ApplyCompaction tests.

func newCompactionTestStore(t *testing.T) (*Store, *Session) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "compact.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	session, err := store.GetOrCreateSession("u1", "c1")
	require.NoError(t, err)
	return store, session
}

func contents(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestApplyCompaction_ScopedAndOrdered(t *testing.T) {
	store, session := newCompactionTestStore(t)
	for _, c := range []string{"h1", "h2", "h3", "r1", "r2"} {
		_, err := store.AddMessage(session.Key, "user", c, nil)
		require.NoError(t, err)
	}
	snap, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)

	// A message arrives after the snapshot (e.g. during summarization).
	_, err = store.AddMessage(session.Key, "user", "new", nil)
	require.NoError(t, err)

	ids := []string{snap[0].ID, snap[1].ID, snap[2].ID}
	sum, err := store.ApplyCompaction(session.Key, ids, CompactionSummary{
		Content: "SUMMARY",
		Before:  snap[3].Timestamp,
	})
	require.NoError(t, err)
	assert.Equal(t, "assistant", sum.Role)

	got, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"SUMMARY", "r1", "r2", "new"}, contents(got))

	s, err := store.GetSession(session.Key)
	require.NoError(t, err)
	assert.Equal(t, 4, s.MessageCount)
}

func TestApplyCompaction_StaleSnapshotIsNoop(t *testing.T) {
	store, session := newCompactionTestStore(t)
	for _, c := range []string{"h1", "h2", "r1"} {
		_, err := store.AddMessage(session.Key, "user", c, nil)
		require.NoError(t, err)
	}
	snap, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)

	ids := []string{snap[0].ID, snap[1].ID}
	_, err = store.ApplyCompaction(session.Key, ids, CompactionSummary{Content: "S1", Before: snap[2].Timestamp})
	require.NoError(t, err)

	// Second apply of the same snapshot must not double-summarize.
	_, err = store.ApplyCompaction(session.Key, ids, CompactionSummary{Content: "S2", Before: snap[2].Timestamp})
	require.ErrorIs(t, err, ErrCompactionStale)

	got, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"S1", "r1"}, contents(got))
}

func TestApplyCompaction_FailureRollsBack(t *testing.T) {
	store, session := newCompactionTestStore(t)
	for _, c := range []string{"h1", "h2", "r1"} {
		_, err := store.AddMessage(session.Key, "user", c, nil)
		require.NoError(t, err)
	}
	snap, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)

	// Inject a failure AFTER the deletes have run inside the transaction:
	// the summary INSERT aborts.
	_, err = store.DB().Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON messages
		WHEN NEW.content = 'BOOM' BEGIN SELECT RAISE(ABORT, 'injected failure'); END;`)
	require.NoError(t, err)

	_, err = store.ApplyCompaction(session.Key, []string{snap[0].ID, snap[1].ID},
		CompactionSummary{Content: "BOOM", Before: snap[2].Timestamp})
	require.Error(t, err)

	got, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"h1", "h2", "r1"}, contents(got))
	s, err := store.GetSession(session.Key)
	require.NoError(t, err)
	assert.Equal(t, 3, s.MessageCount)
}

func TestApplyCompaction_ClosedDBLeavesNothingHalfDone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "closed.db")
	store, err := NewStore(dbPath)
	require.NoError(t, err)
	session, err := store.GetOrCreateSession("u1", "c1")
	require.NoError(t, err)
	for _, c := range []string{"h1", "r1"} {
		_, err := store.AddMessage(session.Key, "user", c, nil)
		require.NoError(t, err)
	}
	snap, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	_, err = store.ApplyCompaction(session.Key, []string{snap[0].ID},
		CompactionSummary{Content: "S", Before: snap[1].Timestamp})
	require.Error(t, err)

	reopened, err := NewStore(dbPath)
	require.NoError(t, err)
	defer reopened.Close()
	got, err := reopened.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"h1", "r1"}, contents(got))
}
