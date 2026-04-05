package rem

import (
	"context"
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

	result, err := rem.Integrate(ctx, false)
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

	// Store entries with overlapping tokens
	require.NoError(t, b.Store(ctx, "pet.dog", "Theo is a golden retriever", brain.TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "friends.john", "John has a golden retriever too", brain.TierLongTerm, "test"))

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect relationship via token overlap (golden, retriever)
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

	result, err := rem.Integrate(ctx, false)
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
	result, err := rem.Integrate(ctx, true)
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

	result, err := rem.Integrate(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should skip integration and return empty result
	assert.Equal(t, 0, result.RelationshipsCreated)
	assert.Empty(t, result.Patterns)
}

func TestIntegrate_EmptyLTM(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	rem.config.IntegrationDay = int(time.Now().Weekday())

	result, err := rem.Integrate(ctx, false)
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

	result, err := rem.Integrate(ctx, false)
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

	result, err := rem.Integrate(ctx, false)
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

	result, err := rem.Integrate(ctx, false)
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
	_, err := rem.Integrate(ctx, false)
	require.NoError(t, err)

	_, err = rem.Integrate(ctx, false)
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
