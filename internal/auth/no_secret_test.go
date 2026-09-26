package auth

import (
	"errors"
	"testing"
)

// conduit-31jg.48: an empty HMAC secret must be an error, never a silently
// generated ephemeral key.
func TestOpenTokenStorage_EmptySecretIsError(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	if _, err := OpenTokenStorage(db, ""); !errors.Is(err, ErrNoTokenSecret) {
		t.Fatalf("OpenTokenStorage(\"\") err = %v, want ErrNoTokenSecret", err)
	}
	if _, err := OpenTokenStorage(db, "   "); !errors.Is(err, ErrNoTokenSecret) {
		t.Fatalf("whitespace secret err = %v, want ErrNoTokenSecret", err)
	}
	ts, err := OpenTokenStorage(db, "real-secret")
	if err != nil || ts == nil {
		t.Fatalf("OpenTokenStorage(real) = %v, %v", ts, err)
	}
}

func TestNewTokenStorage_EmptySecretFailsClosed(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	ts := NewTokenStorage(db, "")
	if _, err := ts.CreateToken(CreateTokenRequest{ClientName: "c"}); !errors.Is(err, ErrNoTokenSecret) {
		t.Fatalf("CreateToken err = %v, want ErrNoTokenSecret", err)
	}
	if _, err := ts.ValidateToken("conduit_whatever"); !errors.Is(err, ErrNoTokenSecret) {
		t.Fatalf("ValidateToken err = %v, want ErrNoTokenSecret", err)
	}
}
