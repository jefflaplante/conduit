package datadir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadEnv_DataDirEnv(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("TEST_LOAD_ENV_A=hello\n"), 0600))

	// Ensure the variable is not already set.
	t.Setenv("TEST_LOAD_ENV_A", "")
	os.Unsetenv("TEST_LOAD_ENV_A")
	t.Setenv(EnvFileEnvVar, "")

	require.NoError(t, LoadEnv(root))
	assert.Equal(t, "hello", os.Getenv("TEST_LOAD_ENV_A"))

	// Cleanup so the variable doesn't leak.
	os.Unsetenv("TEST_LOAD_ENV_A")
}

func TestLoadEnv_ExistingEnvNotOverridden(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	require.NoError(t, os.WriteFile(envFile, []byte("TEST_LOAD_ENV_B=from-file\n"), 0600))

	t.Setenv("TEST_LOAD_ENV_B", "from-env")
	t.Setenv(EnvFileEnvVar, "")

	require.NoError(t, LoadEnv(root))
	assert.Equal(t, "from-env", os.Getenv("TEST_LOAD_ENV_B"), "env var should not be overridden")
}

func TestLoadEnv_EnvFileOverride(t *testing.T) {
	// CONDUIT_ENV_FILE should be the sole source when set.
	dir := t.TempDir()
	envFile := filepath.Join(dir, "custom.env")
	require.NoError(t, os.WriteFile(envFile, []byte("TEST_LOAD_ENV_C=custom\n"), 0600))

	os.Unsetenv("TEST_LOAD_ENV_C")
	t.Setenv(EnvFileEnvVar, envFile)

	require.NoError(t, LoadEnv("/some/root"))
	assert.Equal(t, "custom", os.Getenv("TEST_LOAD_ENV_C"))

	os.Unsetenv("TEST_LOAD_ENV_C")
}

func TestLoadEnv_QuotedValues(t *testing.T) {
	root := t.TempDir()
	content := `TEST_LOAD_ENV_DQ="double quoted"
TEST_LOAD_ENV_SQ='single quoted'
TEST_LOAD_ENV_NQ=unquoted
# comment line
TEST_LOAD_ENV_SP = spaced = value
`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte(content), 0600))

	for _, k := range []string{"TEST_LOAD_ENV_DQ", "TEST_LOAD_ENV_SQ", "TEST_LOAD_ENV_NQ", "TEST_LOAD_ENV_SP"} {
		os.Unsetenv(k)
	}
	t.Setenv(EnvFileEnvVar, "")

	require.NoError(t, LoadEnv(root))

	assert.Equal(t, "double quoted", os.Getenv("TEST_LOAD_ENV_DQ"))
	assert.Equal(t, "single quoted", os.Getenv("TEST_LOAD_ENV_SQ"))
	assert.Equal(t, "unquoted", os.Getenv("TEST_LOAD_ENV_NQ"))
	assert.Equal(t, "spaced = value", os.Getenv("TEST_LOAD_ENV_SP"))

	for _, k := range []string{"TEST_LOAD_ENV_DQ", "TEST_LOAD_ENV_SQ", "TEST_LOAD_ENV_NQ", "TEST_LOAD_ENV_SP"} {
		os.Unsetenv(k)
	}
}

func TestLoadEnv_MissingFileIsOK(t *testing.T) {
	t.Setenv(EnvFileEnvVar, "")
	require.NoError(t, LoadEnv("/nonexistent/path"))
}

func TestLoadEnv_PriorityOrder(t *testing.T) {
	// Data-dir .env takes priority over project-level .env.
	root := t.TempDir()
	projectDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"),
		[]byte("TEST_LOAD_ENV_PRIO=from-root\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, ".env"),
		[]byte("TEST_LOAD_ENV_PRIO=from-project\n"), 0600))

	os.Unsetenv("TEST_LOAD_ENV_PRIO")
	t.Setenv(EnvFileEnvVar, "")

	require.NoError(t, LoadEnv(root, projectDir))
	assert.Equal(t, "from-root", os.Getenv("TEST_LOAD_ENV_PRIO"))

	os.Unsetenv("TEST_LOAD_ENV_PRIO")
}

func TestFindEnvFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".env"), []byte("K=V\n"), 0600))

	t.Setenv(EnvFileEnvVar, "")

	files := FindEnvFiles(root)
	// Should include root .env (and possibly cwd if it exists).
	found := false
	for _, f := range files {
		if f == filepath.Join(root, ".env") {
			found = true
		}
	}
	assert.True(t, found, "should find %s in %v", filepath.Join(root, ".env"), files)
}

func TestFindEnvFiles_Override(t *testing.T) {
	f := filepath.Join(t.TempDir(), "custom.env")
	require.NoError(t, os.WriteFile(f, []byte("K=V\n"), 0600))

	t.Setenv(EnvFileEnvVar, f)

	files := FindEnvFiles("/ignored")
	assert.Equal(t, []string{f}, files)
}

func TestDedupPaths(t *testing.T) {
	result := dedupPaths([]string{"/a/b", "/c/d", "/a/b", "/c/d", "/e"})
	assert.Equal(t, []string{"/a/b", "/c/d", "/e"}, result)
}
