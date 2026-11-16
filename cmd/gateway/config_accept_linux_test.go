//go:build linux

package main

import "testing"

func TestGatewayAncestor_FalseUnderGoTest(t *testing.T) {
	// The test binary is run by `go test`, not by a gateway binary.
	if gatewayAncestor() {
		t.Fatal("gatewayAncestor true under go test")
	}
}
