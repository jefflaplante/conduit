package brain

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-2xd: per-entry importance weighting. A stored weight multiplier > 1.0
// boosts recall ranking (via effective salience) and exempts the entry from
// REM batch decay. The default weight is 1.0, which must be byte-identical
// to pre-weight behaviour.

// --- Migration ---

// Migration 10 adds brain_ltm.weight REAL NOT NULL DEFAULT 1.0; every
// pre-existing row reads as 1.0.
func TestMigration10_DefaultsExistingRowsToOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")

	// Build a v9 database and insert rows the way the pre-weight code did.
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE brain_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT (datetime('now')))`)
	require.NoError(t, err)
	for _, m := range migrations[:9] {
		_, err = db.Exec(m.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO brain_migrations (version) VALUES (?)`, m.Version)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO brain_ltm (key, value, access_count, salience) VALUES ('k', 'v', 1, 0.5)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	b, err := New(path, WithAutoFlushInterval(0))
	require.NoError(t, err)
	defer b.Close()

	var weight float64
	require.NoError(t, b.db.QueryRow(`SELECT weight FROM brain_ltm WHERE key = 'k'`).Scan(&weight))
	assert.InDelta(t, 1.0, weight, 1e-9, "pre-existing rows must default to weight 1.0")
}

// --- Store surface ---

// WithWeight persists weight on the LTM row; a plain Store (no option) keeps 1.0.
func TestStoreWeight_LTM(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "plain.key", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "scar.key", "v", TierLongTerm, "tool", WithWeight(2.0)))

	assert.InDelta(t, 1.0, storedWeight(t, b, "plain.key"), 1e-9)
	assert.InDelta(t, 2.0, storedWeight(t, b, "scar.key"), 1e-9)

	got, err := b.Get(ctx, "scar.key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.InDelta(t, 2.0, got.Weight, 1e-9, "Get must surface the stored weight")
}

// StoreBulk propagates BulkEntry.Weight; omitted weight stays 1.0.
func TestStoreBulk_Weight(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "bulk.plain", Value: "v", Tier: TierLongTerm},
		{Key: "bulk.heavy", Value: "v", Tier: TierLongTerm, Weight: 1.5},
	}))
	assert.InDelta(t, 1.0, storedWeight(t, b, "bulk.plain"), 1e-9)
	assert.InDelta(t, 1.5, storedWeight(t, b, "bulk.heavy"), 1e-9)
}

// Weight belongs to LTM entries; WM entries keep the neutral zero value
// (omitted from JSON, matching pre-weight behavior exactly).
func TestStoreWeight_WorkingTierIgnored(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "wm.key", "v", TierWorking, "tool", WithWeight(2.0)))
	got, err := b.Get(ctx, "wm.key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, TierWorking, got.Tier)
	assert.InDelta(t, 0.0, got.Weight, 1e-9, "WM entries are unaffected by weight")
}

// --- Recall scoring ---

// Two entries identical except for weight: the weighted one outranks the
// unweighted one in recall ordering.
func TestRecall_WeightedEntryOutranksUnweighted(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "plain.topic", "zebra migration patterns", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "scar.topic", "zebra migration patterns", TierLongTerm, "tool", WithWeight(2.0)))

	results, err := b.Recall(ctx, "zebra", 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "scar.topic", results[0].Key, "weight 2.0 must outrank weight 1.0 on identical content")
	assert.InDelta(t, 2.0, results[0].Weight, 1e-9, "recall results must carry weight")
	assert.InDelta(t, 1.0, results[1].Weight, 1e-9)
}

// Effective salience is multiplied by weight: an old weighted entry keeps the
// effective salience it would have had, times 2.
func TestEffectiveSalienceSQL_AppliesWeight(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "k", "v", TierLongTerm, "user", WithWeight(2.0)))
	ts := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	_, err := b.db.Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'k'`, ts.Format("2006-01-02 15:04:05"))
	require.NoError(t, err)

	// Unweighted reference: same row with weight reset to 1.0.
	_, err = b.db.Exec(`UPDATE brain_ltm SET weight = 1.0 WHERE key = 'k'`)
	require.NoError(t, err)
	var plain float64
	require.NoError(t, b.db.QueryRow(`SELECT `+b.EffectiveSalienceSQL()+` FROM brain_ltm WHERE key = 'k'`).Scan(&plain))

	_, err = b.db.Exec(`UPDATE brain_ltm SET weight = 2.0 WHERE key = 'k'`)
	require.NoError(t, err)
	var weighted float64
	require.NoError(t, b.db.QueryRow(`SELECT `+b.EffectiveSalienceSQL()+` FROM brain_ltm WHERE key = 'k'`).Scan(&weighted))

	assert.InDelta(t, plain*2.0, weighted, 1e-9)
	// Both stay within the weighted clamp.
	assert.GreaterOrEqual(t, weighted, 0.0)
	assert.LessOrEqual(t, weighted, 2.0, "clamped to [0, weight]")
}

// Weight 1.0 leaves the effective-salience expression unchanged: the SQL
// must evaluate to exactly the pre-weight formula.
func TestEffectiveSalienceSQL_WeightOneIsIdentity(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "k", "v", TierLongTerm, "user"))
	var eff float64
	require.NoError(t, b.db.QueryRow(`SELECT `+b.EffectiveSalienceSQL()+` FROM brain_ltm WHERE key = 'k'`).Scan(&eff))
	// Pre-weight expression (salience.go before bd-2xd), evaluated by SQLite.
	plainSQL := "MIN(1.0, MAX(0.0, salience + " + sqlFloat(b.recencyWeight) +
		" / (1.0 + MAX(0.0, (julianday('now') - julianday(accessed_at)) * 24.0) * " +
		sqlFloat(b.recencyDecayRate) + ")))"
	var plain float64
	require.NoError(t, b.db.QueryRow(`SELECT `+plainSQL+` FROM brain_ltm WHERE key = 'k'`).Scan(&plain))
	assert.InDelta(t, plain, eff, 1e-9, "weight 1.0 must reduce to the pre-weight expression")
}

// --- REM decay exemption ---
// (Tests live in internal/brain/rem/weight_rem_test.go — the consolidate
// phase and its test helpers are in the rem package; importing it here would
// create an import cycle.)

// --- Backward compatibility ---

// Omitting weight everywhere behaves exactly like weight 1.0: plain Store and
// Store rows must be indistinguishable, and recall must rank two identical
// weight-1.0 entries by the pre-existing signal (tie → stable insertion order
// is not guaranteed, but neither may outrank the other via weight).
func TestWeight_OmittedEqualsOne(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.plain", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "b.explicit", "v", TierLongTerm, "tool", WithWeight(1.0)))

	assert.InDelta(t, storedWeight(t, b, "a.plain"), storedWeight(t, b, "b.explicit"), 1e-9)

	r1, err := b.Recall(ctx, "v", 10)
	require.NoError(t, err)
	require.Len(t, r1, 2)
	for _, e := range r1 {
		assert.InDelta(t, 1.0, e.Weight, 1e-9)
	}
}

// Upsert of a weighted entry preserves its weight across a re-store that
// omits the option (weight is sticky, like created_at) — this is what keeps
// the WM promote/eviction rescue paths (which pass weightSet=false) from
// ever stripping a scar's protection. An explicit WithWeight re-store does
// overwrite.
func TestWeight_StickyAcrossUpsert(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "sticky.key", "v1", TierLongTerm, "tool", WithWeight(2.0)))
	require.NoError(t, b.Store(ctx, "sticky.key", "v2", TierLongTerm, "tool"))
	assert.InDelta(t, 2.0, storedWeight(t, b, "sticky.key"), 1e-9)

	// Bulk re-store without a weight is sticky too.
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{{Key: "sticky.key", Value: "v2b", Tier: TierLongTerm}}))
	assert.InDelta(t, 2.0, storedWeight(t, b, "sticky.key"), 1e-9)

	// And an explicit re-store with a different weight updates it.
	require.NoError(t, b.Store(ctx, "sticky.key", "v3", TierLongTerm, "tool", WithWeight(1.25)))
	assert.InDelta(t, 1.25, storedWeight(t, b, "sticky.key"), 1e-9)
}

func storedWeight(t *testing.T, b *Brain, key string) float64 {
	t.Helper()
	var w float64
	err := b.db.QueryRow(`SELECT weight FROM brain_ltm WHERE key = ?`, key).Scan(&w)
	require.NoError(t, err)
	return w
}
