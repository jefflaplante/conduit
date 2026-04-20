package main

import (
	"os"
	"testing"
)

func TestIsUnderSystemd(t *testing.T) {
	orig, had := os.LookupEnv("INVOCATION_ID")
	t.Cleanup(func() {
		if had {
			os.Setenv("INVOCATION_ID", orig)
		} else {
			os.Unsetenv("INVOCATION_ID")
		}
	})

	tests := []struct {
		name         string
		invocationID string
		unsetVar     bool
		// ppidIs1 can't be reliably faked in-test, so we only assert
		// the INVOCATION_ID branch deterministically.
		wantWhenPPIDNot1 bool
	}{
		{name: "set", invocationID: "abc123", wantWhenPPIDNot1: true},
		{name: "empty", invocationID: "", unsetVar: false, wantWhenPPIDNot1: false},
		{name: "unset", unsetVar: true, wantWhenPPIDNot1: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unsetVar {
				os.Unsetenv("INVOCATION_ID")
			} else {
				os.Setenv("INVOCATION_ID", tt.invocationID)
			}

			got := isUnderSystemd()
			// If PPID happens to be 1 (unlikely in tests), isUnderSystemd
			// will return true regardless. Only assert the negative case
			// when PPID != 1.
			if os.Getppid() != 1 {
				if got != tt.wantWhenPPIDNot1 {
					t.Errorf("isUnderSystemd() = %v, want %v (INVOCATION_ID=%q, PPID=%d)",
						got, tt.wantWhenPPIDNot1, os.Getenv("INVOCATION_ID"), os.Getppid())
				}
			}
		})
	}
}

func TestIsAncestorPID(t *testing.T) {
	// Our own PID is trivially an ancestor of ourselves.
	if !isAncestorPID(os.Getpid()) {
		t.Errorf("isAncestorPID(self) = false, want true")
	}

	// Our parent is an ancestor.
	if ppid := os.Getppid(); ppid > 1 {
		if !isAncestorPID(ppid) {
			t.Errorf("isAncestorPID(parent=%d) = false, want true", ppid)
		}
	}

	// A PID that cannot be an ancestor (0, 1 when we're not init, and a
	// clearly-out-of-tree PID) must return false.
	if isAncestorPID(0) {
		t.Errorf("isAncestorPID(0) = true, want false")
	}

	// PID 1 is an ancestor only when we're a direct/indirect child of init;
	// that's unusual in the `go test` runner (which is itself launched by
	// the shell/test harness), so don't assert either way.

	// A very large PID that almost certainly doesn't exist should return false.
	if isAncestorPID(1<<22 - 1) {
		t.Errorf("isAncestorPID(huge) = true, want false")
	}
}
