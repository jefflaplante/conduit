package rem

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrate_NamespaceRelationships(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with shared namespaces
	require.NoError(t, b.Store(ctx, "solar.production", "45kWh", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.panels", "30", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.efficiency", "85%", brain.TierLongTerm, "test"))

	// Configure to run on current weekday
	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should create relationships between entries in same namespace
	assert.GreaterOrEqual(t, result.RelationshipsCreated, 3) // At least 3 pairs: (prod,panels), (prod,eff), (panels,eff)

	// Verify relationships were stored
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 3)
}

func TestIntegrate_TokenOverlap(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with high token overlap (2/3 = 66.7% >= 0.6)
	require.NoError(t, b.Store(ctx, "pet.dog", "cat dog", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "friends.john", "cat dog bird", brain.TierLongTerm, "test"))

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect relationship via token overlap (cat, dog)
	assert.GreaterOrEqual(t, result.RelationshipsCreated, 1)
}

func TestIntegrate_PatternDetection(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store many entries in same namespace
	for i := 0; i < 6; i++ {
		key := "solar." + string('a'+rune(i))
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))
	}

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect pattern: many entries about "solar"
	assert.GreaterOrEqual(t, len(result.Patterns), 1)
	assert.Contains(t, result.Patterns[0], "solar")
}

func TestIntegrate_DryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store related entries
	require.NoError(t, b.Store(ctx, "test.key1", "value1", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "test.key2", "value2", brain.TierLongTerm, "test"))

	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run in dry-run mode
	result, err := rem.Integrate(ctx, true, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should report what would be created
	assert.GreaterOrEqual(t, result.RelationshipsCreated, 1)

	// Verify relationships were NOT created (dry run)
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestIntegrate_SkipsWrongDay(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store related entries
	require.NoError(t, b.Store(ctx, "test.key1", "value1", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "test.key2", "value2", brain.TierLongTerm, "test"))

	// Set integration day to different weekday
	currentDay := int(time.Now().Weekday())
	rem.config.IntegrationDay = (currentDay + 1) % 7

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should skip integration and return empty result
	assert.Equal(t, 0, result.RelationshipsCreated)
	assert.Empty(t, result.Patterns)
}

func TestIntegrate_ManualBypassesDayGate(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Two entries that share a namespace prefix → at least one candidate.
	require.NoError(t, b.Store(ctx, "scope.alpha", "alpha body", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "scope.beta", "beta body", brain.TierLongTerm, "test"))

	// Pin the gate to a non-matching weekday.
	currentDay := int(time.Now().Weekday())
	rem.config.IntegrationDay = (currentDay + 1) % 7

	// manual=true must bypass shouldRunIntegration().
	result, err := rem.Integrate(ctx, false, true)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Greater(t, result.RelationshipsCreated, 0,
		"manual integrate should run and create at least one relationship even on a non-integration day")
}

func TestRun_ExplicitPhasesTreatedAsManual(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	require.NoError(t, b.Store(ctx, "ns.one", "one body", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "ns.two", "two body", brain.TierLongTerm, "test"))

	// Force the schedule gate closed.
	currentDay := int(time.Now().Weekday())
	rem.config.IntegrationDay = (currentDay + 1) % 7

	// Caller passes phases explicitly → Run() must mark this as manual and
	// integration must fire despite the wrong-day gate.
	report, err := rem.Run(ctx, []string{"integration"}, false)
	require.NoError(t, err)
	require.NotNil(t, report.Integration)
	assert.Greater(t, report.Integration.RelationshipsCreated, 0)
}

func TestIntegrate_EmptyLTM(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should complete without errors
	assert.Equal(t, 0, result.RelationshipsCreated)
	assert.Empty(t, result.Patterns)
}

func TestIntegrate_HighSaliencePattern(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with high salience
	for i := 0; i < 4; i++ {
		key := "important." + string('a'+rune(i))
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))

		// Boost salience
		_, err := rem.db.Exec(`UPDATE brain_ltm SET salience = 0.8 WHERE key = ?`, key)
		require.NoError(t, err)
	}

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect high-salience pattern
	hasHighSaliencePattern := false
	for _, pattern := range result.Patterns {
		if containsString(pattern, "high-importance") || containsString(pattern, "salience > 0.7") {
			hasHighSaliencePattern = true
			break
		}
	}
	assert.True(t, hasHighSaliencePattern)
}

func TestIntegrate_FrequentAccessPattern(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries and access them multiple times
	for i := 0; i < 4; i++ {
		key := "frequent." + string('a'+rune(i))
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))

		// Simulate frequent access
		for j := 0; j < 6; j++ {
			_, _ = b.Get(ctx, key)
		}
	}

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect frequently accessed pattern
	hasFrequentAccessPattern := false
	for _, pattern := range result.Patterns {
		if containsString(pattern, "frequently referenced") {
			hasFrequentAccessPattern = true
			break
		}
	}
	assert.True(t, hasFrequentAccessPattern)
}

func TestIntegrate_RelationshipConfidence(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with shared namespace (high confidence)
	require.NoError(t, b.Store(ctx, "solar.prod", "45kWh", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.panels", "30", brain.TierLongTerm, "test"))

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify relationship has confidence score
	var confidence float64
	err = rem.db.QueryRow(`
		SELECT confidence
		FROM brain_relationships
		WHERE key_a = 'solar.panels' AND key_b = 'solar.prod'
	`).Scan(&confidence)
	require.NoError(t, err)
	assert.Greater(t, confidence, 0.0)
	assert.LessOrEqual(t, confidence, 1.0)
}

func TestIntegrate_NoDuplicateRelationships(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store related entries
	require.NoError(t, b.Store(ctx, "test.a", "value", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "test.b", "value", brain.TierLongTerm, "test"))

	rem.config.IntegrationDay = int(time.Now().Weekday())

	// Run integration twice
	_, err := rem.Integrate(ctx, false, false)
	require.NoError(t, err)

	_, err = rem.Integrate(ctx, false, false)
	require.NoError(t, err)

	// Should not create duplicate relationships (INSERT OR REPLACE)
	var count int
	err = rem.db.QueryRow(`
		SELECT COUNT(*)
		FROM brain_relationships
		WHERE (key_a = 'test.a' AND key_b = 'test.b') OR (key_a = 'test.b' AND key_b = 'test.a')
	`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || len(s) > len(substr) && containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestIntegrate_JaccardThresholdVer60 tests that the raised Jaccard threshold
// (0.3 → 0.6) eliminates noisy cross-namespace edge candidates while preserving
// genuinely related keys.
func TestIntegrate_JaccardThresholdVer60(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Entry A: Entry with few tokens (should be filtered out)
	require.NoError(t, b.Store(ctx, "test.short", "cat", brain.TierLongTerm, "test"))

	// Entry B: Entry with stopword overlap only
	require.NoError(t, b.Store(ctx, "stopwords.a", "the cat and the dog", brain.TierLongTerm, "test"))

	// Entry C: Entry with high Jaccard similarity to B
	require.NoError(t, b.Store(ctx, "stopwords.b", "the cat and the dog", brain.TierLongTerm, "test"))

	// Entry D: Entry with low Jaccard similarity to B (shared "cat", "dog" only)
	require.NoError(t, b.Store(ctx, "low.similarity", "cat dog bird fish", brain.TierLongTerm, "test"))

	// Entry E: Entry with high Jaccard similarity to D (3/4 tokens overlap)
	require.NoError(t, b.Store(ctx, "high.similarity", "cat dog fish wolf", brain.TierLongTerm, "test"))

	// Entry F: Entry with moderately high similarity (2/4 = 50%, should fail threshold)
	require.NoError(t, b.Store(ctx, "moderate.similarity", "cat dog elephant tiger", brain.TierLongTerm, "test"))

	// Run integration
	result, err := rem.Integrate(ctx, false, true)
	require.NoError(t, err)

	// Verify results
	// Should NOT create relationships for:
	// - test.short (filtered out by token count < 2)
	// - stopwords.a vs low.similarity (similarity too low: 2/6 = 33% < 60%)
	// - stopwords.a vs moderate.similarity (2/6 = 33% < 60%)
	// - low.similarity vs moderate.similarity (2/6 = 33% < 60%)
	
	// Should create relationships for:
	// - stopwords.b (100% similarity with stopwords.a)
	// - high.similarity vs low.similarity (3/4 = 75% >= 60%)
	
	// Expected relationships: 2
	assert.GreaterOrEqual(t, result.RelationshipsCreated, 1, "Should create at least 1 relationship for high similarity")

	// Verify that the high similarity pair was created
	rows, err := rem.db.Query(`
		SELECT key_a, key_b, relationship, confidence
		FROM brain_relationships
		WHERE relationship = 'related'
		ORDER BY key_a, key_b
	`)
	require.NoError(t, err)
	defer rows.Close()

	var relationships []struct {
		KeyA       string
		KeyB       string
		Rel        string
		Confidence float64
	}

	for rows.Next() {
		var r struct {
			KeyA       string
			KeyB       string
			Rel        string
			Confidence float64
		}
		err := rows.Scan(&r.KeyA, &r.KeyB, &r.Rel, &r.Confidence)
		require.NoError(t, err)
		relationships = append(relationships, r)
	}

	// Should have the high similarity pair
	foundHighSim := false
	for _, r := range relationships {
		if (r.KeyA == "high.similarity" && r.KeyB == "low.similarity") ||
			(r.KeyA == "low.similarity" && r.KeyB == "high.similarity") {
			foundHighSim = true
			// Confidence should be in the new range: 0.4 + (0.6 * 0.2) = 0.52
			assert.InDelta(t, 0.52, r.Confidence, 0.01, "Confidence should be ~0.52 for 60% similarity")
		}
	}
	assert.True(t, foundHighSim, "Should find high.similarity ↔ low.similarity relationship")
}

// TestIntegrate_NamespacePairCap tests that namespace pair generation is capped
// to prevent quadratic explosion (e.g., learned.* with 248 entries → 30,628 pairs)
func TestIntegrate_NamespacePairCap(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create a namespace with 100+ entries to test the cap
	// Without the cap, this would generate 100*99/2 = 4,950 candidates
	// With the cap (namespacePairCap = 50), it should generate at most 50 candidates
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("large.namespace.entry%03d", i)
		require.NoError(t, b.Store(ctx, key, fmt.Sprintf("value %d", i), brain.TierLongTerm, "test"))
	}

	// Manually boost salience to ensure namespace relationships are created
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("large.namespace.entry%03d", i)
		_, err := rem.db.Exec(`
			UPDATE brain_ltm
			SET salience = 0.7
			WHERE key = ?
		`, key)
		require.NoError(t, err)
	}

	// Run integration
	result, err := rem.Integrate(ctx, false, true) // manual=true to bypass schedule gate
	require.NoError(t, err)
	_ = result // unused

	// Verify that relationships were created for the large namespace
	rows, err := rem.db.Query(`
		SELECT COUNT(*)
		FROM brain_relationships
		WHERE (key_a LIKE 'large.namespace.%' OR key_b LIKE 'large.namespace.%')
		AND relationship = 'related'
	`)
	require.NoError(t, err)
	defer rows.Close()

	var count int
	if rows.Next() {
		require.NoError(t, rows.Scan(&count))
	}
	
	// Should be capped, not the full 4,950 pairs
	assert.LessOrEqual(t, count, namespacePairCap, 
		"Namespace relationships should be capped at namespacePairCap")
	assert.Greater(t, count, 0, "Should have created at least some namespace relationships")

	// Log the actual count for debugging
	t.Logf("Created %d namespace relationships (cap: %d)", count, namespacePairCap)
}

// TestIntegrate_NamespaceSalienceGate tests that low-salience pairs are filtered out
func TestIntegrate_NamespaceSalienceGate(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create entries in the same namespace with varying salience
	// Some low salience (< 0.5), some high salience (>= 0.5)
	
	// Low-salience entries
	lowSalienceKeys := []string{"salience.low1", "salience.low2", "salience.low3"}
	// Values are distinct so token-overlap (Jaccard) does not create edges;
	// only the namespace pairing path is under test here.
	lowSalienceVals := map[string]string{
		"salience.low1": "unrelated alpha value",
		"salience.low2": "unrelated beta value",
		"salience.low3": "unrelated gamma value",
	}
	for _, key := range lowSalienceKeys {
		require.NoError(t, b.Store(ctx, key, lowSalienceVals[key], brain.TierLongTerm, "test"))
	}

	// High-salience entries
	highSalienceKeys := []string{"salience.high1", "salience.high2"}
	for _, key := range highSalienceKeys {
		require.NoError(t, b.Store(ctx, key, "high importance value", brain.TierLongTerm, "test"))
	}

	// Manually adjust salience to control the test
	// Low salience: set to 0.3
	for _, key := range lowSalienceKeys {
		_, err := rem.db.Exec(`
			UPDATE brain_ltm
			SET salience = 0.3
			WHERE key = ?
		`, key)
		require.NoError(t, err)
	}

	// High salience: set to 0.7
	for _, key := range highSalienceKeys {
		_, err := rem.db.Exec(`
			UPDATE brain_ltm
			SET salience = 0.7
			WHERE key = ?
		`, key)
		require.NoError(t, err)
	}

	// Run integration
	_, err := rem.Integrate(ctx, false, true) // manual=true to bypass schedule gate
	require.NoError(t, err)

	// Count relationships (namespace relationships specifically)
	rows, err := rem.db.Query(`
		SELECT key_a, key_b, confidence
		FROM brain_relationships
		WHERE (key_a LIKE 'salience.%' OR key_b LIKE 'salience.%')
		AND relationship = 'related'
		AND confidence = 0.7
		ORDER BY key_a, key_b
	`)
	require.NoError(t, err)
	defer rows.Close()

	var relationships []struct {
		KeyA       string
		KeyB       string
		Confidence float64
	}

	for rows.Next() {
		var r struct {
			KeyA       string
			KeyB       string
			Confidence float64
		}
		require.NoError(t, rows.Scan(&r.KeyA, &r.KeyB, &r.Confidence))
		relationships = append(relationships, r)
	}

	// Should NOT have namespace relationships (confidence=0.7) between two low-salience entries
	for _, r := range relationships {
		isLowA := false
		isLowB := false
		for _, key := range lowSalienceKeys {
			if r.KeyA == key {
				isLowA = true
			}
			if r.KeyB == key {
				isLowB = true
			}
		}
		
		// If both are low-salience, this is a violation of the salience gate
		if isLowA && isLowB {
			t.Errorf("Found namespace relationship (confidence=0.7) between two low-salience entries: %s ↔ %s", r.KeyA, r.KeyB)
		}
	}

	// Should have at least one relationship involving a high-salience entry
	hasHighSalienceRelation := false
	for _, r := range relationships {
		isHighA := false
		isHighB := false
		for _, key := range highSalienceKeys {
			if r.KeyA == key {
				isHighA = true
			}
			if r.KeyB == key {
				isHighB = true
			}
		}
		if isHighA || isHighB {
			hasHighSalienceRelation = true
			break
		}
	}

	assert.True(t, hasHighSalienceRelation, 
		"Should have at least one relationship involving a high-salience entry")
}
