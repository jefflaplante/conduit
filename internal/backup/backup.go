package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
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

	absConfigPath, err := filepath.Abs(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	paths := OriginalPaths{
		Config:       absConfigPath,
		Database:     dbPath,
		WorkspaceDir: wsDir,
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

	// Create archive.
	outFile, err := os.Create(outPath)
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

// snapshotDatabase creates a clean snapshot via VACUUM INTO, falling back to file copy.
func snapshotDatabase(ctx context.Context, srcPath, dstPath string) (DatabaseInfo, error) {
	info := DatabaseInfo{}

	stat, err := os.Stat(srcPath)
	if err != nil {
		return info, fmt.Errorf("stat database: %w", err)
	}
	info.Size = stat.Size()

	// Try VACUUM INTO for a clean, WAL-free snapshot.
	db, err := sql.Open("sqlite", srcPath+"?mode=ro")
	if err == nil {
		defer db.Close()

		// Count tables.
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&count); err == nil {
			info.TableCount = count
		}

		_, vacErr := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(dstPath, "'", "''")))
		if vacErr == nil {
			return info, nil
		}
	}

	// Fallback: file copy.
	if err := copyFile(srcPath, dstPath); err != nil {
		return info, fmt.Errorf("copy database: %w", err)
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

// copyFile is a simple file copy helper.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
