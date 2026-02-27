package searchdb

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveSearchDBPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple db path",
			input:    "gateway.db",
			expected: "gateway.search.db",
		},
		{
			name:     "config-prefixed path",
			input:    "config.telegram.db",
			expected: "config.telegram.search.db",
		},
		{
			name:     "absolute path",
			input:    "/var/data/gateway.db",
			expected: "/var/data/gateway.search.db",
		},
		{
			name:     "relative path with directory",
			input:    "./data/gateway.db",
			expected: "./data/gateway.search.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deriveSearchDBPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewSearchDB(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "test.search.db")
	gatewayPath := filepath.Join(tmpDir, "test.db")

	// Create a minimal gateway.db for testing
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	// Create search database
	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	// Verify it was created
	assert.Equal(t, searchPath, sdb.Path())
	assert.True(t, sdb.IsAvailable())
	assert.NotNil(t, sdb.DB())
	assert.NotNil(t, sdb.GatewayDB())
}

func TestNewSearchDBWithDerivedPath(t *testing.T) {
	tmpDir := t.TempDir()
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	expectedSearchPath := filepath.Join(tmpDir, "gateway.search.db")

	// Create a minimal gateway.db for testing
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	// Create search database with derived path (empty searchPath)
	sdb, err := NewSearchDB("", gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	assert.Equal(t, expectedSearchPath, sdb.Path())
}

func TestSearchDBMigrations(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "test.search.db")
	gatewayPath := filepath.Join(tmpDir, "test.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	// Verify migrations were applied by checking for tables
	var tableCount int
	err = sdb.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name IN ('document_chunks', 'search_schema_migrations')
	`).Scan(&tableCount)
	require.NoError(t, err)
	assert.Equal(t, 2, tableCount)

	// Verify FTS5 virtual tables exist
	var ftsCount int
	err = sdb.DB().QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name IN ('document_chunks_fts', 'beads_fts', 'messages_fts')
	`).Scan(&ftsCount)
	require.NoError(t, err)
	assert.Equal(t, 3, ftsCount)
}

func TestSearchDBPragmas(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "test.search.db")
	gatewayPath := filepath.Join(tmpDir, "test.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	// Verify WAL mode
	var journalMode string
	err = sdb.DB().QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	require.NoError(t, err)
	assert.Equal(t, "wal", journalMode)

	// Verify foreign keys enabled
	var foreignKeys int
	err = sdb.DB().QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
	require.NoError(t, err)
	assert.Equal(t, 1, foreignKeys)
}

func TestSearchDBGetStats(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "test.search.db")
	gatewayPath := filepath.Join(tmpDir, "test.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	stats, err := sdb.GetStats()
	require.NoError(t, err)

	assert.Contains(t, stats, "document_chunks")
	assert.Contains(t, stats, "messages_indexed")
	assert.Contains(t, stats, "beads_indexed")
	assert.Contains(t, stats, "path")
	assert.Equal(t, searchPath, stats["path"])
}

func TestSearchDBVacuum(t *testing.T) {
	tmpDir := t.TempDir()
	searchPath := filepath.Join(tmpDir, "test.search.db")
	gatewayPath := filepath.Join(tmpDir, "test.db")

	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	// Vacuum should not error on empty database
	err = sdb.Vacuum()
	assert.NoError(t, err)
}

// Helper to create a minimal gateway database for testing
func createTestGatewayDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Create minimal tables needed for message sync testing
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT PRIMARY KEY,
			session_key TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}
