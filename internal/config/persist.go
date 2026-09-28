package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BackupSuffix is appended to a config file's name for the copy of its
// previous content kept by ReplaceFile (conduit-rmho).
const BackupSuffix = ".bak"

// ErrConfigChangedOnDisk is returned by ReplaceFile when the file no longer
// holds the content the caller based its edit on.
var ErrConfigChangedOnDisk = errors.New("config file changed on disk since it was read; re-run the update")

// ReplaceFile atomically replaces the file at path with data (conduit-rmho):
//
//   - symlinks are resolved, so the link target is replaced, not the link;
//   - when expected is non-nil the current content must equal it, else
//     ErrConfigChangedOnDisk (guards against a concurrent hand edit);
//   - the previous content is first saved to path+BackupSuffix (itself
//     written atomically);
//   - data goes to a temp file in the same directory with the original
//     file's permission bits (0600 when the file is new), is fsynced, then
//     renamed over the original, and the directory is fsynced.
//
// Readers therefore see either the old or the new file, never a partial
// one. Returns the backup path ("" when there was no previous file).
func ReplaceFile(path string, data, expected []byte) (backup string, err error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}

	mode := os.FileMode(0o600)
	old, err := os.ReadFile(target)
	switch {
	case err == nil:
		if st, serr := os.Stat(target); serr == nil {
			mode = st.Mode().Perm()
		}
	case os.IsNotExist(err):
		old = nil
	default:
		return "", fmt.Errorf("read %s: %w", target, err)
	}
	if expected != nil && !bytes.Equal(old, expected) {
		return "", ErrConfigChangedOnDisk
	}

	if old != nil {
		backup = target + BackupSuffix
		if err := writeAtomic(backup, old, mode); err != nil {
			return "", fmt.Errorf("write backup: %w", err)
		}
	}
	if err := writeAtomic(target, data, mode); err != nil {
		return backup, err
	}
	return backup, nil
}

// writeAtomic writes data to a temp file beside path and renames it over
// path.
func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
