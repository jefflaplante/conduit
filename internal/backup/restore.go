package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"conduit/internal/config"
)

// RestoreBackup extracts a backup archive to the appropriate locations.
func RestoreBackup(opts RestoreOptions) (*RestoreResult, error) {
	manifest, err := readManifestFromArchive(opts.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	if err := ValidateManifest(manifest); err != nil {
		return nil, fmt.Errorf("invalid backup: %w", err)
	}

	result := &RestoreResult{
		Components: manifest.Components,
	}

	if opts.DryRun {
		return dryRunRestore(manifest, opts, result)
	}

	// conduit-31jg.9: never overwrite files under a running gateway (its open
	// DB handles and WAL would corrupt the restored databases). --force only
	// skips the prompt below; it does not bypass this check.
	if err := CheckGatewayStopped(opts.PidfilePath, opts.GatewayAddr); err != nil {
		return nil, err
	}

	if !opts.Force {
		fmt.Println("WARNING: The gateway should be stopped before restoring a backup.")
		fmt.Println("This will overwrite existing files at the target locations.")
		fmt.Printf("Backup from: %s (gateway %s)\n", manifest.Timestamp.Format("2006-01-02 15:04:05 UTC"), manifest.GatewayVersion)
		fmt.Printf("Components: %s\n", manifest.Components)
		fmt.Print("\nContinue? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			return nil, fmt.Errorf("restore cancelled by user")
		}
	}

	f, err := os.Open(opts.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		dest, root, skip := mapEntryToDestination(hdr.Name, manifest, opts)
		if skip {
			result.FilesSkipped++
			if opts.Verbose {
				result.Warnings = append(result.Warnings, fmt.Sprintf("skipped: %s", hdr.Name))
			}
			continue
		}
		if dest == "" {
			// manifest.json or unrecognized — skip silently.
			continue
		}

		// conduit-31jg.9: validate against the entry's containment root (the
		// workspace/skills/database directory). Previously targetDir was
		// always "", so "workspace/../config.json" escaped the workspace.
		if err := validateTarEntry(hdr, root, dest); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("rejected %s: %v", hdr.Name, err))
			result.FilesSkipped++
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		if err := extractFile(tr, hdr, dest); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("failed to restore %s: %v", hdr.Name, err))
			result.FilesSkipped++
			continue
		}
		result.FilesRestored++
	}

	return result, nil
}

// mapEntryToDestination determines the on-disk path for a tar entry.
// Returns (path, root, skip). Empty path with skip=false means ignore
// silently. root is the directory the destination must stay inside when the
// path is derived from the (untrusted) entry name; it is "" for entries whose
// destination is fixed by the manifest/options (config, SSH keys, DBs).
func mapEntryToDestination(name string, m *BackupManifest, opts RestoreOptions) (string, string, bool) {
	switch {
	case name == "manifest.json":
		return "", "", false

	case name == "database/brain.db":
		// conduit-31jg.9: restore to the brain DB path that was backed up
		// (honours brain.path) unless the gateway DB is being relocated.
		if opts.DatabasePath != "" {
			return config.DeriveBrainDBPath(opts.DatabasePath), "", false
		}
		if m.OriginalPaths.BrainDatabase != "" {
			return m.OriginalPaths.BrainDatabase, "", false
		}
		return config.DeriveBrainDBPath(m.OriginalPaths.Database), "", false

	case strings.HasPrefix(name, "database/"):
		relName := strings.TrimPrefix(name, "database/")
		if opts.DatabasePath != "" {
			return opts.DatabasePath, "", false
		}
		root := filepath.Dir(m.OriginalPaths.Database)
		return filepath.Join(root, relName), root, false

	case strings.HasPrefix(name, "config/"):
		if opts.SkipConfig {
			return "", "", true
		}
		if opts.ConfigPath != "" {
			return opts.ConfigPath, "", false
		}
		return m.OriginalPaths.Config, "", false

	case strings.HasPrefix(name, "workspace/"):
		rel := strings.TrimPrefix(name, "workspace/")
		baseDir := m.OriginalPaths.WorkspaceDir
		if opts.WorkspacePath != "" {
			baseDir = opts.WorkspacePath
		}
		return filepath.Join(baseDir, rel), baseDir, false

	case strings.HasPrefix(name, "ssh/"):
		if !opts.RestoreSSHKeys {
			return "", "", true
		}
		base := filepath.Base(name)
		switch base {
		case "ssh_host_key":
			if m.OriginalPaths.SSHHostKey != "" {
				return m.OriginalPaths.SSHHostKey, "", false
			}
		case "authorized_keys":
			if m.OriginalPaths.SSHAuthKeys != "" {
				return m.OriginalPaths.SSHAuthKeys, "", false
			}
		}
		return "", "", true

	case strings.HasPrefix(name, "skills/"):
		// Restore skills to original paths based on directory name.
		parts := strings.SplitN(strings.TrimPrefix(name, "skills/"), "/", 2)
		if len(parts) < 2 {
			return "", "", false
		}
		dirName := parts[0]
		relPath := parts[1]
		for _, sp := range m.OriginalPaths.SkillsPaths {
			if filepath.Base(sp) == dirName {
				return filepath.Join(sp, relPath), sp, false
			}
		}
		return "", "", true

	default:
		return "", "", false
	}
}

// validateTarEntry checks a tar header for path traversal and unsafe entry types.
// targetDir is the root directory files are being extracted into.
// dest is the resolved destination path for the entry.
func validateTarEntry(hdr *tar.Header, targetDir, dest string) error {
	// Reject symlinks and hard links
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeGNUSparse:
		// These are safe entry types
	case tar.TypeSymlink:
		return fmt.Errorf("symlink entry not allowed: %s -> %s", hdr.Name, hdr.Linkname)
	case tar.TypeLink:
		return fmt.Errorf("hard link entry not allowed: %s -> %s", hdr.Name, hdr.Linkname)
	default:
		return fmt.Errorf("unsupported tar entry type %d: %s", hdr.Typeflag, hdr.Name)
	}

	// Clean the entry name and reject obviously malicious paths
	cleaned := filepath.Clean(hdr.Name)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(hdr.Name, "/") {
		return fmt.Errorf("absolute path in tar entry: %s", hdr.Name)
	}
	if strings.HasPrefix(cleaned, "..") {
		return fmt.Errorf("path traversal in tar entry: %s", hdr.Name)
	}
	// Validate the resolved destination is within the target directory.
	// conduit-31jg.9: compare symlink-resolved paths so a symlinked
	// directory inside the target cannot redirect the write elsewhere.
	if targetDir != "" {
		cleanDest := resolveExisting(filepath.Clean(dest))
		cleanTarget := resolveExisting(filepath.Clean(targetDir))
		rel, err := filepath.Rel(cleanTarget, cleanDest)
		if err != nil {
			return fmt.Errorf("cannot resolve path %s relative to %s: %w", dest, targetDir, err)
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("path escapes target directory: %s resolves to %s", hdr.Name, cleanDest)
		}
	}

	return nil
}

// resolveExisting resolves symlinks in the longest existing prefix of p and
// re-appends the non-existent remainder. conduit-31jg.9.
func resolveExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	var rest []string
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, rest...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// maxExtractFileSize is the maximum allowed size for a single extracted file (1 GB).
// This prevents decompression bomb attacks where a small compressed file expands to
// an extremely large file on disk.
//
// Declared as a var (not a const) so tests can shrink it to avoid allocating gigabytes
// of memory when exercising the size-limit path under the race detector.
var maxExtractFileSize int64 = 1 << 30 // 1 GB

// extractFile writes a tar entry to disk, creating parent directories as
// needed. File size is capped at the smaller of hdr.Size and
// maxExtractFileSize.
//
// conduit-31jg.9: the content is written to a temp file in the destination
// directory and renamed into place, so a failed/partial restore never leaves
// a truncated file and an existing symlink at dest is replaced rather than
// followed. For SQLite databases (*.db) any stale "-wal"/"-shm" sidecars are
// removed before the rename — otherwise SQLite would replay the old WAL
// frames onto the restored file.
func extractFile(tr *tar.Reader, hdr *tar.Header, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0644
	}

	// Determine the size limit: use the smaller of the declared size and our cap
	limit := maxExtractFileSize
	if hdr.Size > 0 && hdr.Size < limit {
		limit = hdr.Size
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}

	// Use LimitReader to prevent decompression bombs.
	// Read limit+1 bytes so we can detect if the file exceeds the limit.
	n, err := io.Copy(tmp, io.LimitReader(tr, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("file %s exceeds size limit (%d bytes > %d byte cap)", hdr.Name, n, limit)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if isSQLiteDBPath(dest) {
		for _, sfx := range []string{"-wal", "-shm", "-journal"} {
			if err := os.Remove(dest + sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove stale %s: %w", dest+sfx, err)
			}
		}
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	committed = true
	return nil
}

// isSQLiteDBPath reports whether path names a SQLite database file.
func isSQLiteDBPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return true
	}
	return false
}

// CheckGatewayStopped returns an error when the gateway appears to be
// running: pidfile names a live process, or addr (host:port) accepts a TCP
// connection. Empty arguments skip that check. conduit-31jg.9.
func CheckGatewayStopped(pidfile, addr string) error {
	if pidfile != "" {
		if data, err := os.ReadFile(pidfile); err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 && processAlive(pid) {
				return fmt.Errorf("gateway appears to be running (pid %d from %s); stop it before restoring (if that process is not the gateway, remove the stale pidfile)", pid, pidfile)
			}
		}
	}
	if addr != "" {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return fmt.Errorf("gateway appears to be running (something is listening on %s); stop it before restoring", addr)
		}
	}
	return nil
}

// processAlive reports whether a process with the given PID exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// dryRunRestore reports what would be restored without writing any files.
func dryRunRestore(m *BackupManifest, opts RestoreOptions, result *RestoreResult) (*RestoreResult, error) {
	f, err := os.Open(opts.BackupPath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	fmt.Printf("Dry-run restore of: %s\n", opts.BackupPath)
	fmt.Printf("Backup from: %s (gateway %s)\n", m.Timestamp.Format("2006-01-02 15:04:05 UTC"), m.GatewayVersion)
	fmt.Printf("Components: %s\n\n", m.Components)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry: %w", err)
		}

		dest, root, skip := mapEntryToDestination(hdr.Name, m, opts)
		if skip {
			fmt.Printf("  SKIP  %s\n", hdr.Name)
			result.FilesSkipped++
			continue
		}
		if dest == "" {
			continue
		}
		if err := validateTarEntry(hdr, root, dest); err != nil {
			fmt.Printf("  REJECT %s: %v\n", hdr.Name, err)
			result.FilesSkipped++
			continue
		}

		fmt.Printf("  WRITE %s -> %s\n", hdr.Name, dest)
		result.FilesRestored++
	}

	fmt.Printf("\nWould restore %d files, skip %d files\n", result.FilesRestored, result.FilesSkipped)
	return result, nil
}

// readManifestFromArchive opens the archive and extracts manifest.json.
func readManifestFromArchive(archivePath string) (*BackupManifest, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		if hdr.Name == "manifest.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read manifest: %w", err)
			}
			return UnmarshalManifest(data)
		}
	}

	return nil, fmt.Errorf("manifest.json not found in archive")
}
