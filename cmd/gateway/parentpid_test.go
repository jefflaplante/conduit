//go:build linux || darwin

package main

import (
	"os"
	"testing"
)

func TestParentPID(t *testing.T) {
	if got := parentPID(os.Getpid()); got != os.Getppid() {
		t.Fatalf("parentPID = %d, want %d", got, os.Getppid())
	}
	if got := parentPID(-1); got != 0 {
		t.Fatalf("parentPID(-1) = %d, want 0", got)
	}
}
