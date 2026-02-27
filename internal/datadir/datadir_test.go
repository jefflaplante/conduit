package datadir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Package-level backward-compat functions ---

func TestResolve_EnvVarWins(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env-dir")

	t.Setenv(EnvVar, envDir)

	got, err := Resolve("/should/be/ignored")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != envDir {
		t.Errorf("got %s, want %s", got, envDir)
	}
	// Directory should have been created.
	info, err := os.Stat(envDir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("permissions: got %o, want 0700", perm)
	}
}

func TestResolve_ConfigValueFallback(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg-dir")

	t.Setenv(EnvVar, "") // clear env var

	got, err := Resolve(cfgDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != cfgDir {
		t.Errorf("got %s, want %s", got, cfgDir)
	}
}

func TestResolve_DefaultHome(t *testing.T) {
	t.Setenv(EnvVar, "")

	got, err := Resolve("")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, DefaultDirName)
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestFilePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVar, dir)

	got, err := FilePath("", "auth.json")
	if err != nil {
		t.Fatalf("FilePath: %v", err)
	}
	want := filepath.Join(dir, "auth.json")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// --- DataDir struct tests ---

func TestNew_EnvVarWins(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "env-root")
	t.Setenv(EnvVar, envDir)

	dd, err := New("ignored-config-value")
	require.NoError(t, err)
	assert.Equal(t, envDir, dd.Root())
}

func TestNew_ConfigFallback(t *testing.T) {
	t.Setenv(EnvVar, "")
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "from-config")

	dd, err := New(cfgDir)
	require.NoError(t, err)
	assert.Equal(t, cfgDir, dd.Root())
}

func TestNew_DefaultHome(t *testing.T) {
	t.Setenv(EnvVar, "")
	home, _ := os.UserHomeDir()

	dd, err := New("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, DefaultDirName), dd.Root())
}

func TestDataDir_Subdirectories(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvVar, root)

	dd, err := New("")
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(root, "config"), dd.ConfigDir())
	assert.Equal(t, filepath.Join(root, "auth"), dd.AuthDir())
	assert.Equal(t, filepath.Join(root, "ssh"), dd.SSHDir())
	assert.Equal(t, filepath.Join(root, "data"), dd.DatabaseDir())
	assert.Equal(t, filepath.Join(root, "workspace"), dd.WorkspaceDir())
}

func TestDataDir_FilePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvVar, root)

	dd, err := New("")
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(root, "somefile"), dd.FilePath("somefile"))
	assert.Equal(t, filepath.Join(root, "auth", "tokens.json"), dd.AuthFilePath("tokens.json"))
	assert.Equal(t, filepath.Join(root, "ssh", "host_key"), dd.SSHFilePath("host_key"))
}

func TestDataDir_EnsureDirs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh")
	t.Setenv(EnvVar, root)

	dd, err := New("")
	require.NoError(t, err)

	// Before EnsureDirs, root should not exist.
	_, err = os.Stat(root)
	assert.True(t, os.IsNotExist(err))

	require.NoError(t, dd.EnsureDirs())

	// All subdirectories should exist with 0700.
	for _, dir := range []string{
		dd.Root(),
		dd.ConfigDir(),
		dd.AuthDir(),
		dd.SSHDir(),
		dd.DatabaseDir(),
		dd.WorkspaceDir(),
	} {
		info, err := os.Stat(dir)
		require.NoError(t, err, "dir should exist: %s", dir)
		assert.True(t, info.IsDir(), "should be directory: %s", dir)
		assert.Equal(t, os.FileMode(0700), info.Mode().Perm(), "permissions of %s", dir)
	}
}

func TestDataDir_EnsureDirs_Idempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvVar, root)

	dd, err := New("")
	require.NoError(t, err)

	require.NoError(t, dd.EnsureDirs())
	// Write a file into one of the subdirs.
	require.NoError(t, os.WriteFile(filepath.Join(dd.AuthDir(), "test"), []byte("data"), 0600))

	// Second call should not fail or remove the file.
	require.NoError(t, dd.EnsureDirs())

	data, err := os.ReadFile(filepath.Join(dd.AuthDir(), "test"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(data))
}
