//go:build linux

package main

import (
	"os"
	"testing"
)

func TestParentPID(t *testing.T) {
	if got := parentPID(os.Getpid()); got != os.Getppid() {
		t.Fatalf("parentPID = %d, want %d", got, os.Getppid())
	}
	// The test binary is run by `go test`, not by a gateway binary.
	if gatewayAncestor() {
		t.Fatal("gatewayAncestor true under go test")
	}
}
