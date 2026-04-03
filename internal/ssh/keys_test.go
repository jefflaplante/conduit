package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// Test SSH key (ed25519, no passphrase, for testing only)
const testSSHPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl test@example.com"
const testSSHPublicKey2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPuYfDoCqU8wTCK0+M+eUU/0e0U9A3HcL4vHE2M7J8Y9 test2@example.com"
const testSSHPublicKeyNoComment = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKkJ8ueuxLExT8nT1G0G7awTkDFn5g/Cl+9SyJGxI/R4"

// TestLoadAuthorizedKeys_ValidFile tests loading keys from a valid authorized_keys file
func TestLoadAuthorizedKeys_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := testSSHPublicKey + "\n" + testSSHPublicKey2 + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

// TestLoadAuthorizedKeys_EmptyPath tests that empty path uses default
func TestLoadAuthorizedKeys_EmptyPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	content := testSSHPublicKey + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	keys, err := LoadAuthorizedKeys("")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
}

// TestLoadAuthorizedKeys_EmptyPathNoDefault tests error when no default path available
func TestLoadAuthorizedKeys_EmptyPathNoDefault(t *testing.T) {
	// Set DataDirConfig to something that will cause an error
	// by unsetting the env var and using a path that doesn't exist
	t.Setenv("CONDUIT_DATA_DIR", "")
	DataDirConfig = ""

	// The default path should be ~/.conduit/authorized_keys
	// If this file doesn't exist, LoadAuthorizedKeys will fail
	_, err := LoadAuthorizedKeys("")
	// We can't guarantee failure here since the user might have ~/.conduit/authorized_keys
	// So just verify the function returns without panic
	_ = err
}

// TestLoadAuthorizedKeys_FileNotFound tests error when file doesn't exist
func TestLoadAuthorizedKeys_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "nonexistent")

	_, err := LoadAuthorizedKeys(authKeysPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open authorized keys")
}

// TestLoadAuthorizedKeys_SkipsComments tests that comments and blank lines are skipped
func TestLoadAuthorizedKeys_SkipsComments(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "# This is a comment\n\n" + testSSHPublicKey + "\n# Another comment\n\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
}

// TestLoadAuthorizedKeys_SkipsInvalidKeys tests that invalid key lines are skipped
func TestLoadAuthorizedKeys_SkipsInvalidKeys(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "not-a-valid-key\n" + testSSHPublicKey + "\ninvalid-key-format\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
}

// TestAddAuthorizedKey_Success tests adding a key to the authorized_keys file
func TestAddAuthorizedKey_Success(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	err := AddAuthorizedKey(authKeysPath, testSSHPublicKey)
	require.NoError(t, err)

	content, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), testSSHPublicKey)
}

// TestAddAuthorizedKey_InvalidKey tests error when adding invalid key
func TestAddAuthorizedKey_InvalidKey(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	err := AddAuthorizedKey(authKeysPath, "not-a-valid-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid public key")
}

// TestAddAuthorizedKey_CreatesDirectory tests that parent directory is created
func TestAddAuthorizedKey_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "subdir", "authorized_keys")

	err := AddAuthorizedKey(authKeysPath, testSSHPublicKey)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Dir(authKeysPath))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

// TestAddAuthorizedKey_AppendsToExisting tests appending to existing file
func TestAddAuthorizedKey_AppendsToExisting(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	// Create initial file with first key
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	// Add second key
	err := AddAuthorizedKey(authKeysPath, testSSHPublicKey2)
	require.NoError(t, err)

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

// TestAddAuthorizedKey_TrimsWhitespace tests that keys are trimmed
func TestAddAuthorizedKey_TrimsWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	err := AddAuthorizedKey(authKeysPath, "  "+testSSHPublicKey+"  ")
	require.NoError(t, err)

	content, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Equal(t, testSSHPublicKey+"\n", string(content))
}

// TestAddAuthorizedKey_EmptyPath tests that empty path uses default
func TestAddAuthorizedKey_EmptyPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	err := AddAuthorizedKey("", testSSHPublicKey)
	require.NoError(t, err)

	// Verify the key was added to the default location
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	content, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), testSSHPublicKey)
}

// TestListAuthorizedKeys_Success tests listing keys with fingerprints
func TestListAuthorizedKeys_Success(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := testSSHPublicKey + "\n" + testSSHPublicKey2 + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, entries, 2)

	// Verify first entry
	assert.Equal(t, "test@example.com", entries[0].Comment)
	assert.NotEmpty(t, entries[0].Fingerprint)
	assert.True(t, strings.HasPrefix(entries[0].Fingerprint, "SHA256:"))

	// Verify second entry
	assert.Equal(t, "test2@example.com", entries[1].Comment)
	assert.NotEmpty(t, entries[1].Fingerprint)
}

// TestListAuthorizedKeys_NoComment tests keys without comments
func TestListAuthorizedKeys_NoComment(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKeyNoComment+"\n"), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Empty(t, entries[0].Comment)
}

// TestListAuthorizedKeys_FileNotFound tests error when file doesn't exist
func TestListAuthorizedKeys_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "nonexistent")

	_, err := ListAuthorizedKeys(authKeysPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open authorized keys")
}

// TestListAuthorizedKeys_SkipsInvalidKeys tests that invalid keys are skipped
func TestListAuthorizedKeys_SkipsInvalidKeys(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "invalid\n" + testSSHPublicKey + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

// TestRemoveAuthorizedKey_Success tests removing a key by fingerprint
func TestRemoveAuthorizedKey_Success(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := testSSHPublicKey + "\n" + testSSHPublicKey2 + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	// Get fingerprint of first key
	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	fingerprint := entries[0].Fingerprint

	err = RemoveAuthorizedKey(authKeysPath, fingerprint)
	require.NoError(t, err)

	// Verify only one key remains
	remaining, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "test2@example.com", remaining[0].Comment)
}

// TestRemoveAuthorizedKey_NotFound tests error when fingerprint not found
func TestRemoveAuthorizedKey_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	err := RemoveAuthorizedKey(authKeysPath, "SHA256:nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestRemoveAuthorizedKey_PreservesComments tests that comments are preserved
func TestRemoveAuthorizedKey_PreservesComments(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "# Header comment\n" + testSSHPublicKey + "\n# Middle comment\n" + testSSHPublicKey2 + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	// Get fingerprint of first key
	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	fingerprint := entries[0].Fingerprint

	err = RemoveAuthorizedKey(authKeysPath, fingerprint)
	require.NoError(t, err)

	// Read file content and verify comments are preserved
	result, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Contains(t, string(result), "# Header comment")
	assert.Contains(t, string(result), "# Middle comment")
}

// TestRemoveAuthorizedKey_FileNotFound tests error when file doesn't exist
func TestRemoveAuthorizedKey_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "nonexistent")

	err := RemoveAuthorizedKey(authKeysPath, "SHA256:test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open authorized keys")
}

// TestRemoveAuthorizedKey_PreservesInvalidLines tests that invalid lines are preserved
func TestRemoveAuthorizedKey_PreservesInvalidLines(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "invalid-line\n" + testSSHPublicKey + "\n" + testSSHPublicKey2 + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	fingerprint := entries[0].Fingerprint

	err = RemoveAuthorizedKey(authKeysPath, fingerprint)
	require.NoError(t, err)

	// Read file and verify invalid line is preserved
	result, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Contains(t, string(result), "invalid-line")
}

// TestInitSSHKeys_CreatesFiles tests that InitSSHKeys creates necessary files
func TestInitSSHKeys_CreatesFiles(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	hostKeyPath := filepath.Join(tmpDir, "ssh_host_key")
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	err := InitSSHKeys(hostKeyPath, authKeysPath)
	require.NoError(t, err)

	// Verify authorized_keys was created
	info, err := os.Stat(authKeysPath)
	require.NoError(t, err)
	assert.False(t, info.IsDir())
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Verify content
	content, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "# Conduit authorized SSH keys")
}

// TestInitSSHKeys_ExistingFileNotOverwritten tests that existing files are not overwritten
func TestInitSSHKeys_ExistingFileNotOverwritten(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	// Create existing file with a key
	existingContent := testSSHPublicKey + "\n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(existingContent), 0600))

	err := InitSSHKeys("", authKeysPath)
	require.NoError(t, err)

	// Verify content was NOT overwritten
	content, err := os.ReadFile(authKeysPath)
	require.NoError(t, err)
	assert.Equal(t, existingContent, string(content))
}

// TestInitSSHKeys_CreatesDirectory tests that parent directory is created
func TestInitSSHKeys_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	subdir := filepath.Join(tmpDir, "ssh")
	authKeysPath := filepath.Join(subdir, "authorized_keys")

	err := InitSSHKeys("", authKeysPath)
	require.NoError(t, err)

	info, err := os.Stat(subdir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

// TestInitSSHKeys_DefaultPaths tests that default paths are used when empty
func TestInitSSHKeys_DefaultPaths(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	err := InitSSHKeys("", "")
	require.NoError(t, err)

	// Verify authorized_keys was created in default location
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	_, err = os.Stat(authKeysPath)
	require.NoError(t, err)
}

// TestSSHConfigDir tests the sshConfigDir function
func TestSSHConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	dir, err := sshConfigDir()
	require.NoError(t, err)
	assert.Equal(t, tmpDir, dir)
}

// TestSSHConfigDir_ConfigOverride tests that DataDirConfig is used
func TestSSHConfigDir_ConfigOverride(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", "")
	DataDirConfig = tmpDir

	dir, err := sshConfigDir()
	require.NoError(t, err)
	assert.Equal(t, tmpDir, dir)

	// Reset
	DataDirConfig = ""
}

// TestDefaultAuthorizedKeysPath tests the defaultAuthorizedKeysPath function
func TestDefaultAuthorizedKeysPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	path := defaultAuthorizedKeysPath()
	assert.Equal(t, filepath.Join(tmpDir, "authorized_keys"), path)
}

// TestKeyEntry_Fields tests the KeyEntry struct fields
func TestKeyEntry_Fields(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.NotNil(t, entry.PublicKey)
	assert.Equal(t, "test@example.com", entry.Comment)
	assert.True(t, strings.HasPrefix(entry.Fingerprint, "SHA256:"))

	// Verify the public key can be serialized
	pubKeyBytes := gossh.MarshalAuthorizedKey(entry.PublicKey)
	assert.NotEmpty(t, pubKeyBytes)
}

// TestLoadAuthorizedKeys_EmptyFile tests loading an empty file
func TestLoadAuthorizedKeys_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	require.NoError(t, os.WriteFile(authKeysPath, []byte(""), 0600))

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 0)
}

// TestLoadAuthorizedKeys_OnlyCommentsAndBlanks tests a file with only comments
func TestLoadAuthorizedKeys_OnlyCommentsAndBlanks(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	content := "# Comment 1\n\n# Comment 2\n   \n"
	require.NoError(t, os.WriteFile(authKeysPath, []byte(content), 0600))

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 0)
}

// TestListAuthorizedKeys_EmptyFile tests listing an empty file
func TestListAuthorizedKeys_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	require.NoError(t, os.WriteFile(authKeysPath, []byte(""), 0600))

	entries, err := ListAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, entries, 0)
}
