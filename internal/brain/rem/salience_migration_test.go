package rem

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.53: after migration 9 converts legacy stored salience (which
// had recency = 1.0 baked in) to base salience, REM must reach exactly the
// decisions the legacy thresholds produced on the legacy values.

type legacyRow struct {
	key         string
	salience    float64 // legacy stored value
	accessCount int
	accessedAgo time.Duration
}

// openLegacyBrain creates a brain DB, rewinds it to schema v8 holding legacy
// salience values, then reopens it so migration 9 runs.
func openLegacyBrain(t *testing.T, rows []legacyRow) (*REMCycle, *brain.Brain) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(path, brain.WithAutoFlushInterval(0))
	require.NoError(t, err)
	require.NoError(t, b.Close())

	db, err := sql.Open("sqlite", database.BuildDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM brain_migrations WHERE version = 9`)
	require.NoError(t, err)
	for _, r := range rows {
		ts := time.Now().Add(-r.accessedAgo).UTC().Format("2006-01-02 15:04:05")
		created := time.Now().Add(-60 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
		_, err := db.Exec(`INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
			VALUES (?, ?, 'test', ?, ?, ?, ?)`, r.key, "value of "+r.key, created, ts, r.accessCount, r.salience)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	b, err = brain.New(path, brain.WithAutoFlushInterval(0))
	require.NoError(t, err)
	t.Cleanup(func() { b.Close() })
	cyc := NewREMCycle(b, b.DB(), REMConfig{
		PruneAgeDays:      30,
		SalienceDecayRate: 0.1,
		MaxLTMEntries:     1, // exercise the at-capacity paths
		WorkspaceDir:      t.TempDir(),
	})
	return cyc, b
}

func peakOf(t *testing.T, r *REMCycle, key string) float64 {
	t.Helper()
	var v float64
	require.NoError(t, r.db.QueryRow(`SELECT `+r.peakSalienceSQL()+` FROM brain_ltm WHERE key = ?`, key).Scan(&v))
	return v
}

func TestREM_DecisionsUnchangedByBaseSalienceMigration(t *testing.T) {
	day := 24 * time.Hour
	cyc, _ := openLegacyBrain(t, []legacyRow{
		{"legacy.low", 0.05, 2, 40 * day},   // legacy: pruned (0.05 < 0.1, >30d)
		{"legacy.edge", 0.1, 2, 40 * day},   // legacy: kept (not < 0.1) — but decays first
		{"legacy.mid", 0.6, 5, 40 * day},    // legacy: decays to 0.5, kept
		{"legacy.floor", 0.05, 2, 10 * day}, // legacy: decays to 0.0 (floor), too young to prune
		{"legacy.top", 0.98, 9, time.Hour},  // legacy: boosted, capped at 1.0
		{"legacy.fresh", 0.5, 1, time.Hour}, // legacy flat insert: boosted to 0.55
	})
	ctx := context.Background()

	cons, err := cyc.Consolidate(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 4, cons.SalienceDecayed)
	assert.Equal(t, 2, cons.SalienceBoosted)
	assert.InDelta(t, 0.5, peakOf(t, cyc, "legacy.mid"), 1e-9)
	assert.InDelta(t, 0.0, peakOf(t, cyc, "legacy.floor"), 1e-9)
	assert.InDelta(t, 1.0, peakOf(t, cyc, "legacy.top"), 1e-9)
	assert.InDelta(t, 0.55, peakOf(t, cyc, "legacy.fresh"), 1e-9)

	pr, err := cyc.Prune(ctx, false)
	require.NoError(t, err)
	var archived []string
	for _, a := range pr.Archived {
		if a.Reason == "low_salience" {
			archived = append(archived, a.Key)
		}
	}
	// legacy.low (0.05→0.0 after decay) and legacy.edge (0.1→0.0) are both
	// < 0.1 and older than 30 days — the same set the legacy code archived.
	assert.ElementsMatch(t, []string{"legacy.low", "legacy.edge"}, archived)

	var archSal float64
	require.NoError(t, cyc.db.QueryRow(`SELECT salience FROM brain_archive WHERE key = 'legacy.low'`).Scan(&archSal))
	assert.InDelta(t, 0.0, archSal, 1e-9, "archive records peak salience, as before")
}

func TestREM_IntegrationThresholdsUsePeakSalience(t *testing.T) {
	cyc, _ := openLegacyBrain(t, []legacyRow{
		{"imp.a", 0.8, 3, time.Hour},
		{"imp.b", 0.75, 3, time.Hour},
		{"imp.c", 0.71, 3, time.Hour},
		{"imp.d", 0.9, 3, time.Hour},
		{"low.e", 0.6, 3, time.Hour},
	})
	res, err := cyc.Integrate(context.Background(), true, true)
	require.NoError(t, err)
	found := false
	for _, p := range res.Patterns {
		if containsString(p, "4 high-importance memories") {
			found = true
		}
	}
	assert.True(t, found, "legacy salience > 0.7 rows must still count as high-importance: %v", res.Patterns)
}
