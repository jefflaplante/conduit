package datadir

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvFileEnvVar allows overriding the .env file path entirely.
	EnvFileEnvVar = "CONDUIT_ENV_FILE"
)

// LoadEnv loads KEY=VALUE .env files from standard locations in priority order.
// Later files do NOT override values set by earlier files (first-write-wins),
// and existing environment variables are never overridden.
//
// Default search order:
//  1. CONDUIT_ENV_FILE (if set, only that file is loaded)
//  2. {datadir}/.env
//  3. Project-level .env (current working directory)
//
// Extra directories may be supplied via dirs; a .env file in each is tried
// after the standard locations.
func LoadEnv(dataRoot string, dirs ...string) error {
	paths := findEnvPaths(dataRoot, dirs...)
	seen := make(map[string]bool) // track which keys we already set

	for _, p := range paths {
		if err := loadEnvFile(p, seen); err != nil {
			return fmt.Errorf("failed to load %s: %w", p, err)
		}
	}
	return nil
}

// FindEnvFiles returns all .env file paths that would be loaded, in order.
// Files that don't exist on disk are excluded.
func FindEnvFiles(dataRoot string, dirs ...string) []string {
	candidates := findEnvPaths(dataRoot, dirs...)
	var found []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return found
}

// findEnvPaths builds the candidate list of .env file paths.
func findEnvPaths(dataRoot string, dirs ...string) []string {
	// If CONDUIT_ENV_FILE is set, it is the sole source.
	if override := os.Getenv(EnvFileEnvVar); override != "" {
		return []string{override}
	}

	var paths []string

	// 1. {datadir}/.env
	if dataRoot != "" {
		paths = append(paths, filepath.Join(dataRoot, ".env"))
	}

	// 2. Project-level .env (cwd)
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}

	// 3. Extra directories
	for _, d := range dirs {
		if d != "" {
			paths = append(paths, filepath.Join(d, ".env"))
		}
	}

	return dedupPaths(paths)
}

// loadEnvFile reads a single KEY=VALUE file. Keys already in `seen` or already
// set in the environment are skipped. Missing files are silently ignored.
func loadEnvFile(path string, seen map[string]bool) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil // missing file is fine
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Strip optional surrounding quotes
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// First-write-wins: skip if we loaded this key from a higher-priority file.
		if seen[key] {
			continue
		}
		// Never override existing environment variables.
		if _, exists := os.LookupEnv(key); exists {
			seen[key] = true
			continue
		}

		os.Setenv(key, value)
		seen[key] = true
	}
	return scanner.Err()
}

// dedupPaths removes duplicate paths (after cleaning) while preserving order.
func dedupPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var out []string
	for _, p := range paths {
		clean := filepath.Clean(p)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, p)
	}
	return out
}
