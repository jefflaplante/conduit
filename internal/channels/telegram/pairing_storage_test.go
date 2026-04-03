package telegram

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	// Create the telegram_pairings table
	_, err = db.Exec(`
		CREATE TABLE telegram_pairings (
			code TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			metadata TEXT DEFAULT '{}'
		)
	`)
	require.NoError(t, err)

	return db
}

// Helper to insert a pairing with specific times using UTC
// SQLite datetime('now') returns UTC so we must use UTC times
func insertPairing(t *testing.T, db *sql.DB, code, userID string, createdAt, expiresAt time.Time, isActive bool) {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	// Convert to UTC for SQLite compatibility
	_, err := db.Exec(`
		INSERT INTO telegram_pairings (code, user_id, created_at, expires_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, '{}')
	`, code, userID, createdAt.UTC().Format("2006-01-02 15:04:05"), expiresAt.UTC().Format("2006-01-02 15:04:05"), activeInt)
	require.NoError(t, err)
}

func TestNewPairingStorage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)
	assert.NotNil(t, storage)
	assert.Equal(t, db, storage.db)
}

func TestPairingStorage_CreatePairing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Create a pairing
	pairing, err := storage.CreatePairing("user123", 1*time.Hour)
	require.NoError(t, err)
	require.NotNil(t, pairing)

	assert.NotEmpty(t, pairing.Code)
	assert.Equal(t, "user123", pairing.UserID)
	assert.True(t, pairing.IsActive)
	assert.True(t, pairing.ExpiresAt.After(time.Now()))
}

func TestPairingStorage_GetPairingByCode(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert a pairing directly
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "test-code-123", "user123", now, expiresAt, true)

	// Retrieve it
	retrieved, err := storage.GetPairingByCode("test-code-123")
	require.NoError(t, err)
	require.NotNil(t, retrieved)

	assert.Equal(t, "test-code-123", retrieved.Code)
	assert.Equal(t, "user123", retrieved.UserID)
	assert.True(t, retrieved.IsActive)
}

func TestPairingStorage_GetPairingByCode_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	_, err := storage.GetPairingByCode("nonexistent-code")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPairingStorage_ApprovePairing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert a pairing with future expiration (in UTC for SQLite comparison)
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "test-code-approve", "user123", now, expiresAt, true)

	// Approve it
	err := storage.ApprovePairing("test-code-approve")
	require.NoError(t, err)

	// Verify it's inactive
	retrieved, err := storage.GetPairingByCode("test-code-approve")
	require.NoError(t, err)
	assert.False(t, retrieved.IsActive)
}

func TestPairingStorage_ApprovePairing_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	err := storage.ApprovePairing("nonexistent-code")
	assert.Error(t, err)
}

func TestPairingStorage_ApprovePairing_AlreadyInactive(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert an already inactive pairing
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "test-code-inactive", "user123", now, expiresAt, false)

	// Try to approve
	err := storage.ApprovePairing("test-code-inactive")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already used")
}

func TestPairingStorage_ApprovePairing_Expired(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert an expired pairing (in UTC)
	now := time.Now().UTC()
	expiresAt := now.Add(-1 * time.Hour) // expired
	insertPairing(t, db, "test-code-expired", "user123", now.Add(-2*time.Hour), expiresAt, true)

	// Try to approve - should fail
	err := storage.ApprovePairing("test-code-expired")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestPairingStorage_ListPendingPairings(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert active pairings with future expiration (in UTC for datetime('now') comparison)
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "code1", "user1", now, expiresAt, true)
	insertPairing(t, db, "code2", "user2", now, expiresAt, true)

	// List pending
	pairings, err := storage.ListPendingPairings()
	require.NoError(t, err)
	assert.Len(t, pairings, 2)
}

func TestPairingStorage_ListPendingPairings_ExcludesApproved(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert pairings (in UTC)
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "code1", "user1", now, expiresAt, false) // already approved
	insertPairing(t, db, "code2", "user2", now, expiresAt, true)  // active

	// Should only list the pending one
	pairings, err := storage.ListPendingPairings()
	require.NoError(t, err)
	assert.Len(t, pairings, 1)
	assert.Equal(t, "user2", pairings[0].UserID)
}

func TestPairingStorage_ListPendingPairings_ExcludesExpired(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert pairings (in UTC)
	now := time.Now().UTC()
	insertPairing(t, db, "code1", "user1", now.Add(-2*time.Hour), now.Add(-1*time.Hour), true) // expired
	insertPairing(t, db, "code2", "user2", now, now.Add(1*time.Hour), true)                   // active

	// Should only list the non-expired one
	pairings, err := storage.ListPendingPairings()
	require.NoError(t, err)
	assert.Len(t, pairings, 1)
	assert.Equal(t, "user2", pairings[0].UserID)
}

func TestPairingStorage_CleanupExpiredPairings(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert an expired pairing (in UTC)
	now := time.Now().UTC()
	insertPairing(t, db, "expired-code", "user1", now.Add(-2*time.Hour), now.Add(-1*time.Hour), true)

	// Insert a valid pairing
	insertPairing(t, db, "valid-code", "user2", now, now.Add(1*time.Hour), true)

	// Cleanup
	deleted, err := storage.CleanupExpiredPairings()
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Verify only valid pairing remains
	_, err = storage.GetPairingByCode("valid-code")
	require.NoError(t, err)

	_, err = storage.GetPairingByCode("expired-code")
	assert.Error(t, err) // should be deleted
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "RFC3339 format",
			input:   "2024-01-15T10:30:00Z",
			wantErr: false,
		},
		{
			name:    "simple format",
			input:   "2024-01-15 10:30:00",
			wantErr: false,
		},
		{
			name:    "invalid format",
			input:   "not a timestamp",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTimestamp(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPairingStorage_FindByCodePrefix(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewPairingStorage(db)

	// Insert a pairing with known code prefix (in UTC)
	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	insertPairing(t, db, "abc123-def456", "user1", now, expiresAt, true)

	// Find by prefix
	results, err := storage.findPairingByCodePrefix("abc")
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "abc123-def456", results[0].Code)

	// No match
	results, err = storage.findPairingByCodePrefix("xyz")
	require.NoError(t, err)
	assert.Len(t, results, 0)
}

func TestPairingInfo_Fields(t *testing.T) {
	now := time.Now()
	info := PairingInfo{
		Code:      "test-code",
		UserID:    "user123",
		CreatedAt: now,
		ExpiresAt: now.Add(1 * time.Hour),
		IsActive:  true,
		Metadata:  map[string]string{"key": "value"},
	}

	assert.Equal(t, "test-code", info.Code)
	assert.Equal(t, "user123", info.UserID)
	assert.True(t, info.IsActive)
	assert.Equal(t, "value", info.Metadata["key"])
}
