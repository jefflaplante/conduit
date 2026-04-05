package searchdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// createTestBrainDB creates a minimal brain.db with the brain_ltm table.
func createTestBrainDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS brain_ltm (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			source TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			access_count INTEGER DEFAULT 1,
			salience REAL DEFAULT 0.5
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func setupBrainIndexerTest(t *testing.T) (*BrainIndexer, *sql.DB, func()) {
	t.Helper()
	tmpDir := t.TempDir()

	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	brainPath := filepath.Join(tmpDir, "brain.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)

	brainDB, err := createTestBrainDB(brainPath)
	require.NoError(t, err)

	indexer := NewBrainIndexer(sdb.DB(), brainDB)

	cleanup := func() {
		brainDB.Close()
		sdb.Close()
		gatewayDB.Close()
	}

	return indexer, brainDB, cleanup
}

func TestBrainIndexerEmpty(t *testing.T) {
	indexer, _, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	ctx := context.Background()
	err := indexer.IndexBrain(ctx)
	require.NoError(t, err)

	count, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestBrainIndexerWithEntries(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	// Insert test entries into brain.db
	_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
		"user.name", "Jeff LaPlante", "conversation")
	require.NoError(t, err)
	_, err = brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
		"project.main", "Conduit AI Gateway", "memory")
	require.NoError(t, err)
	_, err = brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
		"preference.editor", "vim", "")
	require.NoError(t, err)

	ctx := context.Background()
	err = indexer.IndexBrain(ctx)
	require.NoError(t, err)

	count, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestBrainIndexerSearch(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	// Insert test entries
	entries := []struct{ key, value, source string }{
		{"user.name", "Jeff LaPlante", "conversation"},
		{"project.main", "Conduit AI Gateway", "memory"},
		{"project.language", "Go programming language", "memory"},
		{"preference.editor", "vim text editor", "config"},
	}
	for _, e := range entries {
		_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
			e.key, e.value, e.source)
		require.NoError(t, err)
	}

	ctx := context.Background()
	require.NoError(t, indexer.IndexBrain(ctx))

	// Search for "conduit"
	results, err := indexer.SearchBrain(ctx, "conduit", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	foundConduit := false
	for _, r := range results {
		if r.Key == "project.main" {
			foundConduit = true
			assert.Contains(t, r.Value, "Conduit")
			break
		}
	}
	assert.True(t, foundConduit, "Expected to find entry with key 'project.main'")

	// Search for "programming"
	results, err = indexer.SearchBrain(ctx, "programming", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)
}

func TestBrainIndexerSkipUnchanged(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
		"test.key", "test value", "test")
	require.NoError(t, err)

	ctx := context.Background()

	// First index
	require.NoError(t, indexer.IndexBrain(ctx))
	count1, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, 1, count1)

	// Second index - should skip since data unchanged
	require.NoError(t, indexer.IndexBrain(ctx))
	count2, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, count1, count2)

	// Verify hash was set (internal detail, but confirms skip logic)
	assert.NotEmpty(t, indexer.lastHash)
}

func TestBrainIndexerNoMatches(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES (?, ?, ?)`,
		"user.name", "Jeff LaPlante", "conversation")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, indexer.IndexBrain(ctx))

	// Search for something that doesn't exist
	results, err := indexer.SearchBrain(ctx, "xylophone", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestBuildBrainFTSQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single word", "conduit", "conduit"},
		{"multiple words", "brain memory", "brain OR memory"},
		{"with special chars", "key:value (test)", "keyvalue OR test"},
		{"empty query", "", ""},
		{"only special chars", ":()+-", ""},
		{"stopword stripping", "what is the helm config", "helm OR config"},
		{"delimiter splitting", "helm/kustomize", "helm OR kustomize"},
		{"all stopwords fallback", "what is it", "what OR is OR it"},
		{"natural language", "who passed away recently", "passed OR away"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildBrainFTSQuery(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
