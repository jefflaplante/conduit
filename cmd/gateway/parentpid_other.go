//go:build !linux && !darwin

package main

// parentPID has no implementation on this platform; callers treat 0 as
// "unknown parent".
func parentPID(pid int) int { return 0 }
