package database

import (
	"strings"
	"time"
)

// IsBusyError returns true if the error is a SQLite BUSY / "database is locked" error.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "database table is locked")
}

// maxBackoff caps the exponential backoff to prevent excessively long waits
// on individual retries while still allowing cumulative retry time to grow.
const maxBackoff = 500 * time.Millisecond

// RetryOnBusy retries fn with exponential backoff when it returns a SQLite BUSY error.
// Backoff sequence: 50ms, 100ms, 200ms, 400ms, 500ms (capped), ...
func RetryOnBusy(maxRetries int, fn func() error) error {
	var err error
	backoff := 50 * time.Millisecond

	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = fn()
		if err == nil || !IsBusyError(err) {
			return err
		}
		if attempt < maxRetries {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}

	return err
}
