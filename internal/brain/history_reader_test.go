package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reader-side tests for the brain_history API (bd-r1f3). The write path
// stamps datetime('now') at second precision, so tests build coherent
// historical timelines by backdating the storage columns directly:
//
//   - a history row represents a value live on [created_at, archived_at)
//   - the live brain_ltm row represents a value live on [created_at, ∞)
//
// Tests that assert point-in-time behavior must keep those intervals
// consistent, exactly as the real write path does.

// backdateHistory sets archived_at of the nth archive row (chronological by
// (archived_at, id)) for key.
func backdateHistory(t *testing.T, b *Brain, key string, n int, archivedAt time.Time) {
	t.Helper()
	rows := historyRows(t, b, key)
	require.Greater(t, len(rows), n, "need at least %d history rows, have %d", n+1, len(rows))
	_, err := b.db.Exec(`UPDATE brain_history SET archived_at = ? WHERE id = ?`,
		archivedAt.UTC().Format("2006-01-02 15:04:05"), rows[n].id)
	require.NoError(t, err)
}

// backdateLiveCreated sets created_at of the live brain_ltm row for key, so
// the NEXT archive of that row inherits the backdated birth time.
func backdateLiveCreated(t *testing.T, b *Brain, key string, createdAt time.Time) {
	t.Helper()
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = ? WHERE key = ?`,
		createdAt.UTC().Format("2006-01-02 15:04:05"), key)
	require.NoError(t, err)
}

func TestListKeyHistoryEmpty(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "fresh.key", "v1", TierLongTerm, "user"))

	hist, err := b.ListKeyHistory(ctx, "fresh.key", 0)
	require.NoError(t, err)
	assert.Empty(t, hist)

	// Unknown key: same story.
	hist, err = b.ListKeyHistory(ctx, "no.such.key", 0)
	require.NoError(t, err)
	assert.Empty(t, hist)
}

func TestListKeyHistoryNewestFirst(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "acme.status", "B", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "acme.status", "C", TierLongTerm, "user"))

	hist, err := b.ListKeyHistory(ctx, "acme.status", 0)
	require.NoError(t, err)
	require.Len(t, hist, 2)
	// Newest archive first: B was archived after A.
	assert.Equal(t, "B", hist[0].Value)
	assert.Equal(t, "A", hist[1].Value)
	assert.False(t, hist[0].ArchivedAt.Before(hist[1].ArchivedAt))
	for _, h := range hist {
		assert.Equal(t, "acme.status", h.Key)
		assert.Equal(t, "superseded", h.Reason)
		assert.Equal(t, TierLongTerm, h.Tier)
		assert.Equal(t, "user", h.Source)
	}
}

func TestListKeyHistoryLimit(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "acme.status", "B", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "acme.status", "C", TierLongTerm, "user"))

	hist, err := b.ListKeyHistory(ctx, "acme.status", 1)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, "B", hist[0].Value, "limit returns the newest archive rows")
}

func TestValueAsOfLiveFallback(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	beforeStore := time.Now().Add(-time.Minute)
	require.NoError(t, b.Store(ctx, "rex.birthday", "January 1", TierLongTerm, "user"))

	// No archives exist, so every T at-or-after creation reads the live row.
	for _, asOf := range []time.Time{time.Now(), time.Now().Add(time.Hour)} {
		got, err := b.ValueAsOf(ctx, "rex.birthday", asOf)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "January 1", got.Value)
	}

	// Before the fact existed: not found, even though the fact is live now.
	got, err := b.ValueAsOf(ctx, "rex.birthday", beforeStore)
	require.NoError(t, err)
	assert.Nil(t, got, "timestamp predating the fact must not leak the current value")

	// Never-existing key: not found.
	got, err = b.ValueAsOf(ctx, "no.such.key", time.Now())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestValueAsOfBetweenArchives pins the interval semantics: a history row
// covers [created_at, archived_at) — the value it holds stopped being live
// at archived_at. The value live between two archives is therefore carried
// by the LATER archive row; a naive "newest row at-or-before T" rule would
// wrongly return the pre-previous value.
func TestValueAsOfBetweenArchives(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	t0 := base                    // A born
	t2 := base.Add(2 * time.Hour) // A archived, B born
	t4 := base.Add(4 * time.Hour) // B archived, C born

	// Build a coherent timeline: backdate each generation's birth before the
	// next store so the archive row inherits it, then backdate the archive
	// instant itself.
	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))
	backdateLiveCreated(t, b, "acme.status", t0)
	require.NoError(t, b.Store(ctx, "acme.status", "B", TierLongTerm, "user"))
	backdateHistory(t, b, "acme.status", 0, t2) // archive of A
	backdateLiveCreated(t, b, "acme.status", t2)
	require.NoError(t, b.Store(ctx, "acme.status", "C", TierLongTerm, "user"))
	backdateHistory(t, b, "acme.status", 1, t4) // archive of B
	backdateLiveCreated(t, b, "acme.status", t4)

	// Sanity: the write side produced the expected archive rows.
	require.Len(t, historyRows(t, b, "acme.status"), 2)

	cases := []struct {
		name string
		asOf time.Time
		want string
	}{
		{"before fact birth", t0.Add(-time.Second), ""},
		{"mid first interval", t0.Add(time.Hour), "A"},
		{"exactly at first archive instant", t2, "B"},
		{"between archives", t2.Add(time.Hour), "B"},
		{"exactly at second archive instant", t4, "C"},
		{"after last archive", t4.Add(time.Hour), "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := b.ValueAsOf(ctx, "acme.status", tc.asOf)
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Value)
		})
	}
}

// TestValueAsOfBeforeFirstArchiveBackdates covers the common real-world
// shape: created_at survives upserts (it is the fact's original birth time),
// so the first archive row of a long-lived fact has created_at far older
// than its archived_at. Point-in-time reads must order by archived_at, not
// be confused by the shared old created_at.
func TestValueAsOfBeforeFirstArchiveBackdates(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Fact born 3 days ago, superseded "now".
	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = datetime(created_at, '-3 days') WHERE key = ?`, "acme.status")
	require.NoError(t, err)
	require.NoError(t, b.Store(ctx, "acme.status", "B", TierLongTerm, "user"))

	// T between birth (3d ago) and the archive (now): the value live then
	// was A, which sits in the archive row stamped ~now.
	got, err := b.ValueAsOf(ctx, "acme.status", time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "A", got.Value)
}

func TestValueAsOfDeletedKey(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	deletedAt := base.Add(2 * time.Hour)

	// Coherent deleted timeline: A born at base, deleted at deletedAt.
	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))
	backdateLiveCreated(t, b, "acme.status", base)
	require.NoError(t, b.Delete(ctx, "acme.status"))
	backdateHistory(t, b, "acme.status", 0, deletedAt) // deletion archive

	// While live: A (reconstructed from the deleted-reason archive row —
	// there is no live row anymore).
	got, err := b.ValueAsOf(ctx, "acme.status", base.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "A", got.Value)
	assert.Equal(t, "deleted", got.ArchivedReason, "value reconstructed from the deletion archive")

	// At/after deletion: gone. The deleted-reason row terminates the
	// timeline; nothing falls back to a live row.
	for _, asOf := range []time.Time{deletedAt, deletedAt.Add(time.Hour)} {
		got, err := b.ValueAsOf(ctx, "acme.status", asOf)
		require.NoError(t, err)
		assert.Nil(t, got, "asOf=%v", asOf)
	}
}

// TestValueAsOfDoesNotBumpAccessCount guards a subtle property: time-travel
// reads are historical queries and must not touch live-row bookkeeping
// (getLTM increments access_count as a side effect).
func TestValueAsOfDoesNotBumpAccessCount(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "acme.status", "A", TierLongTerm, "user"))

	_, err := b.ValueAsOf(ctx, "acme.status", time.Now())
	require.NoError(t, err)

	var accessCount int
	require.NoError(t, b.db.QueryRow(`SELECT access_count FROM brain_ltm WHERE key = ?`, "acme.status").Scan(&accessCount))
	assert.Equal(t, 1, accessCount, "ValueAsOf must not bump live access_count")
}

// TestBulkDuplicateSingleVersionFromReader covers the bd-31u write nuance
// from the read side: a bulk batch containing the same key twice archives
// once, against the pre-batch live value only — so the intermediate in-batch
// value is unreachable at every point in time.
func TestBulkDuplicateSingleVersionFromReader(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	base := time.Now().Add(-24 * time.Hour).Truncate(time.Second)

	require.NoError(t, b.Store(ctx, "acme.status", "pre-batch", TierLongTerm, "user"))
	backdateLiveCreated(t, b, "acme.status", base)
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "acme.status", Value: "in-batch-1", Tier: TierLongTerm, Source: "user"},
		{Key: "acme.status", Value: "in-batch-2", Tier: TierLongTerm, Source: "user"},
	}))

	hist, err := b.ListKeyHistory(ctx, "acme.status", 0)
	require.NoError(t, err)
	require.Len(t, hist, 1, "in-batch duplicates must not archive against each other")
	assert.Equal(t, "pre-batch", hist[0].Value)

	// Before the batch, the pre-batch value was live.
	got, err := b.ValueAsOf(ctx, "acme.status", base.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "pre-batch", got.Value)
	assert.NotEqual(t, "in-batch-1", got.Value)

	// Net-value semantics: a batch whose FIRST occurrence repeats the live
	// value but whose LAST value differs still archives the pre-batch value
	// exactly once (the batch moved the value away from it).
	require.NoError(t, b.Store(ctx, "net.key", "v0", TierLongTerm, "user"))
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "net.key", Value: "v0", Tier: TierLongTerm, Source: "user"},
		{Key: "net.key", Value: "v1", Tier: TierLongTerm, Source: "user"},
	}))
	hist, err = b.ListKeyHistory(ctx, "net.key", 0)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, "v0", hist[0].Value)
}

func TestRecentHistory(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// alpha archives a1 (when a2 lands); beta archives b1 (when b2 lands);
	// gamma never archives (single value).
	require.NoError(t, b.Store(ctx, "alpha.key", "a1", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "alpha.key", "a2", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "beta.key", "b1", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "beta.key", "b2", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "gamma.key", "g1", TierLongTerm, "user"))

	hist, err := b.RecentHistory(ctx, 10)
	require.NoError(t, err)
	require.Len(t, hist, 2)

	// Newest archive first across keys: beta/b1 archived after alpha/a1.
	assert.Equal(t, "beta.key", hist[0].Key)
	assert.Equal(t, "b1", hist[0].Value)
	assert.Equal(t, "alpha.key", hist[1].Key)
	assert.Equal(t, "a1", hist[1].Value)

	// Limit truncates to the newest.
	hist, err = b.RecentHistory(ctx, 1)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, "b1", hist[0].Value)

	// Zero or negative limit returns everything.
	hist, err = b.RecentHistory(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, hist, 2)
}
