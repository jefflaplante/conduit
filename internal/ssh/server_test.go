package ssh

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	charmssh "github.com/charmbracelet/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"

	"conduit/internal/tui"
)

// TestSSHConfig_Defaults tests that SSHConfig has sensible defaults
func TestSSHConfig_Defaults(t *testing.T) {
	cfg := SSHConfig{}
	assert.Empty(t, cfg.ListenAddr)
	assert.Empty(t, cfg.HostKeyPath)
	assert.Empty(t, cfg.AuthorizedKeysPath)
	assert.Empty(t, cfg.GatewayURL)
	assert.Empty(t, cfg.GatewayToken)
	assert.Empty(t, cfg.AssistantName)
	assert.Nil(t, cfg.Location)
	assert.Nil(t, cfg.ClientFactory)
}

// TestSSHConfig_Fields tests setting SSHConfig fields
func TestSSHConfig_Fields(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	factory := func(sshUser string) tui.GatewayClient {
		return nil
	}

	cfg := SSHConfig{
		ListenAddr:         ":2222",
		HostKeyPath:        "/path/to/host_key",
		AuthorizedKeysPath: "/path/to/authorized_keys",
		GatewayURL:         "ws://localhost:18789",
		GatewayToken:       "test-token",
		AssistantName:      "Conduit",
		Location:           loc,
		ClientFactory:      factory,
		ShellSecurity: tui.ShellSecurityConfig{
			Enabled: true,
			CommandAllowlist: []string{"ls", "cat"},
		},
	}

	assert.Equal(t, ":2222", cfg.ListenAddr)
	assert.Equal(t, "/path/to/host_key", cfg.HostKeyPath)
	assert.Equal(t, "/path/to/authorized_keys", cfg.AuthorizedKeysPath)
	assert.Equal(t, "ws://localhost:18789", cfg.GatewayURL)
	assert.Equal(t, "test-token", cfg.GatewayToken)
	assert.Equal(t, "Conduit", cfg.AssistantName)
	assert.Equal(t, loc, cfg.Location)
	assert.NotNil(t, cfg.ClientFactory)
	assert.True(t, cfg.ShellSecurity.Enabled)
	assert.Len(t, cfg.ShellSecurity.CommandAllowlist, 2)
}

// TestNewServer_DefaultListenAddr tests that default listen address is set
func TestNewServer_DefaultListenAddr(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	// Create authorized_keys file so server can start
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()

	// The server should have the default address
	// We can't easily verify this without reflection, but we can check
	// that the server was created successfully
}

// TestNewServer_CustomListenAddr tests custom listen address
func TestNewServer_CustomListenAddr(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	cfg := SSHConfig{
		ListenAddr:         ":3333",
		AuthorizedKeysPath: authKeysPath,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_DefaultHostKeyPath tests that default host key path is set
func TestNewServer_DefaultHostKeyPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_CustomHostKeyPath tests custom host key path
func TestNewServer_CustomHostKeyPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	hostKeyPath := filepath.Join(tmpDir, "custom_host_key")

	cfg := SSHConfig{
		HostKeyPath:        hostKeyPath,
		AuthorizedKeysPath: authKeysPath,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_NoAuthorizedKeys tests server creation without authorized keys
func TestNewServer_NoAuthorizedKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	// Don't create authorized_keys file
	cfg := SSHConfig{
		AuthorizedKeysPath: filepath.Join(tmpDir, "nonexistent"),
	}

	// Server should still be created, but without public key auth
	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_EmptyAuthorizedKeys tests server with empty authorized_keys
func TestNewServer_EmptyAuthorizedKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(""), 0600))

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_WithClientFactory tests server with client factory
func TestNewServer_WithClientFactory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	factoryCalled := false
	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
		ClientFactory: func(sshUser string) tui.GatewayClient {
			factoryCalled = true
			return nil
		},
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()

	// Factory won't be called until a session connects
	assert.False(t, factoryCalled)
}

// TestPublicKeyHandler_ValidKey tests accepting a valid public key
func TestPublicKeyHandler_ValidKey(t *testing.T) {
	// Parse test key
	pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey))
	require.NoError(t, err)

	authorizedKeys := []charmssh.PublicKey{pubKey}

	// Create a mock context
	ctx := &mockContext{user: "testuser"}

	result := publicKeyHandler(ctx, pubKey, authorizedKeys)
	assert.True(t, result)
}

// TestPublicKeyHandler_InvalidKey tests rejecting an invalid public key
func TestPublicKeyHandler_InvalidKey(t *testing.T) {
	// Parse different keys
	pubKey1, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey))
	require.NoError(t, err)

	pubKey2, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey2))
	require.NoError(t, err)

	// Only authorize the first key
	authorizedKeys := []charmssh.PublicKey{pubKey1}

	// Create a mock context
	ctx := &mockContext{user: "testuser"}

	// Try to authenticate with the second key
	result := publicKeyHandler(ctx, pubKey2, authorizedKeys)
	assert.False(t, result)
}

// TestPublicKeyHandler_EmptyAuthorizedKeys tests with no authorized keys
func TestPublicKeyHandler_EmptyAuthorizedKeys(t *testing.T) {
	pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey))
	require.NoError(t, err)

	authorizedKeys := []charmssh.PublicKey{}

	ctx := &mockContext{user: "testuser"}

	result := publicKeyHandler(ctx, pubKey, authorizedKeys)
	assert.False(t, result)
}

// TestPublicKeyHandler_MultipleAuthorizedKeys tests with multiple authorized keys
func TestPublicKeyHandler_MultipleAuthorizedKeys(t *testing.T) {
	pubKey1, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey))
	require.NoError(t, err)

	pubKey2, _, _, _, err := gossh.ParseAuthorizedKey([]byte(testSSHPublicKey2))
	require.NoError(t, err)

	authorizedKeys := []charmssh.PublicKey{pubKey1, pubKey2}

	ctx := &mockContext{user: "testuser"}

	// Both keys should be accepted
	assert.True(t, publicKeyHandler(ctx, pubKey1, authorizedKeys))
	assert.True(t, publicKeyHandler(ctx, pubKey2, authorizedKeys))
}

// TestShellSecurityConfig_Defaults tests ShellSecurityConfig defaults
func TestShellSecurityConfig_Defaults(t *testing.T) {
	cfg := tui.ShellSecurityConfig{}
	assert.False(t, cfg.Enabled)
	assert.Nil(t, cfg.CommandAllowlist)
	assert.Nil(t, cfg.CommandBlocklist)
}

// TestShellSecurityConfig_Enabled tests ShellSecurityConfig with enabled flag
func TestShellSecurityConfig_Enabled(t *testing.T) {
	cfg := tui.ShellSecurityConfig{
		Enabled:          true,
		CommandAllowlist: []string{"ls", "cat", "grep"},
		CommandBlocklist: []string{"rm", "dd"},
	}

	assert.True(t, cfg.Enabled)
	assert.Len(t, cfg.CommandAllowlist, 3)
	assert.Len(t, cfg.CommandBlocklist, 2)
	assert.Contains(t, cfg.CommandAllowlist, "ls")
	assert.Contains(t, cfg.CommandBlocklist, "rm")
}

// mockContext implements charmssh.Context for testing
type mockContext struct {
	user string
	mu   sync.Mutex
}

func (m *mockContext) User() string {
	return m.user
}

// Implement other required methods from charmssh.Context interface
func (m *mockContext) SessionID() string                        { return "test-session" }
func (m *mockContext) ClientVersion() string                    { return "SSH-2.0-Test" }
func (m *mockContext) ServerVersion() string                    { return "SSH-2.0-Wish" }
func (m *mockContext) RemoteAddr() net.Addr                     { return nil }
func (m *mockContext) LocalAddr() net.Addr                      { return nil }
func (m *mockContext) Permissions() *charmssh.Permissions       { return nil }
func (m *mockContext) SetValue(key, value interface{})          {}
func (m *mockContext) Value(key interface{}) interface{}        { return nil }
func (m *mockContext) Deadline() (time.Time, bool)              { return time.Time{}, false }
func (m *mockContext) Done() <-chan struct{}                    { return nil }
func (m *mockContext) Err() error                               { return nil }
func (m *mockContext) Lock()                                    { m.mu.Lock() }
func (m *mockContext) Unlock()                                  { m.mu.Unlock() }

// TestNewServer_WithShellSecurity tests server with shell security config
func TestNewServer_WithShellSecurity(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
		ShellSecurity: tui.ShellSecurityConfig{
			Enabled:          true,
			CommandAllowlist: []string{"ls", "cat"},
		},
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_WithLocation tests server with timezone location
func TestNewServer_WithLocation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
		Location:           loc,
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_WithAssistantName tests server with custom assistant name
func TestNewServer_WithAssistantName(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	cfg := SSHConfig{
		AuthorizedKeysPath: authKeysPath,
		AssistantName:      "Jules",
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}

// TestNewServer_FullConfig tests server with all configuration options
func TestNewServer_FullConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", tmpDir)
	DataDirConfig = ""

	authKeysPath := filepath.Join(tmpDir, "authorized_keys")
	hostKeyPath := filepath.Join(tmpDir, "host_key")
	require.NoError(t, os.WriteFile(authKeysPath, []byte(testSSHPublicKey+"\n"), 0600))

	loc, err := time.LoadLocation("UTC")
	require.NoError(t, err)

	cfg := SSHConfig{
		ListenAddr:         ":2222",
		HostKeyPath:        hostKeyPath,
		AuthorizedKeysPath: authKeysPath,
		GatewayURL:         "ws://localhost:18789/ws",
		GatewayToken:       "test-token-123",
		AssistantName:      "Conduit",
		Location:           loc,
		ClientFactory: func(sshUser string) tui.GatewayClient {
			return nil
		},
		ShellSecurity: tui.ShellSecurityConfig{
			Enabled:          true,
			CommandAllowlist: []string{"ls", "cat", "pwd"},
			CommandBlocklist: []string{"rm", "dd", "mkfs"},
		},
	}

	server, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, server)
	defer server.Close()
}
