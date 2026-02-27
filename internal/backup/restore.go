package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

		dest, skip := mapEntryToDestination(hdr.Name, manifest, opts)
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
// Returns (path, skip). Empty path with skip=false means ignore silently.
func mapEntryToDestination(name string, m *BackupManifest, opts RestoreOptions) (string, bool) {
	switch {
	case name == "manifest.json":
		return "", false

	case strings.HasPrefix(name, "database/"):
		relName := strings.TrimPrefix(name, "database/")
		if opts.DatabasePath != "" {
			return opts.DatabasePath, false
		}
		return filepath.Join(filepath.Dir(m.OriginalPaths.Database), relName), false

	case strings.HasPrefix(name, "config/"):
		if opts.SkipConfig {
			return "", true
		}
		if opts.ConfigPath != "" {
			return opts.ConfigPath, false
		}
		return m.OriginalPaths.Config, false

	case strings.HasPrefix(name, "workspace/"):
		rel := strings.TrimPrefix(name, "workspace/")
		baseDir := m.OriginalPaths.WorkspaceDir
		if opts.WorkspacePath != "" {
			baseDir = opts.WorkspacePath
		}
		return filepath.Join(baseDir, rel), false

	case strings.HasPrefix(name, "ssh/"):
		if !opts.RestoreSSHKeys {
			return "", true
		}
		base := filepath.Base(name)
		switch base {
		case "ssh_host_key":
			if m.OriginalPaths.SSHHostKey != "" {
				return m.OriginalPaths.SSHHostKey, false
			}
		case "authorized_keys":
			if m.OriginalPaths.SSHAuthKeys != "" {
				return m.OriginalPaths.SSHAuthKeys, false
			}
		}
		return "", true

	case strings.HasPrefix(name, "skills/"):
		// Restore skills to original paths based on directory name.
		parts := strings.SplitN(strings.TrimPrefix(name, "skills/"), "/", 2)
		if len(parts) < 2 {
			return "", false
		}
		dirName := parts[0]
		relPath := parts[1]
		for _, sp := range m.OriginalPaths.SkillsPaths {
			if filepath.Base(sp) == dirName {
				return filepath.Join(sp, relPath), false
			}
		}
		return "", true

	default:
		return "", false
	}
}

// extractFile writes a tar entry to disk, creating parent directories as needed.
func extractFile(tr *tar.Reader, hdr *tar.Header, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	mode := os.FileMode(hdr.Mode)
	if mode == 0 {
		mode = 0644
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, tr); err != nil {
		return err
	}
	return nil
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

		dest, skip := mapEntryToDestination(hdr.Name, m, opts)
		if skip {
			fmt.Printf("  SKIP  %s\n", hdr.Name)
			result.FilesSkipped++
			continue
		}
		if dest == "" {
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
