package auth

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"conduit/internal/database"
)

// setupTestDB creates a temporary database for testing
func setupTestDB(t *testing.T) *sql.DB {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	// Configure database and run migrations
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	return db
}

func TestNewTokenStorage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)
	if storage == nil {
		t.Error("Expected NewTokenStorage to return a non-nil storage")
	}

	if storage.db != db {
		t.Error("Expected storage to use the provided database")
	}
}

func TestCreateToken(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	tests := []struct {
		name      string
		req       CreateTokenRequest
		shouldErr bool
	}{
		{
			name: "valid token creation",
			req: CreateTokenRequest{
				ClientName: "test-client",
				Metadata: map[string]string{
					"description": "Test token",
					"environment": "test",
				},
			},
			shouldErr: false,
		},
		{
			name: "token with expiration",
			req: CreateTokenRequest{
				ClientName: "test-client-expiry",
				ExpiresAt:  timePtr(time.Now().Add(24 * time.Hour)),
			},
			shouldErr: false,
		},
		{
			name: "empty client name should fail",
			req: CreateTokenRequest{
				ClientName: "",
			},
			shouldErr: true,
		},
		{
			name: "whitespace-only client name should fail",
			req: CreateTokenRequest{
				ClientName: "   ",
			},
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := storage.CreateToken(tt.req)

			if tt.shouldErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if resp == nil {
				t.Fatal("Expected response but got nil")
			}

			if resp.Token == "" {
				t.Error("Expected non-empty token")
			}

			if resp.Token[:8] != "conduit_" {
				t.Errorf("Expected token to have 'conduit_' prefix, got '%s'", resp.Token[:8])
			}

			if resp.TokenInfo.TokenID == "" {
				t.Error("Expected non-empty token ID")
			}

			if resp.TokenInfo.ClientName != tt.req.ClientName {
				t.Errorf("Expected client name '%s', got '%s'",
					tt.req.ClientName, resp.TokenInfo.ClientName)
			}

			if !resp.TokenInfo.IsActive {
				t.Error("Expected newly created token to be active")
			}

			if resp.TokenInfo.CreatedAt.IsZero() {
				t.Error("Expected created_at to be set")
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a valid token
	validResp, err := storage.CreateToken(CreateTokenRequest{
		ClientName: "test-client",
		Metadata: map[string]string{
			"test": "value",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Create an expired token
	expiredResp, err := storage.CreateToken(CreateTokenRequest{
		ClientName: "expired-client",
		ExpiresAt:  timePtr(time.Now().Add(-1 * time.Hour)), // Already expired
	})
	if err != nil {
		t.Fatalf("Failed to create expired token: %v", err)
	}

	tests := []struct {
		name      string
		token     string
		shouldErr bool
	}{
		{
			name:      "valid token",
			token:     validResp.Token,
			shouldErr: false,
		},
		{
			name:      "expired token",
			token:     expiredResp.Token,
			shouldErr: true,
		},
		{
			name:      "invalid token",
			token:     "conduit_invalidtoken123",
			shouldErr: true,
		},
		{
			name:      "empty token",
			token:     "",
			shouldErr: true,
		},
		{
			name:      "malformed token",
			token:     "not-a-token",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenInfo, err := storage.ValidateToken(tt.token)

			if tt.shouldErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tokenInfo == nil {
				t.Fatal("Expected token info but got nil")
			}

			if tokenInfo.ClientName == "" {
				t.Error("Expected non-empty client name")
			}

			if !tokenInfo.IsActive {
				t.Error("Expected validated token to be active")
			}

			if tokenInfo.LastUsedAt == nil {
				t.Error("Expected last_used_at to be updated")
			}
		})
	}
}

func TestGetTokenInfo(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a test token
	resp, err := storage.CreateToken(CreateTokenRequest{
		ClientName: "test-client",
		Metadata: map[string]string{
			"environment": "test",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Test getting the token info
	tokenInfo, err := storage.GetTokenInfo(resp.TokenInfo.TokenID)
	if err != nil {
		t.Fatalf("Failed to get token info: %v", err)
	}

	if tokenInfo.TokenID != resp.TokenInfo.TokenID {
		t.Errorf("Expected token ID '%s', got '%s'",
			resp.TokenInfo.TokenID, tokenInfo.TokenID)
	}

	if tokenInfo.ClientName != "test-client" {
		t.Errorf("Expected client name 'test-client', got '%s'", tokenInfo.ClientName)
	}

	if tokenInfo.Metadata["environment"] != "test" {
		t.Error("Expected metadata to be preserved")
	}

	// Test getting non-existent token
	_, err = storage.GetTokenInfo("non-existent-id")
	if err == nil {
		t.Error("Expected error when getting non-existent token")
	}
}

func TestListTokens(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create test tokens
	_, err := storage.CreateToken(CreateTokenRequest{ClientName: "client1"})
	if err != nil {
		t.Fatalf("Failed to create token 1: %v", err)
	}

	_, err = storage.CreateToken(CreateTokenRequest{ClientName: "client1"})
	if err != nil {
		t.Fatalf("Failed to create token 2: %v", err)
	}

	resp3, err := storage.CreateToken(CreateTokenRequest{ClientName: "client2"})
	if err != nil {
		t.Fatalf("Failed to create token 3: %v", err)
	}

	// Revoke one token
	if err := storage.RevokeToken(resp3.TokenInfo.TokenID); err != nil {
		t.Fatalf("Failed to revoke token: %v", err)
	}

	tests := []struct {
		name            string
		clientName      string
		includeInactive bool
		expectedCount   int
	}{
		{
			name:            "list all active tokens",
			clientName:      "",
			includeInactive: false,
			expectedCount:   2, // 2 active tokens (1 revoked)
		},
		{
			name:            "list all tokens including inactive",
			clientName:      "",
			includeInactive: true,
			expectedCount:   3, // All tokens
		},
		{
			name:            "list client1 tokens only",
			clientName:      "client1",
			includeInactive: false,
			expectedCount:   2, // Both client1 tokens are active
		},
		{
			name:            "list client2 tokens only",
			clientName:      "client2",
			includeInactive: false,
			expectedCount:   0, // client2 token is revoked
		},
		{
			name:            "list client2 tokens including inactive",
			clientName:      "client2",
			includeInactive: true,
			expectedCount:   1, // client2 token exists but inactive
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := storage.ListTokens(tt.clientName, tt.includeInactive)
			if err != nil {
				t.Fatalf("Failed to list tokens: %v", err)
			}

			if len(tokens) != tt.expectedCount {
				t.Errorf("Expected %d tokens, got %d", tt.expectedCount, len(tokens))
			}

			for _, token := range tokens {
				if tt.clientName != "" && token.ClientName != tt.clientName {
					t.Errorf("Expected client name '%s', got '%s'",
						tt.clientName, token.ClientName)
				}
			}
		})
	}
}

func TestRevokeToken(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a test token
	resp, err := storage.CreateToken(CreateTokenRequest{ClientName: "test-client"})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Revoke the token
	if err := storage.RevokeToken(resp.TokenInfo.TokenID); err != nil {
		t.Fatalf("Failed to revoke token: %v", err)
	}

	// Verify token is revoked
	tokenInfo, err := storage.GetTokenInfo(resp.TokenInfo.TokenID)
	if err != nil {
		t.Fatalf("Failed to get token info: %v", err)
	}

	if tokenInfo.IsActive {
		t.Error("Expected revoked token to be inactive")
	}

	// Test revoking non-existent token
	if err := storage.RevokeToken("non-existent-id"); err == nil {
		t.Error("Expected error when revoking non-existent token")
	}
}

func TestDeleteToken(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a test token
	resp, err := storage.CreateToken(CreateTokenRequest{ClientName: "test-client"})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Delete the token
	if err := storage.DeleteToken(resp.TokenInfo.TokenID); err != nil {
		t.Fatalf("Failed to delete token: %v", err)
	}

	// Verify token is deleted
	_, err = storage.GetTokenInfo(resp.TokenInfo.TokenID)
	if err == nil {
		t.Error("Expected error when getting deleted token")
	}

	// Test deleting non-existent token
	if err := storage.DeleteToken("non-existent-id"); err == nil {
		t.Error("Expected error when deleting non-existent token")
	}
}

func TestUpdateTokenMetadata(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a test token
	resp, err := storage.CreateToken(CreateTokenRequest{
		ClientName: "test-client",
		Metadata: map[string]string{
			"original": "value",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Update metadata
	newMetadata := map[string]string{
		"updated":     "value",
		"environment": "production",
	}

	if err := storage.UpdateTokenMetadata(resp.TokenInfo.TokenID, newMetadata); err != nil {
		t.Fatalf("Failed to update token metadata: %v", err)
	}

	// Verify metadata was updated
	tokenInfo, err := storage.GetTokenInfo(resp.TokenInfo.TokenID)
	if err != nil {
		t.Fatalf("Failed to get token info: %v", err)
	}

	if tokenInfo.Metadata["updated"] != "value" {
		t.Error("Expected updated metadata to be present")
	}

	if tokenInfo.Metadata["environment"] != "production" {
		t.Error("Expected new metadata to be present")
	}

	if tokenInfo.Metadata["original"] != "" {
		t.Error("Expected original metadata to be replaced")
	}

	// Test updating non-existent token
	if err := storage.UpdateTokenMetadata("non-existent-id", newMetadata); err == nil {
		t.Error("Expected error when updating non-existent token")
	}
}

func TestCleanupExpiredTokens(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create tokens with different expiration times
	_, err := storage.CreateToken(CreateTokenRequest{
		ClientName: "never-expires",
		ExpiresAt:  nil, // Never expires
	})
	if err != nil {
		t.Fatalf("Failed to create never-expiring token: %v", err)
	}

	_, err = storage.CreateToken(CreateTokenRequest{
		ClientName: "future-expiry",
		ExpiresAt:  timePtr(time.Now().Add(24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("Failed to create future-expiring token: %v", err)
	}

	_, err = storage.CreateToken(CreateTokenRequest{
		ClientName: "already-expired",
		ExpiresAt:  timePtr(time.Now().Add(-1 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("Failed to create expired token: %v", err)
	}

	// Cleanup expired tokens
	deletedCount, err := storage.CleanupExpiredTokens()
	if err != nil {
		t.Fatalf("Failed to cleanup expired tokens: %v", err)
	}

	if deletedCount != 1 {
		t.Errorf("Expected 1 deleted token, got %d", deletedCount)
	}

	// Verify only expired token was deleted
	tokens, err := storage.ListTokens("", true)
	if err != nil {
		t.Fatalf("Failed to list tokens after cleanup: %v", err)
	}

	if len(tokens) != 2 {
		t.Errorf("Expected 2 remaining tokens, got %d", len(tokens))
	}

	for _, token := range tokens {
		if token.ClientName == "already-expired" {
			t.Error("Expired token should have been deleted")
		}
	}
}

// TestDatabaseConstraints tests that unique constraints work correctly
func TestDatabaseConstraints(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	storage := NewTokenStorage(db)

	// Create a token
	resp1, err := storage.CreateToken(CreateTokenRequest{ClientName: "test-client"})
	if err != nil {
		t.Fatalf("Failed to create first token: %v", err)
	}

	// Try to create another token with the same raw token value (this should be extremely rare
	// but let's verify the database constraint works by directly inserting a duplicate hash)

	// Get the hashed token from the database for testing
	var hashedToken string
	err = db.QueryRow("SELECT hashed_token FROM auth_tokens WHERE token_id = ?",
		resp1.TokenInfo.TokenID).Scan(&hashedToken)
	if err != nil {
		t.Fatalf("Failed to get hashed token: %v", err)
	}

	// Try to insert a duplicate hashed token directly (should fail due to UNIQUE constraint)
	_, err = db.Exec(`
		INSERT INTO auth_tokens (token_id, client_name, hashed_token, is_active, metadata)
		VALUES (?, ?, ?, ?, ?)
	`, "duplicate-id", "test-client", hashedToken, true, "{}")

	if err == nil {
		t.Error("Expected error due to unique constraint violation, but got none")
	}
}

// timePtr returns a pointer to a time.Time value
func timePtr(t time.Time) *time.Time {
	return &t
}
