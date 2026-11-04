//go:build unix

package gateway

import (
	"os"
	"syscall"
)

// lockAlertQueue takes the exclusive flock alertlock.py uses on the queue's
// lock file (bd-j2of) and returns the release func.
func lockAlertQueue(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
