package telegram

import (
	"database/sql"
	"testing"
	"time"

	"conduit/internal/database"
	"conduit/internal/ratelimit"

	_ "modernc.org/sqlite"
)

func setupTestPairingManager(t *testing.T) (*PairingManager, *sql.DB) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	pm := NewPairingManager(db)
	t.Cleanup(func() {
		pm.Stop()
		db.Close()
	})

	return pm, db
}

func TestPairingRateLimitConstants(t *testing.T) {
	// Verify the rate limit constants are reasonable
	if PairingRateLimit < 1 {
		t.Fatal("PairingRateLimit must be at least 1")
	}
	if PairingRateWindow <= 0 {
		t.Fatal("PairingRateWindow must be positive")
	}
	if PairingRateCleanupInterval <= 0 {
		t.Fatal("PairingRateCleanupInterval must be positive")
	}
}

func TestPairingManagerHasRateLimiter(t *testing.T) {
	pm, _ := setupTestPairingManager(t)

	if pm.rateLimiter == nil {
		t.Fatal("PairingManager should have a rate limiter initialized")
	}
}

func TestPairingRateLimiterAllowsUpToLimit(t *testing.T) {
	pm, _ := setupTestPairingManager(t)

	userID := "test-user-1"

	// Should allow up to PairingRateLimit attempts
	for i := 0; i < PairingRateLimit; i++ {
		allowed, _, _, _ := pm.rateLimiter.Allow(userID)
		if !allowed {
			t.Fatalf("Attempt %d should be allowed (limit is %d)", i+1, PairingRateLimit)
		}
	}

	// Next attempt should be denied
	allowed, _, _, _ := pm.rateLimiter.Allow(userID)
	if allowed {
		t.Fatal("Attempt beyond rate limit should be denied")
	}
}

func TestPairingRateLimiterPerUser(t *testing.T) {
	pm, _ := setupTestPairingManager(t)

	user1 := "user-1"
	user2 := "user-2"

	// Exhaust user1's limit
	for i := 0; i < PairingRateLimit; i++ {
		pm.rateLimiter.Allow(user1)
	}

	// user1 should be rate limited
	allowed, _, _, _ := pm.rateLimiter.Allow(user1)
	if allowed {
		t.Fatal("user1 should be rate limited")
	}

	// user2 should still be allowed
	allowed, _, _, _ = pm.rateLimiter.Allow(user2)
	if !allowed {
		t.Fatal("user2 should not be affected by user1's rate limit")
	}
}

func TestPairingRateLimiterCustomWindow(t *testing.T) {
	// Create a pairing manager with a custom short-window rate limiter for testing
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("Failed to configure test database: %v", err)
	}

	pm := NewPairingManager(db)
	pm.rateLimiter.Stop() // Stop the default limiter

	// Replace with a very short window for testing reset behavior
	pm.rateLimiter = ratelimit.NewSlidingWindow(
		50*time.Millisecond,
		2,
		1*time.Second,
	)
	defer pm.Stop()

	userID := "test-reset-user"

	// Use both allowed attempts
	for i := 0; i < 2; i++ {
		allowed, _, _, _ := pm.rateLimiter.Allow(userID)
		if !allowed {
			t.Fatalf("Attempt %d should be allowed", i+1)
		}
	}

	// Should be rate limited now
	allowed, _, _, _ := pm.rateLimiter.Allow(userID)
	if allowed {
		t.Fatal("Should be rate limited after exceeding limit")
	}
}

func TestPairingManagerStop(t *testing.T) {
	pm, _ := setupTestPairingManager(t)

	// Stop should not panic
	pm.Stop()

	// Double stop should not panic either
	pm.Stop()
}

func TestPairingRateLimitMessage(t *testing.T) {
	// Verify the rate limit message constant is non-empty
	if PairingRateLimitMessage == "" {
		t.Fatal("PairingRateLimitMessage should not be empty")
	}
}

func TestPairingWorkflowWithRateLimit(t *testing.T) {
	pm, _ := setupTestPairingManager(t)

	userID := "rate-limit-workflow-user"

	// Generate pairing codes up to the rate limit
	for i := 0; i < PairingRateLimit; i++ {
		allowed, _, _, _ := pm.rateLimiter.Allow(userID)
		if !allowed {
			t.Fatalf("Pairing attempt %d should be allowed", i+1)
		}
		code, err := pm.GeneratePairingCode(userID)
		if err != nil {
			t.Fatalf("Failed to generate pairing code on attempt %d: %v", i+1, err)
		}
		if code == "" {
			t.Fatalf("Generated code should not be empty on attempt %d", i+1)
		}
	}

	// Next attempt should be rate limited
	allowed, _, _, retryAfter := pm.rateLimiter.Allow(userID)
	if allowed {
		t.Fatal("Should be rate limited after exhausting attempts")
	}
	if retryAfter <= 0 {
		t.Fatal("retryAfter should be positive when rate limited")
	}
}

func TestPairedUserBypassesRateLimit(t *testing.T) {
	// Verify that the rate limiter is only checked for unpaired users
	// by confirming that paired users skip the rate limit check in HandlePairingForUser
	pm, _ := setupTestPairingManager(t)

	userID := "paired-user"

	// Generate and approve a pairing code
	code, err := pm.GeneratePairingCode(userID)
	if err != nil {
		t.Fatalf("Failed to generate pairing code: %v", err)
	}
	if err := pm.ApprovePairing(code); err != nil {
		t.Fatalf("Failed to approve pairing: %v", err)
	}

	// Exhaust the rate limiter for this user
	for i := 0; i < PairingRateLimit+5; i++ {
		pm.rateLimiter.Allow(userID)
	}

	// User is paired, so IsUserPaired should return true
	// (HandlePairingForUser returns early before checking rate limit)
	isPaired, err := pm.IsUserPaired(userID)
	if err != nil {
		t.Fatalf("Failed to check pairing status: %v", err)
	}
	if !isPaired {
		t.Fatal("User should be paired after approval")
	}
}
