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

// conduit-31jg.53: brain_ltm.salience holds base salience only; recency is
// computed at query time.

func storedSalience(t *testing.T, b *Brain, key string) float64 {
	t.Helper()
	var s float64
	require.NoError(t, b.db.QueryRow(`SELECT salience FROM brain_ltm WHERE key = ?`, key).Scan(&s))
	return s
}

func TestLTM_StoresBaseSalienceWithoutRecency(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("u")

	require.NoError(t, b.Store(ctx, "fact.a", "v", TierLongTerm, "user"))
	// access=1/100*0.4 + 0.8*0.2 = 0.164; no recency term stored.
	assert.InDelta(t, 0.164, storedSalience(t, b, "fact.a"), 1e-9)

	// Re-store bumps access_count to 2 and recomputes base (still no recency).
	require.NoError(t, b.Store(ctx, "fact.a", "v2", TierLongTerm, "user"))
	assert.InDelta(t, 0.168, storedSalience(t, b, "fact.a"), 1e-9)

	// StoreBulk writes the same base values.
	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{{Key: "fact.b", Value: "v", Tier: TierLongTerm}}))
	assert.InDelta(t, 0.164, storedSalience(t, b, "fact.b"), 1e-9)

	// Get recomputes base for access_count=3 and returns the effective value:
	// just accessed, so recency = 1 → base + recencyWeight.
	e, err := b.Get(ctx, "fact.a")
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.InDelta(t, 0.172, storedSalience(t, b, "fact.a"), 1e-9)
	assert.InDelta(t, 0.572, e.Salience, 1e-3)
}

func TestLTM_RecencyComputedAtQueryTime(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("u")

	require.NoError(t, b.Store(ctx, "topic.fresh", "zebra fact", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "topic.stale", "zebra fact", TierLongTerm, "user"))
	// Same base salience; "stale" was last accessed 48h ago.
	_, err := b.db.Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'topic.stale'`, sqlTime(48*time.Hour))
	require.NoError(t, err)

	res, err := b.Recall(ctx, "zebra", 10)
	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, "topic.fresh", res[0].Key)
	assert.Equal(t, "topic.stale", res[1].Key)
	// Effective salience: base 0.164 + 0.4/(1+48) ≈ 0.172 for the stale row.
	assert.InDelta(t, 0.164+0.4/49.0, res[1].Salience, 2e-3)
	assert.InDelta(t, 0.564, res[0].Salience, 2e-3)

	// List reports effective salience too. (Recall bumped accessed_at on both
	// hits, so backdate again.)
	_, err = b.db.Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'topic.stale'`, sqlTime(48*time.Hour))
	require.NoError(t, err)
	list, err := b.List(ctx, "topic.", "")
	require.NoError(t, err)
	got := map[string]float64{}
	for _, e := range list {
		got[e.Key] = e.Salience
	}
	assert.Greater(t, got["topic.fresh"], got["topic.stale"])
}

func TestEffectiveSalienceSQLMatchesGo(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "k", "v", TierLongTerm, "user"))
	for _, age := range []time.Duration{0, 30 * time.Minute, 5 * time.Hour, 72 * time.Hour} {
		ts := time.Now().Add(-age).UTC().Truncate(time.Second)
		_, err := b.db.Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'k'`, ts.Format("2006-01-02 15:04:05"))
		require.NoError(t, err)
		var sqlVal float64
		require.NoError(t, b.db.QueryRow(`SELECT `+b.EffectiveSalienceSQL()+` FROM brain_ltm WHERE key = 'k'`).Scan(&sqlVal))
		assert.InDelta(t, b.effectiveSalience(0.164, ts, time.Now()), sqlVal, 1e-3, "age %s", age)
		var peak float64
		require.NoError(t, b.db.QueryRow(`SELECT `+b.PeakSalienceSQL()+` FROM brain_ltm WHERE key = 'k'`).Scan(&peak))
		assert.InDelta(t, 0.564, peak, 1e-9)
	}
}

// Eviction must rank by query-time recency: an old high-base row loses to a
// slightly lower-base row that was touched recently (but outside the grace).
func TestLTMEviction_UsesQueryTimeRecency(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(2), WithSpreadingEnabled(false))
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "old.valuable", "v", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "recent.plain", "v", TierLongTerm, "user"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET salience = salience + 0.1, created_at = ?, accessed_at = ? WHERE key = 'old.valuable'`,
		sqlTime(72*time.Hour), sqlTime(72*time.Hour))
	require.NoError(t, err)
	_, err = b.db.Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ? WHERE key = 'recent.plain'`,
		sqlTime(72*time.Hour), sqlTime(90*time.Minute))
	require.NoError(t, err)

	require.NoError(t, b.Store(ctx, "new.fact", "v", TierLongTerm, "user"))
	assert.True(t, ltmHas(t, b, "new.fact"))
	assert.True(t, ltmHas(t, b, "recent.plain"), "recently touched row must survive")
	assert.False(t, ltmHas(t, b, "old.valuable"))
	assert.Equal(t, "capacity", archiveReason(t, b, "old.valuable"))
}

func TestWithLTMEvictionGraceOption(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(1), WithLTMEvictionGrace(10*time.Hour), WithSpreadingEnabled(false))
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "a", "v", TierLongTerm, "user"))
	ts := sqlTime(5 * time.Hour)
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ?`, ts, ts)
	require.NoError(t, err)
	require.NoError(t, b.Store(ctx, "b", "v", TierLongTerm, "user"))
	// "a" is 5h old, inside the 10h grace: nothing may be evicted.
	assert.Equal(t, 2, ltmCount(t, b))
}

// Migration 9 subtracts the legacy baked-in recency contribution exactly once.
func TestMigration9_ConvertsLegacySalienceOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")

	// Build a v8 database with legacy stored values.
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE brain_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT (datetime('now')))`)
	require.NoError(t, err)
	for _, m := range migrations[:8] {
		_, err := db.Exec(m.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO brain_migrations (version) VALUES (?)`, m.Version)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO brain_ltm (key, value, access_count, salience) VALUES
		('touched', 'v', 5, 0.58), ('flat', 'v', 1, 0.5), ('boosted', 'v', 9, 1.0), ('decayed', 'v', 1, 0.05)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	want := map[string]float64{"touched": 0.18, "flat": 0.1, "boosted": 0.6, "decayed": -0.35}
	for i := 0; i < 2; i++ { // second open must be a no-op
		b, err := New(path, WithAutoFlushInterval(0))
		require.NoError(t, err)
		for k, v := range want {
			assert.InDelta(t, v, storedSalience(t, b, k), 1e-9, "open %d key %s", i, k)
			// Peak salience equals the legacy stored value.
			var peak float64
			require.NoError(t, b.db.QueryRow(`SELECT `+b.PeakSalienceSQL()+` FROM brain_ltm WHERE key = ?`, k).Scan(&peak))
			assert.InDelta(t, v+0.4, peak, 1e-9)
		}
		require.NoError(t, b.Close())
	}
}

// A recency weight configured away from the default is what the legacy
// upsert baked in, so the migration must subtract that value.
func TestMigration9_UsesConfiguredRecencyWeight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE brain_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT (datetime('now')))`)
	require.NoError(t, err)
	for _, m := range migrations[:8] {
		_, err := db.Exec(m.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO brain_migrations (version) VALUES (?)`, m.Version)
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO brain_ltm (key, value, access_count, salience) VALUES ('k', 'v', 1, 0.7)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	b, err := New(path, WithAutoFlushInterval(0), WithRecencyWeight(0.25))
	require.NoError(t, err)
	defer b.Close()
	assert.InDelta(t, 0.45, storedSalience(t, b, "k"), 1e-9)
}
