//go:build darwin

package main

import "golang.org/x/sys/unix"

// parentPID returns the parent PID of pid via sysctl kern.proc.pid, or 0.
func parentPID(pid int) int {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0
	}
	return int(kp.Eproc.Ppid)
}
