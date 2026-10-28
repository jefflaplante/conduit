//go:build !linux

package main

// gatewayAncestor is Linux-only (/proc); elsewhere only the environment
// marker is checked.
func gatewayAncestor() bool { return false }
