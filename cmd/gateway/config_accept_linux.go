//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// gatewayAncestor reports whether a parent process (up to init) runs the
// same executable as this one: the gateway, which started the agent's shell.
func gatewayAncestor() bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	self, _ = filepath.EvalSymlinks(self)
	selfInfo, _ := os.Stat(self)
	pid := os.Getppid()
	for i := 0; i < 64 && pid > 1; i++ {
		exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		if err == nil {
			// A rebuilt binary shows as "<path> (deleted)" for the old process.
			if strings.TrimSuffix(exe, " (deleted)") == self {
				return true
			}
			if fi, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil && selfInfo != nil && os.SameFile(fi, selfInfo) {
				return true
			}
		}
		pid = parentPID(pid)
	}
	return false
}

func parentPID(pid int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// pid (comm) state ppid ...; comm may contain spaces and parentheses.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}
