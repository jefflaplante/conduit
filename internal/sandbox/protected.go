package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Protected files (the gateway's config.json and its approved snapshot)
// can only be changed through update_config, which shows the owner the
// diff and needs a "YES <code>". Write, Edit and Bash refuse them whatever
// the sandbox allows, including through a symlink, a relative path or a
// hard link.

// ErrProtected is returned for a write to a protected file.
var ErrProtected = errors.New("path is protected")

var protected struct {
	mu    sync.RWMutex
	paths []string // canonical
}

// Protect adds path to the protected set. Relative paths are made absolute
// against the process working directory.
func Protect(path string) {
	if path == "" {
		return
	}
	real, err := Canonicalize(path)
	if err != nil {
		if real, err = filepath.Abs(path); err != nil {
			return
		}
	}
	protected.mu.Lock()
	defer protected.mu.Unlock()
	for _, p := range protected.paths {
		if p == real {
			return
		}
	}
	protected.paths = append(protected.paths, real)
}

// ProtectedPaths returns the protected files (canonical paths).
func ProtectedPaths() []string {
	protected.mu.RLock()
	defer protected.mu.RUnlock()
	return append([]string(nil), protected.paths...)
}

// ResetProtected clears the protected set. Tests only.
func ResetProtected() {
	protected.mu.Lock()
	protected.paths = nil
	protected.mu.Unlock()
}

// CheckWrite returns an ErrProtected error if path names a protected file:
// the same canonical path (symlinks resolved) or the same file on disk (a
// hard link). A path that cannot be canonicalized is refused when its
// lexical form names a protected file.
func CheckWrite(path string) error {
	for _, p := range ProtectedPaths() {
		if IsProtectedFile(path, p) {
			return fmt.Errorf("%w: %s can only be changed through the Gateway tool's update_config, which needs the owner's approval", ErrProtected, path)
		}
	}
	return nil
}

// IsProtectedFile reports whether path refers to the file protectedPath.
func IsProtectedFile(path, protectedPath string) bool {
	real, err := Canonicalize(path)
	if err != nil {
		abs, aerr := filepath.Abs(path)
		return aerr == nil && abs == protectedPath
	}
	if real == protectedPath {
		return true
	}
	a, err := os.Stat(real)
	if err != nil {
		return false
	}
	b, err := os.Stat(protectedPath)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}
