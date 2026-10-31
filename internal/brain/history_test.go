package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type historyRow struct {
	id        int64
	value     string
	createdAt time.Time
	source    string
	reason    string
}

// historyRows returns brain_history rows for key, oldest first.
func historyRows(t *testing.T, b *Brain, key string) []historyRow {
	t.Helper()
	rows, err := b.db.Query(`SELECT id, value, created_at, source, reason FROM brain_history WHERE key = ? ORDER BY archived_at, id`, key)
	require.NoError(t, err)
	defer rows.Close()
	var out []historyRow
	for rows.Next() {
		var r historyRow
		require.NoError(t, rows.Scan(&r.id, &r.value, &r.createdAt, &r.source, &r.reason))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// ltmCreatedAt returns the live brain_ltm row's created_at for key.
func ltmCreatedAt(t *testing.T, b *Brain, key string) time.Time {
	t.Helper()
	var ts time.Time
	require.NoError(t, b.db.QueryRow(`SELECT created_at FROM brain_ltm WHERE key = ?`, key).Scan(&ts))
	return ts
}

func TestLTMUpsertArchivesSupersededValue(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "rex.birthday", "February 2", TierLongTerm, "user"))

	entry, err := b.Get(ctx, "rex.birthday")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "February 2", entry.Value)

	hist := historyRows(t, b, "rex.birthday")
	require.Len(t, hist, 1)
	assert.Equal(t, "January 1", hist[0].value)
	assert.Equal(t, "superseded", hist[0].reason)

	// The archived row carries the ORIGINAL created_at of the replaced live
	// row, not NULL and not the archival moment.
	assert.WithinDuration(t, ltmCreatedAt(t, b, "rex.birthday"), hist[0].createdAt, 2*time.Second)
}

// TestLTMHistoryPreservesOriginalCreatedAt proves created_at is the old fact's
// birth time rather than the archival time: the live row is backdated by two
// days before the superseding store, and the archive row must inherit that
// backdated value.
func TestLTMHistoryPreservesOriginalCreatedAt(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = datetime(created_at, '-2 days') WHERE key = ?`, "rex.birthday")
	require.NoError(t, err)
	original := ltmCreatedAt(t, b, "rex.birthday")

	require.NoError(t, b.Store(ctx, "rex.birthday", "February 2", TierLongTerm, "user"))

	hist := historyRows(t, b, "rex.birthday")
	require.Len(t, hist, 1)
	assert.WithinDuration(t, original, hist[0].createdAt, time.Second)
	assert.True(t, hist[0].createdAt.Before(time.Now().Add(-47*time.Hour)),
		"archived created_at must be the original birth time, not the archival moment")
}

func TestLTMSameValueStoreDoesNotArchive(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))

	assert.Empty(t, historyRows(t, b, "rex.birthday"))

	// Normal upsert bookkeeping still applies.
	var accessCount int
	require.NoError(t, b.db.QueryRow(`SELECT access_count FROM brain_ltm WHERE key = ?`, "rex.birthday").Scan(&accessCount))
	assert.Equal(t, 2, accessCount)
}

func TestLTMHistoryMultipleVersions(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	for _, v := range []string{"version A", "version B", "version C"} {
		require.NoError(t, b.Store(ctx, "acme.status", v, TierLongTerm, "user"))
	}

	entry, err := b.Get(ctx, "acme.status")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "version C", entry.Value)

	hist := historyRows(t, b, "acme.status")
	require.Len(t, hist, 2)
	assert.Equal(t, "version A", hist[0].value)
	assert.Equal(t, "version B", hist[1].value)
	assert.Less(t, hist[0].id, hist[1].id)
	for _, h := range hist {
		assert.Equal(t, "superseded", h.reason)
	}
}

func TestBulkStoreArchivesOnValueChange(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{{Key: "rex.birthday", Value: "January 1", Tier: TierLongTerm, Source: "user"}}))
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{{Key: "rex.birthday", Value: "February 2", Tier: TierLongTerm, Source: "user"}}))
	// Same-value bulk re-store must not add another version.
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{{Key: "rex.birthday", Value: "February 2", Tier: TierLongTerm, Source: "user"}}))

	entry, err := b.Get(ctx, "rex.birthday")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "February 2", entry.Value)

	hist := historyRows(t, b, "rex.birthday")
	require.Len(t, hist, 1)
	assert.Equal(t, "January 1", hist[0].value)
	assert.Equal(t, "user", hist[0].source)
	assert.Equal(t, "superseded", hist[0].reason)
}

func TestDeleteArchives(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))
	ltmCreatedAtBeforeDelete := ltmCreatedAt(t, b, "rex.birthday")
	require.NoError(t, b.Delete(ctx, "rex.birthday"))

	entry, err := b.Get(ctx, "rex.birthday")
	require.NoError(t, err)
	assert.Nil(t, entry)

	hist := historyRows(t, b, "rex.birthday")
	require.Len(t, hist, 1)
	assert.Equal(t, "January 1", hist[0].value)
	assert.Equal(t, "deleted", hist[0].reason)
	assert.WithinDuration(t, ltmCreatedAtBeforeDelete, hist[0].createdAt, 2*time.Second)

	// Deleting a key that does not exist archives nothing.
	require.NoError(t, b.Delete(ctx, "missing.key"))
	assert.Empty(t, historyRows(t, b, "missing.key"))
}

func TestHistoryExcludedFromRecall(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "acme.mascot", "zebracorn", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "acme.mascot", "platypus", TierLongTerm, "user"))

	// The superseded value lives on in history...
	hist := historyRows(t, b, "acme.mascot")
	require.Len(t, hist, 1)
	assert.Equal(t, "zebracorn", hist[0].value)

	// ...but recall never sees it.
	results, err := b.Recall(ctx, "zebracorn", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	results, err = b.Recall(ctx, "platypus", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "platypus", results[0].Value)
}
