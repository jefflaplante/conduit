//go:build !unix

package gateway

import "os"

// lockAlertQueue only creates the lock file: there is no flock off unix, and
// the alertlock.py tooling that shares the queue is unix-only (bd-j2of).
func lockAlertQueue(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return func() { f.Close() }, nil
}
