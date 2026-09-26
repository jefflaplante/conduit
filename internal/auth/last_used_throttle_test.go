package auth

import (
	"testing"
	"time"
)

// conduit-31jg.4: last_used_at is written at most once per minute per token.
func TestValidateToken_LastUsedThrottled(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	ts := NewTokenStorage(db, "test-secret-key-for-hmac-hashing")

	resp, err := ts.CreateToken(CreateTokenRequest{ClientName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	id := resp.TokenInfo.TokenID

	lastUsedIsNull := func() bool {
		var n int
		if err := db.QueryRow(`SELECT last_used_at IS NULL FROM auth_tokens WHERE token_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	if _, err := ts.ValidateToken(resp.Token); err != nil {
		t.Fatal(err)
	}
	if lastUsedIsNull() {
		t.Fatal("first validation should record last_used_at")
	}

	// Clear it; a second validation within the interval must not write.
	if _, err := db.Exec(`UPDATE auth_tokens SET last_used_at = NULL WHERE token_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.ValidateToken(resp.Token); err != nil {
		t.Fatal(err)
	}
	if !lastUsedIsNull() {
		t.Fatal("second validation within interval wrote last_used_at")
	}

	// Once the interval has elapsed, it writes again.
	ts.lastUsedMu.Lock()
	ts.lastUsedWritten[id] = time.Now().Add(-2 * lastUsedInterval)
	ts.lastUsedMu.Unlock()
	if _, err := ts.ValidateToken(resp.Token); err != nil {
		t.Fatal(err)
	}
	if lastUsedIsNull() {
		t.Fatal("validation after interval should record last_used_at")
	}
}
