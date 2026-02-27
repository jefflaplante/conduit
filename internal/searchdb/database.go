package searchdb

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// SearchDB owns a dedicated search.db for FTS5 indices and optional vector storage.
// This separates search concerns from the main gateway.db, allowing:
// - Independent search index rebuilds without affecting core data
// - Graceful degradation if search DB is unavailable
// - Future vector search integration
type SearchDB struct {
	db        *sql.DB
	path      string
	gatewayDB *sql.DB // Read-only reference for message sync
	mu        sync.RWMutex
}

// NewSearchDB creates a new search database at the given path.
// If searchPath is empty, it derives the path from gatewayDBPath (e.g., gateway.db → gateway.search.db).
func NewSearchDB(searchPath, gatewayDBPath string, gatewayDB *sql.DB) (*SearchDB, error) {
	if searchPath == "" {
		searchPath = deriveSearchDBPath(gatewayDBPath)
	}

	db, err := sql.Open("sqlite", searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open search database: %w", err)
	}

	sdb := &SearchDB{
		db:        db,
		path:      searchPath,
		gatewayDB: gatewayDB,
	}

	if err := sdb.configure(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure search database: %w", err)
	}

	if err := sdb.runMigrations(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run search migrations: %w", err)
	}

	log.Printf("SearchDB initialized at %s", searchPath)
	return sdb, nil
}

// DB returns the underlying database connection for use by indexers and searchers.
func (s *SearchDB) DB() *sql.DB {
	return s.db
}

// GatewayDB returns the gateway database reference for message sync.
func (s *SearchDB) GatewayDB() *sql.DB {
	return s.gatewayDB
}

// Path returns the database file path.
func (s *SearchDB) Path() string {
	return s.path
}

// Close closes the database connection.
func (s *SearchDB) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// IsAvailable checks if the search database is operational.
func (s *SearchDB) IsAvailable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return false
	}
	return s.db.Ping() == nil
}

// configure applies SQLite optimizations matching gateway.db settings.
func (s *SearchDB) configure() error {
	// Configure connection pool for SQLite
	s.db.SetMaxOpenConns(4)
	s.db.SetMaxIdleConns(2)
	s.db.SetConnMaxLifetime(0)

	// Apply SQLite performance configurations
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=10000",
		"PRAGMA foreign_keys=ON",
	}

	for _, pragma := range pragmas {
		if _, err := s.db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to apply pragma '%s': %w", pragma, err)
		}
	}

	return nil
}

// deriveSearchDBPath creates a search.db path from the gateway.db path.
// Examples:
//   - gateway.db → gateway.search.db
//   - config.telegram.db → config.telegram.search.db
//   - /path/to/data.db → /path/to/data.search.db
func deriveSearchDBPath(gatewayDBPath string) string {
	ext := filepath.Ext(gatewayDBPath)
	base := strings.TrimSuffix(gatewayDBPath, ext)
	return base + ".search" + ext
}
