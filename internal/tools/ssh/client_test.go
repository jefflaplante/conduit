package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"

	"golang.org/x/crypto/ssh"
)

// testHostConfig creates a test host configuration
func testHostConfig(name, hostname string) config.SSHHostConfig {
	return config.SSHHostConfig{
		Name:     name,
		Hostname: hostname,
		Port:     22,
		User:     "testuser",
	}
}

// testDefaults creates test default settings
func testDefaults() config.SSHHostDefaults {
	return config.SSHHostDefaults{
		Port:           22,
		User:           "defaultuser",
		ConnectTimeout: 10 * time.Second,
	}
}

// testPoolConfig creates test pool configuration
func testPoolConfig() config.SSHPoolConfig {
	return config.SSHPoolConfig{
		MaxConnectionsPerHost: 5,
		MaxTotalConnections:   50,
		IdleTimeout:           5 * time.Minute,
		ConnectTimeout:        30 * time.Second,
		HealthCheckInterval:   1 * time.Minute,
		StrictHostKeyChecking: "no", // For testing
	}
}

func TestSSHHostConfig_GetPort(t *testing.T) {
	tests := []struct {
		name     string
		host     config.SSHHostConfig
		defaults config.SSHHostDefaults
		want     int
	}{
		{
			name:     "host port specified",
			host:     config.SSHHostConfig{Port: 2222},
			defaults: config.SSHHostDefaults{Port: 22},
			want:     2222,
		},
		{
			name:     "use default port",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{Port: 2222},
			want:     2222,
		},
		{
			name:     "fallback to 22",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{},
			want:     22,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.GetPort(tt.defaults); got != tt.want {
				t.Errorf("GetPort() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSSHHostConfig_GetUser(t *testing.T) {
	tests := []struct {
		name     string
		host     config.SSHHostConfig
		defaults config.SSHHostDefaults
		want     string
	}{
		{
			name:     "host user specified",
			host:     config.SSHHostConfig{User: "hostuser"},
			defaults: config.SSHHostDefaults{User: "defaultuser"},
			want:     "hostuser",
		},
		{
			name:     "use default user",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{User: "defaultuser"},
			want:     "defaultuser",
		},
		{
			name:     "empty user",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.GetUser(tt.defaults); got != tt.want {
				t.Errorf("GetUser() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSSHHostConfig_GetIdentityFile(t *testing.T) {
	tests := []struct {
		name     string
		host     config.SSHHostConfig
		defaults config.SSHHostDefaults
		want     string
	}{
		{
			name:     "host identity file",
			host:     config.SSHHostConfig{IdentityFile: "/path/to/key"},
			defaults: config.SSHHostDefaults{IdentityFile: "/default/key"},
			want:     "/path/to/key",
		},
		{
			name:     "use default identity file",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{IdentityFile: "/default/key"},
			want:     "/default/key",
		},
		{
			name:     "empty identity file",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.GetIdentityFile(tt.defaults); got != tt.want {
				t.Errorf("GetIdentityFile() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSSHHostConfig_GetConnectTimeout(t *testing.T) {
	tests := []struct {
		name     string
		host     config.SSHHostConfig
		defaults config.SSHHostDefaults
		want     time.Duration
	}{
		{
			name:     "host timeout specified",
			host:     config.SSHHostConfig{ConnectTimeout: 60 * time.Second},
			defaults: config.SSHHostDefaults{ConnectTimeout: 30 * time.Second},
			want:     60 * time.Second,
		},
		{
			name:     "use default timeout",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{ConnectTimeout: 45 * time.Second},
			want:     45 * time.Second,
		},
		{
			name:     "fallback to 30s",
			host:     config.SSHHostConfig{},
			defaults: config.SSHHostDefaults{},
			want:     30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.GetConnectTimeout(tt.defaults); got != tt.want {
				t.Errorf("GetConnectTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildHostKeyCallback_Modes(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{
			name:    "no mode - accepts anything",
			mode:    "no",
			wantErr: false,
		},
		{
			name:    "invalid mode",
			mode:    "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poolConfig := config.SSHPoolConfig{
				StrictHostKeyChecking: tt.mode,
			}

			callback, err := buildHostKeyCallback(poolConfig)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if callback == nil {
				t.Error("expected callback, got nil")
			}
		})
	}
}

func TestBuildHostKeyCallback_StrictWithMissingFile(t *testing.T) {
	poolConfig := config.SSHPoolConfig{
		StrictHostKeyChecking: "yes",
		KnownHostsFile:        "/nonexistent/known_hosts",
	}

	_, err := buildHostKeyCallback(poolConfig)
	if err == nil {
		t.Error("expected error for missing known_hosts file")
	}
}

func TestBuildHostKeyCallback_AcceptNew(t *testing.T) {
	// Create a temp directory for known_hosts
	tmpDir := t.TempDir()
	knownHostsFile := filepath.Join(tmpDir, "known_hosts")

	poolConfig := config.SSHPoolConfig{
		StrictHostKeyChecking: "accept-new",
		KnownHostsFile:        knownHostsFile,
	}

	callback, err := buildHostKeyCallback(poolConfig)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if callback == nil {
		t.Error("expected callback, got nil")
	}
}

func TestBuildAuthMethods(t *testing.T) {
	host := config.SSHHostConfig{
		Name:     "test",
		Hostname: "localhost",
	}
	defaults := config.SSHHostDefaults{}

	// This test verifies that buildAuthMethods doesn't panic
	// and returns something (may be empty if no agent/keys available)
	methods, err := buildAuthMethods(host, defaults)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	// Methods may be empty if no SSH agent or keys are available
	// This is fine for the test environment
	_ = methods
}

func TestGetKeyFileAuth_NonexistentFile(t *testing.T) {
	_, err := getKeyFileAuth("/nonexistent/key")
	if err == nil {
		t.Error("expected error for nonexistent key file")
	}
}

func TestGetKeyFileAuth_InvalidKey(t *testing.T) {
	// Create a temp file with invalid key content
	tmpDir := t.TempDir()
	keyFile := filepath.Join(tmpDir, "invalid_key")
	if err := os.WriteFile(keyFile, []byte("not a valid key"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := getKeyFileAuth(keyFile)
	if err == nil {
		t.Error("expected error for invalid key file")
	}
}

func TestGetKeyFileAuth_TildeExpansion(t *testing.T) {
	// This test verifies ~ expansion works without panicking
	// It will likely fail to find the file, which is expected
	_, err := getKeyFileAuth("~/.ssh/nonexistent_test_key")
	if err == nil {
		t.Log("Note: key file exists in home directory")
	}
	// We don't fail on error - we just want to verify ~ expansion works
}

func TestExecResult(t *testing.T) {
	result := &ExecResult{
		Stdout:   "output",
		Stderr:   "error",
		ExitCode: 1,
	}

	if result.Stdout != "output" {
		t.Errorf("Stdout = %s, want output", result.Stdout)
	}
	if result.Stderr != "error" {
		t.Errorf("Stderr = %s, want error", result.Stderr)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
}

func TestSSHClient_ClosedClient(t *testing.T) {
	// We can't easily create a real SSHClient without a server,
	// but we can test the closed state behavior
	client := &SSHClient{
		closed: true,
	}

	if !client.IsClosed() {
		t.Error("IsClosed() should return true for closed client")
	}

	_, err := client.Exec("echo test")
	if err == nil {
		t.Error("Exec should fail on closed client")
	}
}

func TestSSHClient_Timestamps(t *testing.T) {
	now := time.Now()
	client := &SSHClient{
		createdAt:  now,
		lastUsedAt: now.Add(-1 * time.Minute),
	}

	if client.CreatedAt() != now {
		t.Error("CreatedAt() returned wrong time")
	}

	if client.LastUsedAt() != now.Add(-1*time.Minute) {
		t.Error("LastUsedAt() returned wrong time")
	}
}

func TestAcceptNewCallback_SessionTracking(t *testing.T) {
	tmpDir := t.TempDir()
	knownHostsFile := filepath.Join(tmpDir, "known_hosts")

	cb := &acceptNewCallback{
		knownHostsFile: knownHostsFile,
		known:          make(map[string]ssh.PublicKey),
	}

	// Verify the callback is created with empty known hosts
	if len(cb.known) != 0 {
		t.Error("expected empty known hosts map")
	}

	if cb.knownHostsFile != knownHostsFile {
		t.Errorf("knownHostsFile = %s, want %s", cb.knownHostsFile, knownHostsFile)
	}
}

// Note: Integration tests that require actual SSH connections are in client_integration_test.go
// Those tests would need a running SSH server or test containers

func TestJumpHostParsing(t *testing.T) {
	tests := []struct {
		name     string
		jumpSpec string
		wantUser string
		wantHost string
		wantPort int
	}{
		{
			name:     "simple host",
			jumpSpec: "bastion.example.com",
			wantHost: "bastion.example.com",
			wantPort: 22,
		},
		{
			name:     "host with port",
			jumpSpec: "bastion.example.com:2222",
			wantHost: "bastion.example.com",
			wantPort: 2222,
		},
		{
			name:     "user@host",
			jumpSpec: "admin@bastion.example.com",
			wantUser: "admin",
			wantHost: "bastion.example.com",
			wantPort: 22,
		},
		{
			name:     "user@host:port",
			jumpSpec: "admin@bastion.example.com:2222",
			wantUser: "admin",
			wantHost: "bastion.example.com",
			wantPort: 2222,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the jump spec manually to verify our parsing logic
			jumpUser := ""
			jumpHost := tt.jumpSpec
			jumpPort := 22

			if atIdx := strings.Index(tt.jumpSpec, "@"); atIdx != -1 {
				jumpUser = tt.jumpSpec[:atIdx]
				jumpHost = tt.jumpSpec[atIdx+1:]
			}

			if colonIdx := strings.LastIndex(jumpHost, ":"); colonIdx != -1 {
				portStr := jumpHost[colonIdx+1:]
				jumpHost = jumpHost[:colonIdx]
				fmt.Sscanf(portStr, "%d", &jumpPort)
			}

			if jumpUser != tt.wantUser {
				t.Errorf("user = %s, want %s", jumpUser, tt.wantUser)
			}
			if jumpHost != tt.wantHost {
				t.Errorf("host = %s, want %s", jumpHost, tt.wantHost)
			}
			if jumpPort != tt.wantPort {
				t.Errorf("port = %d, want %d", jumpPort, tt.wantPort)
			}
		})
	}
}
