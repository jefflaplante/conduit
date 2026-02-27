package telegram

import (
	"database/sql"
	"testing"
	"time"

	"conduit/internal/database"

	_ "modernc.org/sqlite"
)

// Example test demonstrating the pairing workflow
func TestPairingWorkflow(t *testing.T) {
	// Create in-memory database for testing
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Run migrations to set up the schema
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	// Create pairing manager
	pairingMgr := NewPairingManager(db)

	// Test user ID
	userID := "12345678"

	// Step 1: Check if user is paired (should be false initially)
	isPaired, err := pairingMgr.IsUserPaired(userID)
	if err != nil {
		t.Fatalf("Failed to check pairing status: %v", err)
	}
	if isPaired {
		t.Fatal("User should not be paired initially")
	}

	// Step 2: Generate pairing code
	code, err := pairingMgr.GeneratePairingCode(userID)
	if err != nil {
		t.Fatalf("Failed to generate pairing code: %v", err)
	}
	if code == "" {
		t.Fatal("Generated code should not be empty")
	}
	t.Logf("Generated pairing code: %s", code)

	// Step 3: Validate the pairing code
	record, err := pairingMgr.ValidatePairingCode(code)
	if err != nil {
		t.Fatalf("Failed to validate pairing code: %v", err)
	}
	if record.UserID != userID {
		t.Fatalf("Record user ID mismatch: expected %s, got %s", userID, record.UserID)
	}
	if !record.IsActive {
		t.Fatal("Record should be active")
	}

	// Step 4: User should still not be paired (code is active but not approved)
	isPaired, err = pairingMgr.IsUserPaired(userID)
	if err != nil {
		t.Fatalf("Failed to check pairing status: %v", err)
	}
	if isPaired {
		t.Fatal("User should not be paired until code is approved")
	}

	// Step 5: Approve the pairing
	err = pairingMgr.ApprovePairing(code)
	if err != nil {
		t.Fatalf("Failed to approve pairing: %v", err)
	}

	// Step 6: Now user should be paired
	isPaired, err = pairingMgr.IsUserPaired(userID)
	if err != nil {
		t.Fatalf("Failed to check pairing status: %v", err)
	}
	if !isPaired {
		t.Fatal("User should be paired after approval")
	}

	// Step 7: Code should no longer be valid
	_, err = pairingMgr.ValidatePairingCode(code)
	if err == nil {
		t.Fatal("Code should no longer be valid after approval")
	}

	// Step 8: Test statistics
	stats, err := pairingMgr.GetPairingStats()
	if err != nil {
		t.Fatalf("Failed to get pairing stats: %v", err)
	}

	if activeCodes, ok := stats["active_codes"].(int); !ok || activeCodes != 0 {
		t.Fatalf("Expected 0 active codes, got %v", stats["active_codes"])
	}

	if approvedPairings, ok := stats["approved_pairings"].(int); !ok || approvedPairings != 1 {
		t.Fatalf("Expected 1 approved pairing, got %v", stats["approved_pairings"])
	}

	t.Log("Pairing workflow test completed successfully")
}

// Example test demonstrating code expiration
func TestPairingCodeExpiration(t *testing.T) {
	// Create in-memory database for testing
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	pairingMgr := NewPairingManager(db)
	userID := "87654321"

	// Generate a pairing code
	code, err := pairingMgr.GeneratePairingCode(userID)
	if err != nil {
		t.Fatalf("Failed to generate pairing code: %v", err)
	}

	// Manually expire the code by updating the database
	_, err = db.Exec(`
		UPDATE telegram_pairings 
		SET expires_at = ? 
		WHERE code = ?
	`, time.Now().Add(-1*time.Hour), code)
	if err != nil {
		t.Fatalf("Failed to manually expire code: %v", err)
	}

	// Try to validate the expired code
	_, err = pairingMgr.ValidatePairingCode(code)
	if err == nil {
		t.Fatal("Expired code should not validate")
	}

	// Cleanup expired codes
	err = pairingMgr.CleanupExpiredCodes()
	if err != nil {
		t.Fatalf("Failed to cleanup expired codes: %v", err)
	}

	t.Log("Code expiration test completed successfully")
}

// Example test demonstrating multiple codes per user
func TestMultiplePairingCodes(t *testing.T) {
	// Create in-memory database for testing
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	pairingMgr := NewPairingManager(db)
	userID := "11111111"

	// Generate first pairing code
	code1, err := pairingMgr.GeneratePairingCode(userID)
	if err != nil {
		t.Fatalf("Failed to generate first pairing code: %v", err)
	}

	// Generate second pairing code (should deactivate the first)
	code2, err := pairingMgr.GeneratePairingCode(userID)
	if err != nil {
		t.Fatalf("Failed to generate second pairing code: %v", err)
	}

	// First code should no longer be valid
	_, err = pairingMgr.ValidatePairingCode(code1)
	if err == nil {
		t.Fatal("First code should be invalidated when second code is generated")
	}

	// Second code should be valid
	_, err = pairingMgr.ValidatePairingCode(code2)
	if err != nil {
		t.Fatalf("Second code should be valid: %v", err)
	}

	t.Log("Multiple pairing codes test completed successfully")
}

// Example showing how to use the pairing system in practice
func ExamplePairingManager() {
	// This example shows typical usage patterns
	// Note: This is example code, not a real test

	// Initialize database and pairing manager
	db, _ := sql.Open("sqlite", "test.db")
	database.ConfigureDatabase(db)
	pairingMgr := NewPairingManager(db)

	userID := "123456789"

	// When a new user sends a message:
	isPaired, _ := pairingMgr.IsUserPaired(userID)
	if !isPaired {
		// Generate and send pairing code
		code, _ := pairingMgr.GeneratePairingCode(userID)
		// SendPairingCode(bot, chatID, code) would be called here
		_ = code
		return // Don't process the message
	}

	// When an admin approves a pairing:
	code := "some-uuid-code"
	err := pairingMgr.ApprovePairing(code)
	if err != nil {
		// Handle approval error
		return
	}
	// SendApprovalNotification(bot, chatID) would be called here

	// Periodic cleanup (could be run as a scheduled task):
	pairingMgr.CleanupExpiredCodes()

	// Monitoring:
	stats, _ := pairingMgr.GetPairingStats()
	_ = stats // Contains active_codes, expired_codes, approved_pairings, paired_users
}
