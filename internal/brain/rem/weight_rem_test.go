package rem

import (
	"context"
	"testing"
	"time"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-2xd: weight exempts entries from REM batch decay only. Boost, merge and
// the rest of the consolidate phases treat weighted entries identically to
// unweighted ones. The primary decay-exemption tests live in
// internal/brain/weight_test.go (the column and store path live there); these
// cover the remaining REM-phase interactions via the public cycle.

// An entry with weight 2.0 untouched for >7d is NOT decayed by Consolidate;
// an identical weight-1.0 entry is. MaxLTMEntries is forced low so the size
// gate opens and decay actually runs.
func TestConsolidateDecay_WeightExempt(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	oldTime := time.Now().Add(-10 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	require.NoError(t, b.Store(ctx, "protected.scar", "never forget", brain.TierLongTerm, "tool", brain.WithWeight(2.0)))
	_, err := b.DB().Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ? WHERE key = 'protected.scar'`, oldTime, oldTime)
	require.NoError(t, err)
	require.NoError(t, b.Store(ctx, "plain.old", "ordinary", brain.TierLongTerm, "tool"))
	_, err = b.DB().Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ? WHERE key = 'plain.old'`, oldTime, oldTime)
	require.NoError(t, err)

	rem.config.SalienceDecayRate = 0.1
	rem.config.MaxLTMEntries = 1 // force the size gate open

	result, err := rem.Consolidate(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SalienceDecayed, "only the weight-1.0 entry may be decayed")

	// Base salience written by Store: ltmBaseSalience(1) = 1/100*0.4 + 0.8*0.2.
	initial := 0.164
	var protected, plain float64
	require.NoError(t, b.DB().QueryRow(`SELECT salience FROM brain_ltm WHERE key = 'protected.scar'`).Scan(&protected))
	assert.InDelta(t, initial, protected, 1e-9, "weight 2.0 entry must be untouched by decay")
	require.NoError(t, b.DB().QueryRow(`SELECT salience FROM brain_ltm WHERE key = 'plain.old'`).Scan(&plain))
	assert.InDelta(t, initial-0.1, plain, 1e-9, "weight 1.0 entry decays by SalienceDecayRate")
}

// Dry-run counting must also skip weighted entries.
func TestConsolidateDecay_WeightExemptDryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	oldTime := time.Now().Add(-10 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	require.NoError(t, b.Store(ctx, "heavy.old", "v", brain.TierLongTerm, "tool", brain.WithWeight(2.0)))
	_, err := b.DB().Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'heavy.old'`, oldTime)
	require.NoError(t, err)
	require.NoError(t, b.Store(ctx, "light.old", "v", brain.TierLongTerm, "tool"))
	_, err = b.DB().Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'light.old'`, oldTime)
	require.NoError(t, err)

	rem.config.MaxLTMEntries = 1
	rem.config.SalienceDecayRate = 0.1

	result, err := rem.Consolidate(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SalienceDecayed, "dry run counts only unweighted entries")
}

// The REM boost phase (recently-accessed +0.05, capped at peak 1.0) must keep
// working for weighted entries — weight exempts from decay, nothing else.
func TestConsolidateBoost_WeightedEntryStillBoosted(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	// Weighted entry, accessed now → boost phase candidate.
	require.NoError(t, b.Store(ctx, "fresh.scar", "v", brain.TierLongTerm, "tool", brain.WithWeight(2.0)))

	rem.config.SalienceDecayRate = 0.1
	rem.config.MaxLTMEntries = 1

	result, err := rem.Consolidate(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SalienceBoosted, "weighted entries still get the recent-access boost")

	// Boost adds 0.05 capped at peak 1.0 → base cap = 1 - recencyWeight.
	// Fresh Store base (0.164) + 0.05 is well under the cap.
	var salience, weight float64
	require.NoError(t, b.DB().QueryRow(`SELECT salience, weight FROM brain_ltm WHERE key = 'fresh.scar'`).
		Scan(&salience, &weight))
	assert.InDelta(t, 2.0, weight, 1e-9)
	assert.InDelta(t, 0.164+0.05, salience, 1e-9, "boost applied unchanged for weighted entry")
}

// Weight does not leak into the merge-duplicates decision: identical keys
// normalize the same regardless of weight, and the merged row's weight must
// not contaminate the kept row.
func TestConsolidateMerge_WeightDoesNotAffectMerge(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	// Two keys that normalize identically; different weights.
	require.NoError(t, b.Store(ctx, "My.Key", "v", brain.TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "my.key ", "v", brain.TierLongTerm, "tool", brain.WithWeight(2.0)))
	oldTime := time.Now().Add(-10 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := b.DB().Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'My.Key'`, oldTime)
	require.NoError(t, err)

	rem.config.MaxLTMEntries = 1
	rem.config.SalienceDecayRate = 0.1

	result, err := rem.Consolidate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, result.Merged, 1, "duplicate keys still merge regardless of weight")

	var weight float64
	require.NoError(t, b.DB().QueryRow(`SELECT weight FROM brain_ltm WHERE key = 'My.Key'`).Scan(&weight))
	assert.InDelta(t, 1.0, weight, 1e-9, "merging into a plain key must not import the merged row's weight")
}
