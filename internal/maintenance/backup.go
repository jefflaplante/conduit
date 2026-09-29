package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupDatabase writes a consistent copy of the database with
// VACUUM INTO, to dir (or next to dbPath when dir is empty) as
// "<name>.backup.<UTC timestamp>". The file is created 0600 before SQLite
// writes into it, so the copy of private conversations is never
// world-readable. VACUUM INTO reads through the WAL, so it is safe while the
// gateway is running (unlike copying the main file). conduit-2cxu
func BackupDatabase(ctx context.Context, db *sql.DB, dbPath, dir string, now time.Time) (string, error) {
	if dbPath == "" {
		return "", errors.New("backup: database path not available")
	}
	if dir == "" {
		dir = filepath.Dir(dbPath)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s.backup.%s", filepath.Base(dbPath), now.UTC().Format("20060102T150405.000Z")))

	// VACUUM INTO accepts an existing empty file and keeps its mode.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("backup: %w", err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("backup: VACUUM INTO %s: %w", path, err)
	}
	// Belt and braces in case SQLite recreated the file.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	return path, nil
}
