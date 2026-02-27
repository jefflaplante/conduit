package datadir

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultDirName is the default data directory name under $HOME.
	DefaultDirName = ".conduit"

	// EnvVar is the environment variable that overrides the data directory.
	EnvVar = "CONDUIT_DATA_DIR"

	// subdirectory names inside the data root
	configSubdir    = "config"
	authSubdir      = "auth"
	sshSubdir       = "ssh"
	databaseSubdir  = "data"
	workspaceSubdir = "workspace"
)

// DataDir provides a single source of truth for all data-directory paths.
// Use New to construct an instance, which resolves the root and optionally
// creates the directory tree.
type DataDir struct {
	root string
}

// New returns a DataDir rooted at the resolved data directory.
// It does NOT create subdirectories; call EnsureDirs for that.
//
// Resolution priority:
//  1. CONDUIT_DATA_DIR environment variable
//  2. configValue argument (from config.json data_dir field)
//  3. ~/.conduit/
func New(configValue string) (*DataDir, error) {
	root, err := resolveRoot(configValue)
	if err != nil {
		return nil, err
	}
	return &DataDir{root: root}, nil
}

// Root returns the base data directory path.
func (d *DataDir) Root() string { return d.root }

// ConfigDir returns {root}/config/.
func (d *DataDir) ConfigDir() string { return filepath.Join(d.root, configSubdir) }

// AuthDir returns {root}/auth/.
func (d *DataDir) AuthDir() string { return filepath.Join(d.root, authSubdir) }

// SSHDir returns {root}/ssh/.
func (d *DataDir) SSHDir() string { return filepath.Join(d.root, sshSubdir) }

// DatabaseDir returns {root}/data/.
func (d *DataDir) DatabaseDir() string { return filepath.Join(d.root, databaseSubdir) }

// WorkspaceDir returns {root}/workspace/.
func (d *DataDir) WorkspaceDir() string { return filepath.Join(d.root, workspaceSubdir) }

// FilePath returns the full path to a file directly inside the root directory.
func (d *DataDir) FilePath(filename string) string {
	return filepath.Join(d.root, filename)
}

// AuthFilePath returns the full path to a file inside the auth subdirectory.
func (d *DataDir) AuthFilePath(filename string) string {
	return filepath.Join(d.AuthDir(), filename)
}

// SSHFilePath returns the full path to a file inside the ssh subdirectory.
func (d *DataDir) SSHFilePath(filename string) string {
	return filepath.Join(d.SSHDir(), filename)
}

// subdirectories returns all managed subdirectory paths.
func (d *DataDir) subdirectories() []string {
	return []string{
		d.ConfigDir(),
		d.AuthDir(),
		d.SSHDir(),
		d.DatabaseDir(),
		d.WorkspaceDir(),
	}
}

// EnsureDirs creates the root and all subdirectories with 0700 permissions.
func (d *DataDir) EnsureDirs() error {
	dirs := append([]string{d.root}, d.subdirectories()...)
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// -----------------------------------------------------------------------
// Backward-compatible package-level functions (used by existing callers)
// -----------------------------------------------------------------------

// Resolve returns the data directory path, creating it with 0700 permissions
// if it doesn't already exist.
//
// Resolution priority:
//  1. CONDUIT_DATA_DIR environment variable
//  2. configValue argument (from config.json data_dir field)
//  3. ~/.conduit/
func Resolve(configValue string) (string, error) {
	root, err := resolveRoot(configValue)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", fmt.Errorf("failed to create data directory %s: %w", root, err)
	}
	return root, nil
}

// FilePath returns the full path to a file inside the data directory,
// ensuring the directory exists.
func FilePath(configValue, filename string) (string, error) {
	dir, err := Resolve(configValue)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}

// -----------------------------------------------------------------------
// Internal helpers
// -----------------------------------------------------------------------

// resolveRoot determines the root path without creating it.
func resolveRoot(configValue string) (string, error) {
	dir := os.Getenv(EnvVar)
	if dir == "" {
		dir = configValue
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, DefaultDirName)
	}
	return dir, nil
}
