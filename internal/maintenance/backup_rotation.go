package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"conduit/internal/config"
)

// backupTimestampLayout is the UTC timestamp BackupDatabase puts in backup
// file names.
const backupTimestampLayout = "20060102T150405.000Z"

// DefaultKeepBackups is how many <db>.backup.<timestamp> files rotation keeps
// when maintenance.keep_backups is not set (conduit-16f0).
const DefaultKeepBackups = config.DefaultKeepBackups

// BackupFile is a backup written by BackupDatabase.
type BackupFile struct {
	Path string    `json:"path"`
	Time time.Time `json:"time"` // parsed from the file name (UTC)
	Size int64     `json:"size_bytes"`
}

// backupDir returns the directory BackupDatabase writes dbPath's backups to.
func backupDir(dbPath, dir string) string {
	if dir == "" {
		return filepath.Dir(dbPath)
	}
	return dir
}

// parseBackupName reports whether name is exactly "<base>.backup.<ts>" as
// BackupDatabase names it, and returns the timestamp. The timestamp must
// round-trip through backupTimestampLayout, so hand-made copies such as
// "<base>.backup.old", "<base>.backup.<ts>.gz", "<base>.bak-1" or
// "<base>.pre-migrate" never match.
func parseBackupName(base, name string) (time.Time, bool) {
	prefix := base + ".backup."
	if !strings.HasPrefix(name, prefix) {
		return time.Time{}, false
	}
	ts := name[len(prefix):]
	t, err := time.Parse(backupTimestampLayout, ts)
	if err != nil || t.UTC().Format(backupTimestampLayout) != ts {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// ListBackups returns the backups BackupDatabase wrote for dbPath in dir (or
// next to dbPath when dir is empty), newest first. Only regular files whose
// name is exactly "<db name>.backup.<timestamp>" are returned; symlinks,
// directories and any other name are ignored. A missing directory is not an
// error.
func ListBackups(dbPath, dir string) ([]BackupFile, error) {
	if dbPath == "" {
		return nil, errors.New("backup: database path not available")
	}
	dir = backupDir(dbPath, dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: list %s: %w", dir, err)
	}
	base := filepath.Base(dbPath)
	var out []BackupFile
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		t, ok := parseBackupName(base, e.Name())
		if !ok {
			continue
		}
		b := BackupFile{Path: filepath.Join(dir, e.Name()), Time: t}
		if info, err := e.Info(); err == nil {
			b.Size = info.Size()
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].Path > out[j].Path
	})
	return out, nil
}

// BackupRotation is what rotating a database's backups keeps and removes.
type BackupRotation struct {
	Dir     string       `json:"dir"`
	Keep    int          `json:"keep"` // 0 = keep all
	Kept    []BackupFile `json:"kept"`
	Removed []BackupFile `json:"removed"` // planned, or deleted by RotateBackups
	Errors  []string     `json:"errors,omitempty"`
}

// PlanBackupRotation returns which existing backups of dbPath a rotation
// keeping the newest keep would remove once pending more backups have been
// written (a dry run passes 1 for the backup the run would write first).
// keep <= 0 keeps all. Nothing is changed.
func PlanBackupRotation(dbPath, dir string, keep, pending int) (*BackupRotation, error) {
	backups, err := ListBackups(dbPath, dir)
	if err != nil {
		return nil, err
	}
	rot := &BackupRotation{Dir: backupDir(dbPath, dir), Keep: max(keep, 0)}
	n := len(backups)
	if keep > 0 {
		n = min(max(keep-max(pending, 0), 0), n)
	}
	rot.Kept, rot.Removed = backups[:n], backups[n:]
	return rot, nil
}

// RotateBackups deletes all but the newest keep backups of dbPath in dir
// (conduit-16f0); keep <= 0 keeps all. current, the backup just written, is
// never deleted, even if a clock step makes an older name sort after it.
// Only files ListBackups matches are ever touched. A file that cannot be
// deleted is recorded in Errors and the rest are still processed.
func RotateBackups(dbPath, dir string, keep int, current string) (*BackupRotation, error) {
	rot, err := PlanBackupRotation(dbPath, dir, keep, 0)
	if err != nil {
		return nil, err
	}
	planned := rot.Removed
	rot.Removed = nil
	for _, b := range planned {
		if current != "" && filepath.Clean(b.Path) == filepath.Clean(current) {
			rot.Kept = append(rot.Kept, b)
			continue
		}
		if err := os.Remove(b.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rot.Errors = append(rot.Errors, err.Error())
			rot.Kept = append(rot.Kept, b)
			continue
		}
		rot.Removed = append(rot.Removed, b)
	}
	return rot, nil
}

// Summary describes the rotation in one line.
func (r *BackupRotation) Summary(dryRun bool) string {
	if r.Keep <= 0 {
		return fmt.Sprintf("backup rotation off (keep_backups 0): %d backup(s) kept in %s", len(r.Kept), r.Dir)
	}
	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	s := fmt.Sprintf("backup rotation (keep %d): %s %d old backup(s) in %s", r.Keep, verb, len(r.Removed), r.Dir)
	if len(r.Errors) > 0 {
		s += fmt.Sprintf("; %d could not be removed: %s", len(r.Errors), strings.Join(r.Errors, "; "))
	}
	return s
}

// BackupAndRotate writes a backup with BackupDatabase, then rotates the
// directory it went to, keeping the newest keep (<= 0 keeps all). A rotation
// failure does not fail the backup: rot is nil or carries Errors, and the
// caller reports it as a warning.
func BackupAndRotate(ctx context.Context, db *sql.DB, dbPath, dir string, keep int, now time.Time) (string, *BackupRotation, error) {
	path, err := BackupDatabase(ctx, db, dbPath, dir, now)
	if err != nil {
		return "", nil, err
	}
	rot, err := RotateBackups(dbPath, dir, keep, path)
	if err != nil {
		return path, &BackupRotation{Dir: backupDir(dbPath, dir), Keep: max(keep, 0), Errors: []string{err.Error()}}, nil
	}
	return path, rot, nil
}
