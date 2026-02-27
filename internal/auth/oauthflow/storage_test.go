package oauthflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/datadir"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempHome overrides HOME for the duration of a test so that
// storagePath() writes to a temp directory.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv(datadir.EnvVar, "") // ensure datadir falls back to HOME
	DataDirConfig = ""           // clear any config override
	return tmp
}

func TestStoredToken_IsExpired(t *testing.T) {
	t.Run("not expired", func(t *testing.T) {
		token := &StoredToken{ExpiresAt: time.Now().Add(time.Hour).Unix()}
		assert.False(t, token.IsExpired())
	})

	t.Run("expired", func(t *testing.T) {
		token := &StoredToken{ExpiresAt: time.Now().Add(-time.Hour).Unix()}
		assert.True(t, token.IsExpired())
	})
}

func TestStoredToken_ExpiresIn(t *testing.T) {
	token := &StoredToken{ExpiresAt: time.Now().Add(30 * time.Minute).Unix()}
	d := token.ExpiresIn()
	assert.InDelta(t, 30*time.Minute, d, float64(2*time.Second))
}

func TestSaveAndLoadProviderToken(t *testing.T) {
	withTempHome(t)

	token := &StoredToken{
		AccessToken:  "sk-ant-oat01-test-token",
		RefreshToken: "anthro-rt-test",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Scope:        "user:inference",
		ObtainedAt:   time.Now().Unix(),
		ClientID:     "test-client-id",
	}

	// Save.
	err := SaveProviderToken("anthropic", token)
	require.NoError(t, err)

	// Load.
	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, token.AccessToken, loaded.AccessToken)
	assert.Equal(t, token.RefreshToken, loaded.RefreshToken)
	assert.Equal(t, token.TokenType, loaded.TokenType)
	assert.Equal(t, token.ExpiresAt, loaded.ExpiresAt)
	assert.Equal(t, token.Scope, loaded.Scope)
	assert.Equal(t, token.ClientID, loaded.ClientID)
}

func TestLoadProviderToken_NotFound(t *testing.T) {
	withTempHome(t)

	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestDeleteProviderToken(t *testing.T) {
	withTempHome(t)

	// Save then delete.
	token := &StoredToken{AccessToken: "to-delete", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, SaveProviderToken("anthropic", token))

	require.NoError(t, DeleteProviderToken("anthropic"))

	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestSaveProviderToken_MultipleProviders(t *testing.T) {
	withTempHome(t)

	token1 := &StoredToken{AccessToken: "token-1", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	token2 := &StoredToken{AccessToken: "token-2", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}

	require.NoError(t, SaveProviderToken("anthropic", token1))
	require.NoError(t, SaveProviderToken("openai", token2))

	loaded1, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	assert.Equal(t, "token-1", loaded1.AccessToken)

	loaded2, err := LoadProviderToken("openai")
	require.NoError(t, err)
	assert.Equal(t, "token-2", loaded2.AccessToken)
}

func TestFilePermissions(t *testing.T) {
	home := withTempHome(t)

	token := &StoredToken{AccessToken: "perm-test", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, SaveProviderToken("anthropic", token))

	// Check directory permissions.
	dirInfo, err := os.Stat(filepath.Join(home, datadir.DefaultDirName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())

	// Check file permissions.
	fileInfo, err := os.Stat(filepath.Join(home, datadir.DefaultDirName, StorageFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm())
}

func TestMigrateFromLegacy(t *testing.T) {
	home := withTempHome(t)

	// Create legacy directory with auth.json.
	legacyDir := filepath.Join(home, legacyDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0700))
	legacyData := `{"version":1,"providers":{"anthropic":{"access_token":"legacy-token","token_type":"Bearer","expires_at":9999999999}}}`
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, StorageFile), []byte(legacyData), 0600))

	// Load should trigger migration.
	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "legacy-token", loaded.AccessToken)

	// New file should exist.
	newPath := filepath.Join(home, datadir.DefaultDirName, StorageFile)
	_, err = os.Stat(newPath)
	assert.NoError(t, err, "new auth.json should exist after migration")
}

func TestMigrateFromLegacy_NoOverwrite(t *testing.T) {
	home := withTempHome(t)

	// Create both legacy and new auth.json with different tokens.
	legacyDir := filepath.Join(home, legacyDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, StorageFile),
		[]byte(`{"version":1,"providers":{"anthropic":{"access_token":"old","token_type":"Bearer","expires_at":9999999999}}}`), 0600))

	newDir := filepath.Join(home, datadir.DefaultDirName)
	require.NoError(t, os.MkdirAll(newDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(newDir, StorageFile),
		[]byte(`{"version":1,"providers":{"anthropic":{"access_token":"new","token_type":"Bearer","expires_at":9999999999}}}`), 0600))

	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "new", loaded.AccessToken, "should not overwrite existing new file")
}

func TestSaveProviderToken_Overwrites(t *testing.T) {
	withTempHome(t)

	token1 := &StoredToken{AccessToken: "old-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, SaveProviderToken("anthropic", token1))

	token2 := &StoredToken{AccessToken: "new-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(2 * time.Hour).Unix()}
	require.NoError(t, SaveProviderToken("anthropic", token2))

	loaded, err := LoadProviderToken("anthropic")
	require.NoError(t, err)
	assert.Equal(t, "new-token", loaded.AccessToken)
}
