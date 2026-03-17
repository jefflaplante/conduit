package database

import (
	"errors"
	"testing"
)

func TestIsBusyError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("some other error"), false},
		{errors.New("database is locked"), true},
		{errors.New("SQLITE_BUSY (5)"), true},
		{errors.New("database table is locked"), true},
		{errors.New("failed to exec: database is locked"), true},
	}

	for _, tt := range tests {
		got := IsBusyError(tt.err)
		if got != tt.want {
			t.Errorf("IsBusyError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestRetryOnBusy(t *testing.T) {
	// Fails twice with busy, then succeeds
	attempts := 0
	err := RetryOnBusy(3, func() error {
		attempts++
		if attempts <= 2 {
			return errors.New("database is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryOnBusy_NonBusyError(t *testing.T) {
	// Non-busy error should not retry
	attempts := 0
	err := RetryOnBusy(3, func() error {
		attempts++
		return errors.New("syntax error")
	})
	if err == nil || err.Error() != "syntax error" {
		t.Fatalf("expected syntax error, got: %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt for non-busy error, got %d", attempts)
	}
}

func TestRetryOnBusy_ExhaustsRetries(t *testing.T) {
	attempts := 0
	err := RetryOnBusy(2, func() error {
		attempts++
		return errors.New("database is locked")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != 3 { // initial + 2 retries
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}
