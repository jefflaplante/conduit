//go:build unix

package gateway

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Enqueue waits for a writer holding the alertlock.py flock (bd-j2of).
func TestMQTTAlertQueue_WaitsForExternalFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- newMQTTEventQueuer(path).Enqueue(testNotice("high")) }()
	select {
	case err := <-done:
		t.Fatalf("Enqueue finished while the lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue did not finish after the lock was released")
	}
	if n := len(readPendingArray(t, path)); n != 1 {
		t.Fatalf("want 1 entry, got %d", n)
	}
}
