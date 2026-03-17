package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary database for testing
func setupTestDB(t *testing.T) *sql.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	return db
}

// setupTestDBWithDSN creates a temporary database using BuildDSN
func setupTestDBWithDSN(t *testing.T) *sql.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", BuildDSN(dbPath))
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	return db
}

func TestConfigureDatabase(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Configure database (should apply pragmas and run migrations)
	err := ConfigureDatabase(db)
	if err != nil {
		t.Fatalf("Failed to configure database: %v", err)
	}

	// Verify that migrations table was created
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check for migrations table: %v", err)
	}

	if count != 1 {
		t.Error("Expected schema_migrations table to be created")
	}

	// Verify that migrations were applied
	err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}

	expectedMigrations := len(GetMigrations())
	if count != expectedMigrations {
		t.Errorf("Expected %d migrations to be applied, got %d", expectedMigrations, count)
	}
}

func TestRunMigrations(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Apply basic pragmas first
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("Failed to set WAL mode: %v", err)
	}

	// Run migrations
	err := RunMigrations(db)
	if err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	// Verify sessions table exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sessions'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check for sessions table: %v", err)
	}

	if count != 1 {
		t.Error("Expected sessions table to be created")
	}

	// Verify messages table exists
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check for messages table: %v", err)
	}

	if count != 1 {
		t.Error("Expected messages table to be created")
	}

	// Verify auth_tokens table exists
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='auth_tokens'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check for auth_tokens table: %v", err)
	}

	if count != 1 {
		t.Error("Expected auth_tokens table to be created")
	}

	// Verify that indexes were created for auth_tokens
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='auth_tokens'").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check for auth_tokens indexes: %v", err)
	}

	if count < 4 { // We expect at least 4 indexes on auth_tokens
		t.Errorf("Expected at least 4 indexes on auth_tokens table, got %d", count)
	}
}

func TestRunMigrationsIdempotent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Apply basic pragmas first
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("Failed to set WAL mode: %v", err)
	}

	// Run migrations twice - should be idempotent
	err := RunMigrations(db)
	if err != nil {
		t.Fatalf("Failed to run migrations first time: %v", err)
	}

	err = RunMigrations(db)
	if err != nil {
		t.Fatalf("Failed to run migrations second time (should be idempotent): %v", err)
	}

	// Verify migration count is still correct
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}

	expectedMigrations := len(GetMigrations())
	if count != expectedMigrations {
		t.Errorf("Expected %d migrations after running twice, got %d", expectedMigrations, count)
	}
}

func TestGetCurrentVersion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Before any migrations, version should be 0
	version, err := getCurrentVersion(db)
	if err != nil {
		t.Fatalf("Failed to get current version: %v", err)
	}

	if version != 0 {
		t.Errorf("Expected initial version 0, got %d", version)
	}

	// Create migrations table and add a migration
	err = ensureMigrationsTable(db)
	if err != nil {
		t.Fatalf("Failed to create migrations table: %v", err)
	}

	// Insert a test migration
	_, err = db.Exec("INSERT INTO schema_migrations (version, name) VALUES (?, ?)", 1, "test_migration")
	if err != nil {
		t.Fatalf("Failed to insert test migration: %v", err)
	}

	// Check version again
	version, err = getCurrentVersion(db)
	if err != nil {
		t.Fatalf("Failed to get current version after migration: %v", err)
	}

	if version != 1 {
		t.Errorf("Expected version 1 after migration, got %d", version)
	}
}

func TestGetMigrations(t *testing.T) {
	migrations := GetMigrations()

	if len(migrations) == 0 {
		t.Error("Expected at least one migration")
	}

	// Verify migrations are ordered by version
	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version <= migrations[i-1].Version {
			t.Error("Expected migrations to be ordered by version")
		}
	}

	// Verify each migration has required fields
	for _, migration := range migrations {
		if migration.Version <= 0 {
			t.Errorf("Migration %s has invalid version %d", migration.Name, migration.Version)
		}

		if migration.Name == "" {
			t.Errorf("Migration %d has empty name", migration.Version)
		}

		if migration.SQL == "" {
			t.Errorf("Migration %s has empty SQL", migration.Name)
		}
	}
}

func TestBuildDSN(t *testing.T) {
	dsn := BuildDSN("/tmp/test.db")

	// Must start with file: scheme
	if dsn[:5] != "file:" {
		t.Errorf("DSN should start with file:, got %s", dsn)
	}

	// Must contain key pragmas
	for _, want := range []string{"busy_timeout", "journal_mode", "synchronous", "cache_size", "foreign_keys"} {
		if !containsSubstring(dsn, want) {
			t.Errorf("DSN missing pragma %s: %s", want, dsn)
		}
	}

	// Must contain _txlock=immediate
	if !containsSubstring(dsn, "_txlock=immediate") {
		t.Errorf("DSN missing _txlock=immediate: %s", dsn)
	}
}

func TestPragmasAppliedToAllPoolConnections(t *testing.T) {
	db := setupTestDBWithDSN(t)
	defer db.Close()

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	ctx := context.Background()

	// Pin 4 connections to force the pool to create all of them
	conns := make([]*sql.Conn, 4)
	for i := 0; i < 4; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Failed to get connection %d: %v", i, err)
		}
		conns[i] = conn
	}

	// Verify each pinned connection has busy_timeout=5000
	for i, conn := range conns {
		var bt int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&bt); err != nil {
			t.Errorf("Connection %d: %v", i, err)
		} else if bt != 5000 {
			t.Errorf("Connection %d: busy_timeout=%d, want 5000", i, bt)
		}
	}

	// Release all pinned connections
	for _, conn := range conns {
		conn.Close()
	}
}

func TestConfigureDatabaseWithDSN(t *testing.T) {
	db := setupTestDBWithDSN(t)
	defer db.Close()

	if err := ConfigureDatabase(db); err != nil {
		t.Fatalf("ConfigureDatabase failed: %v", err)
	}

	// Verify migrations ran
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}
	if count != len(GetMigrations()) {
		t.Errorf("Expected %d migrations, got %d", len(GetMigrations()), count)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
