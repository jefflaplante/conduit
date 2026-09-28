package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/config"

	_ "modernc.org/sqlite"
)

// CreateBackup produces a .tar.gz archive containing the gateway's data.
func CreateBackup(ctx context.Context, opts BackupOptions) (*BackupResult, error) {
	start := time.Now()

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	configDir, err := filepath.Abs(filepath.Dir(opts.ConfigPath))
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}

	// Resolve database path relative to config directory.
	dbPath := cfg.Database.Path
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(configDir, dbPath)
	}

	// Resolve workspace dir.
	wsDir := cfg.Workspace.ContextDir
	if wsDir == "" {
		wsDir = "./workspace"
	}
	if !filepath.IsAbs(wsDir) {
		wsDir = filepath.Join(configDir, wsDir)
	}

	// Build components bitmask.
	components := ComponentDatabase | ComponentConfig | ComponentWorkspace
	if opts.IncludeSSHKeys {
		components |= ComponentSSHKeys
	}
	if opts.IncludeSkills {
		components |= ComponentSkills
	}

	// Snapshot database to temp file.
	tmpDir, err := os.MkdirTemp("", "conduit-backup-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dbSnapshotPath := filepath.Join(tmpDir, "gateway.db")
	dbInfo, err := snapshotDatabase(ctx, dbPath, dbSnapshotPath)
	if err != nil {
		return nil, fmt.Errorf("snapshot database: %w", err)
	}

	// Snapshot brain database if it exists. conduit-31jg.9: honour
	// brain.path (as the gateway does) instead of only the path derived from
	// database.path; relative paths resolve against the config directory like
	// database.path above.
	brainDBPath := cfg.Brain.Path
	if brainDBPath == "" {
		brainDBPath = config.DeriveBrainDBPath(dbPath)
	} else if !filepath.IsAbs(brainDBPath) {
		brainDBPath = filepath.Join(configDir, brainDBPath)
	}
	var brainSnapshotPath string
	if _, err := os.Stat(brainDBPath); err == nil {
		brainSnapshotPath = filepath.Join(tmpDir, "brain.db")
		if _, snErr := snapshotDatabase(ctx, brainDBPath, brainSnapshotPath); snErr != nil {
			log.Printf("WARNING: failed to snapshot brain database: %v", snErr)
		} else {
			components |= ComponentBrainDB
		}
	}

	absConfigPath, err := filepath.Abs(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	paths := OriginalPaths{
		Config:       absConfigPath,
		Database:     dbPath,
		WorkspaceDir: wsDir,
	}
	if brainSnapshotPath != "" {
		paths.BrainDatabase = brainDBPath
	}

	if opts.IncludeSSHKeys {
		paths.SSHHostKey = cfg.SSH.HostKeyPath
		paths.SSHAuthKeys = cfg.SSH.AuthorizedKeysPath
	}
	if opts.IncludeSkills && cfg.Skills.Enabled {
		paths.SkillsPaths = cfg.Skills.SearchPaths
	}

	manifest := NewManifest(components, paths, dbInfo)

	// Determine output path.
	outPath := opts.OutputPath
	if outPath == "" {
		outPath = fmt.Sprintf("conduit-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
	}

	outPath, err = filepath.Abs(outPath)
	if err != nil {
		return nil, fmt.Errorf("resolve output path: %w", err)
	}

	result := &BackupResult{
		ArchivePath: outPath,
		Components:  components,
	}

	// Create archive with restrictive permissions (owner read/write only).
	// Backup archives contain sensitive data (database, config, SSH keys).
	outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	// 1. Manifest
	manifestData, err := MarshalManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := writeTarBytes(tw, "manifest.json", manifestData); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	result.FileCount++

	// 2. Database snapshot.
	if err := writeTarFile(tw, "database/gateway.db", dbSnapshotPath); err != nil {
		return nil, fmt.Errorf("write database: %w", err)
	}
	result.FileCount++

	if brainSnapshotPath != "" {
		if err := writeTarFile(tw, "database/brain.db", brainSnapshotPath); err != nil {
			return nil, fmt.Errorf("archive brain database: %w", err)
		}
		result.FileCount++
	}

	// 3. Config file.
	configFilename := filepath.Base(opts.ConfigPath)
	if err := writeTarFile(tw, "config/"+configFilename, absConfigPath); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	result.FileCount++

	// 4. Workspace directory.
	if stat, err := os.Stat(wsDir); err == nil && stat.IsDir() {
		n, err := writeTarDir(tw, "workspace", wsDir)
		if err != nil {
			return nil, fmt.Errorf("write workspace: %w", err)
		}
		result.FileCount += n
	} else {
		result.Warnings = append(result.Warnings, fmt.Sprintf("workspace dir not found: %s", wsDir))
	}

	// 5. SSH keys (optional).
	if opts.IncludeSSHKeys {
		sshCount, sshWarnings := writeSSHKeys(tw, cfg)
		result.FileCount += sshCount
		result.Warnings = append(result.Warnings, sshWarnings...)
	}

	// 6. Skills (optional).
	if opts.IncludeSkills && cfg.Skills.Enabled {
		for _, sp := range cfg.Skills.SearchPaths {
			absPath := sp
			if !filepath.IsAbs(absPath) {
				absPath = filepath.Join(configDir, absPath)
			}
			if stat, err := os.Stat(absPath); err == nil && stat.IsDir() {
				dirName := filepath.Base(absPath)
				n, err := writeTarDir(tw, "skills/"+dirName, absPath)
				if err != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("failed to backup skills dir %s: %v", absPath, err))
					continue
				}
				result.FileCount += n
			} else {
				result.Warnings = append(result.Warnings, fmt.Sprintf("skills dir not found: %s", absPath))
			}
		}
	}

	// Close writers to flush and get final size.
	tw.Close()
	gw.Close()
	outFile.Close()

	if stat, err := os.Stat(outPath); err == nil {
		result.TotalSize = stat.Size()
	}
	result.Duration = time.Since(start)

	return result, nil
}

// snapshotDatabase creates a clean, self-contained snapshot via VACUUM INTO.
//
// conduit-31jg.9: VACUUM INTO reads a consistent snapshot through SQLite, so
// it includes committed pages still in the -wal file. The previous code
// opened "path?mode=ro" — modernc ignores query parameters on non-URI names,
// so that was a read-write open — and on any VACUUM failure fell back to a
// raw copy of the main file, which silently dropped everything not yet
// checkpointed from the WAL. There is no raw-copy fallback any more: a
// failure is returned to the caller.
func snapshotDatabase(ctx context.Context, srcPath, dstPath string) (DatabaseInfo, error) {
	info := DatabaseInfo{}

	stat, err := os.Stat(srcPath)
	if err != nil {
		return info, fmt.Errorf("stat database: %w", err)
	}
	info.Size = stat.Size()

	// Real read-only URI open (file: form so mode=ro is honoured) with a busy
	// timeout so a concurrently-writing gateway does not fail the backup.
	dsn := "file:" + srcPath + "?mode=ro&_pragma=busy_timeout%3D5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return info, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Count tables.
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count); err == nil {
		info.TableCount = count
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(dstPath, "'", "''"))); err != nil {
		return info, fmt.Errorf("VACUUM INTO snapshot: %w", err)
	}
	// Restrict permissions on the snapshot file created by SQLite.
	if err := os.Chmod(dstPath, 0600); err != nil {
		return info, fmt.Errorf("restrict snapshot permissions: %w", err)
	}
	return info, nil
}

// writeTarBytes writes in-memory data as a tar entry.
func writeTarBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// writeTarFile adds a file from disk to the tar archive.
func writeTarFile(tw *tar.Writer, archivePath, diskPath string) error {
	fi, err := os.Stat(diskPath)
	if err != nil {
		return err
	}

	hdr := &tar.Header{
		Name:    archivePath,
		Mode:    int64(fi.Mode().Perm()),
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}

	f, err := os.Open(diskPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(tw, f)
	return err
}

// writeTarDir recursively adds a directory to the tar archive.
// Returns the number of files written.
func writeTarDir(tw *tar.Writer, prefix, root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		archivePath := prefix + "/" + filepath.ToSlash(rel)

		if err := writeTarFile(tw, archivePath, path); err != nil {
			return fmt.Errorf("write %s: %w", archivePath, err)
		}
		count++
		return nil
	})
	return count, err
}

// writeSSHKeys adds SSH key files if they exist.
func writeSSHKeys(tw *tar.Writer, cfg *config.Config) (int, []string) {
	count := 0
	var warnings []string

	if cfg.SSH.HostKeyPath != "" {
		if err := writeTarFile(tw, "ssh/ssh_host_key", cfg.SSH.HostKeyPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("SSH host key not found: %v", err))
		} else {
			count++
		}
	}

	if cfg.SSH.AuthorizedKeysPath != "" {
		if err := writeTarFile(tw, "ssh/authorized_keys", cfg.SSH.AuthorizedKeysPath); err != nil {
			warnings = append(warnings, fmt.Sprintf("SSH authorized keys not found: %v", err))
		} else {
			count++
		}
	}

	return count, warnings
}
