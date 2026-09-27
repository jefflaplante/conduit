package sessions

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertRawMessage(t *testing.T, store *Store, id, sessionKey, content string, ts time.Time) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO messages (id, session_key, role, content, timestamp, metadata)
		VALUES (?, ?, 'user', ?, ?, '{}')`, id, sessionKey, content, ts)
	require.NoError(t, err)
}

// conduit-31jg.50: rows with identical timestamps come back in insertion
// order (rowid tie-breaker), not in random-UUID order.
func TestGetMessages_IdenticalTimestampsKeepInsertionOrder(t *testing.T) {
	store, session := newCompactionTestStore(t)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const n = 30
	var want []string
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("m%02d", i)
		want = append(want, content)
		// IDs sort opposite to insertion order.
		insertRawMessage(t, store, fmt.Sprintf("%03d-id", n-i), session.Key, content, ts)
	}

	got, err := store.GetMessages(session.Key, 0)
	require.NoError(t, err)
	assert.Equal(t, want, contents(got))

	// The LIMIT window keeps the newest rows by insertion order too.
	got, err = store.GetMessages(session.Key, 5)
	require.NoError(t, err)
	assert.Equal(t, want[n-5:], contents(got))
}

// conduit-31jg.50: a compaction summary stamped 1ns before the oldest
// retained message (conduit-31jg.21) sorts first even though its rowid is the
// highest and the retained messages tie on timestamp.
func TestApplyCompaction_SummarySortsBeforeTiedRetained(t *testing.T) {
	store, session := newCompactionTestStore(t)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, c := range []string{"h1", "h2", "r1", "r2"} {
		insertRawMessage(t, store, fmt.Sprintf("id%d", 9-i), session.Key, c, ts)
	}
	snap, err := store.GetMessages(session.Key, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"h1", "h2", "r1", "r2"}, contents(snap))

	_, err = store.ApplyCompaction(session.Key, []string{snap[0].ID, snap[1].ID},
		CompactionSummary{Content: "SUMMARY", Before: snap[2].Timestamp})
	require.NoError(t, err)
	got, err := store.GetMessages(session.Key, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"SUMMARY", "r1", "r2"}, contents(got))
}

// conduit-31jg.73: % and _ in the query are literal in the LIKE fallback.
func TestSearchMessagesLIKE_EscapesWildcards(t *testing.T) {
	store, session := newCompactionTestStore(t)
	for _, c := range []string{"100% done", "1000 done", "a_b here", "axb here", `back\slash`} {
		_, err := store.AddMessage(session.Key, "user", c, nil)
		require.NoError(t, err)
	}
	for q, want := range map[string][]string{
		"100%": {"100% done"},
		"a_b":  {"a_b here"},
		`k\s`:  {`back\slash`},
		"done": {"1000 done", "100% done"},
		"%":    {"100% done"},
		"_":    {"a_b here"},
	} {
		res, err := store.searchMessagesLIKE(q, 10)
		require.NoError(t, err, q)
		var got []string
		for _, r := range res {
			got = append(got, r.Message.Content)
		}
		assert.ElementsMatch(t, want, got, "query %q", q)
	}
}
