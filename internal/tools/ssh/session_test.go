//go:build with_ssh

package ssh

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// testSessionConfig creates a test session configuration
func testSessionConfig() config.SSHSessionConfig {
	return config.SSHSessionConfig{
		MaxConcurrentSessions: 5,
		SessionIdleTimeout:    10 * time.Minute,
		DefaultShell:          "/bin/sh",
		OutputBoundaryMarker:  "___TEST_BOUNDARY___",
	}
}

// testHostsConfig creates test host configurations
func testHostsConfig() []config.SSHHostConfig {
	enabled := true
	disabled := false
	return []config.SSHHostConfig{
		{
			Name:     "test-host-1",
			Hostname: "192.168.1.100",
			Port:     22,
			User:     "admin",
			Enabled:  &enabled,
		},
		{
			Name:     "test-host-2",
			Hostname: "192.168.1.101",
			Port:     22,
			User:     "admin",
			Enabled:  &enabled,
		},
		{
			Name:     "disabled-host",
			Hostname: "192.168.1.200",
			Enabled:  &disabled,
		},
	}
}

func TestNewSessionManager(t *testing.T) {
	cfg := testSessionConfig()
	hosts := testHostsConfig()
	defaults := config.SSHHostDefaults{Port: 22, User: "root"}
	poolConfig := config.SSHPoolConfig{}

	sm := NewSessionManager(cfg, hosts, defaults, poolConfig)
	if sm == nil {
		t.Fatal("NewSessionManager() returned nil")
	}
	defer sm.Close()

	if sm.maxSessions != 5 {
		t.Errorf("maxSessions = %d, want 5", sm.maxSessions)
	}

	if sm.idleTimeout != 10*time.Minute {
		t.Errorf("idleTimeout = %v, want 10m", sm.idleTimeout)
	}

	if sm.marker != "___TEST_BOUNDARY___" {
		t.Errorf("marker = %s, want ___TEST_BOUNDARY___", sm.marker)
	}

	if sm.shell != "/bin/sh" {
		t.Errorf("shell = %s, want /bin/sh", sm.shell)
	}

	// Check hosts were loaded
	if len(sm.hosts) != 3 {
		t.Errorf("hosts count = %d, want 3", len(sm.hosts))
	}
}

func TestNewSessionManager_Defaults(t *testing.T) {
	cfg := config.SSHSessionConfig{} // Empty config, should use defaults
	hosts := testHostsConfig()
	defaults := config.SSHHostDefaults{}
	poolConfig := config.SSHPoolConfig{}

	sm := NewSessionManager(cfg, hosts, defaults, poolConfig)
	if sm == nil {
		t.Fatal("NewSessionManager() returned nil")
	}
	defer sm.Close()

	if sm.maxSessions != 5 {
		t.Errorf("default maxSessions = %d, want 5", sm.maxSessions)
	}

	if sm.idleTimeout != 10*time.Minute {
		t.Errorf("default idleTimeout = %v, want 10m", sm.idleTimeout)
	}

	if sm.marker != "___CONDUIT_OUTPUT_BOUNDARY___" {
		t.Errorf("default marker = %s, want ___CONDUIT_OUTPUT_BOUNDARY___", sm.marker)
	}

	if sm.shell != "/bin/sh" {
		t.Errorf("default shell = %s, want /bin/sh", sm.shell)
	}
}

func TestSessionManager_ListSessions_Empty(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	sessions := sm.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("ListSessions() on empty manager = %d sessions, want 0", len(sessions))
	}
}

func TestSessionManager_SessionCount(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	count := sm.SessionCount()
	if count != 0 {
		t.Errorf("SessionCount() on empty manager = %d, want 0", count)
	}
}

func TestSessionManager_StartSession_UnknownHost(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	_, err := sm.StartSession("nonexistent-host")
	if err == nil {
		t.Error("StartSession() with unknown host should return error")
	}

	if !strings.Contains(err.Error(), "unknown host") {
		t.Errorf("Error should mention 'unknown host', got: %s", err.Error())
	}
}

func TestSessionManager_StartSession_DisabledHost(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	_, err := sm.StartSession("disabled-host")
	if err == nil {
		t.Error("StartSession() with disabled host should return error")
	}

	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("Error should mention 'disabled', got: %s", err.Error())
	}
}

func TestSessionManager_SendCommand_InvalidSession(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	_, err := sm.SendCommand("nonexistent-session", "ls", 30*time.Second)
	if err == nil {
		t.Error("SendCommand() with invalid session should return error")
	}

	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("Error should mention 'session not found', got: %s", err.Error())
	}
}

func TestSessionManager_CloseSession_InvalidSession(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	err := sm.CloseSession("nonexistent-session")
	if err == nil {
		t.Error("CloseSession() with invalid session should return error")
	}

	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("Error should mention 'session not found', got: %s", err.Error())
	}
}

func TestSessionManager_GetSession_InvalidSession(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	_, err := sm.GetSession("nonexistent-session")
	if err == nil {
		t.Error("GetSession() with invalid session should return error")
	}

	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("Error should mention 'session not found', got: %s", err.Error())
	}
}

func TestSessionManager_HasSession(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	if sm.HasSession("nonexistent") {
		t.Error("HasSession() should return false for nonexistent session")
	}
}

func TestSessionManager_AddHost(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	enabled := true
	newHost := config.SSHHostConfig{
		Name:     "new-host",
		Hostname: "192.168.1.150",
		Port:     22,
		Enabled:  &enabled,
	}

	err := sm.AddHost(newHost)
	if err != nil {
		t.Fatalf("AddHost() error = %v", err)
	}

	// Verify host was added
	_, ok := sm.GetHostConfig("new-host")
	if !ok {
		t.Error("GetHostConfig() should find newly added host")
	}
}

func TestSessionManager_AddHost_Duplicate(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	enabled := true
	duplicateHost := config.SSHHostConfig{
		Name:     "test-host-1", // Already exists
		Hostname: "192.168.1.150",
		Port:     22,
		Enabled:  &enabled,
	}

	err := sm.AddHost(duplicateHost)
	if err == nil {
		t.Error("AddHost() with duplicate host should return error")
	}

	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Error should mention 'already exists', got: %s", err.Error())
	}
}

func TestSessionManager_GetHostConfig(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	host, ok := sm.GetHostConfig("test-host-1")
	if !ok {
		t.Fatal("GetHostConfig() should find existing host")
	}

	if host.Name != "test-host-1" {
		t.Errorf("host.Name = %s, want test-host-1", host.Name)
	}

	if host.Hostname != "192.168.1.100" {
		t.Errorf("host.Hostname = %s, want 192.168.1.100", host.Hostname)
	}
}

func TestSessionManager_GetHostConfig_NotFound(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	_, ok := sm.GetHostConfig("nonexistent-host")
	if ok {
		t.Error("GetHostConfig() should return false for nonexistent host")
	}
}

func TestSessionManager_ConcurrentAccess(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sm.ListSessions()
			sm.SessionCount()
			sm.HasSession("test")
			sm.GetHostConfig("test-host-1")
		}()
	}

	wg.Wait()
}

func TestSessionInfo_Fields(t *testing.T) {
	now := time.Now()
	info := &SessionInfo{
		ID:           "test-id",
		Host:         "test-host",
		CreatedAt:    now,
		LastUsedAt:   now,
		CommandCount: 5,
	}

	if info.ID != "test-id" {
		t.Errorf("ID = %s, want test-id", info.ID)
	}

	if info.Host != "test-host" {
		t.Errorf("Host = %s, want test-host", info.Host)
	}

	if info.CommandCount != 5 {
		t.Errorf("CommandCount = %d, want 5", info.CommandCount)
	}
}

func TestSessionOutput_Fields(t *testing.T) {
	output := &SessionOutput{
		Stdout:   "hello world",
		Stderr:   "warning message",
		ExitCode: 0,
		Duration: 100 * time.Millisecond,
	}

	if output.Stdout != "hello world" {
		t.Errorf("Stdout = %s, want hello world", output.Stdout)
	}

	if output.Stderr != "warning message" {
		t.Errorf("Stderr = %s, want warning message", output.Stderr)
	}

	if output.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", output.ExitCode)
	}

	if output.Duration != 100*time.Millisecond {
		t.Errorf("Duration = %v, want 100ms", output.Duration)
	}
}

func TestSessionManager_MaxSessions_Limit(t *testing.T) {
	// Create manager with max 2 sessions for testing
	cfg := config.SSHSessionConfig{
		MaxConcurrentSessions: 2,
		SessionIdleTimeout:    10 * time.Minute,
	}
	sm := NewSessionManager(cfg, testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})
	defer sm.Close()

	// Verify the limit is set correctly
	if sm.maxSessions != 2 {
		t.Errorf("maxSessions = %d, want 2", sm.maxSessions)
	}
}

// TestParseBoundaryMarkers tests the boundary marker parsing logic
func TestParseBoundaryMarkers(t *testing.T) {
	marker := "___TEST_BOUNDARY___"

	tests := []struct {
		name            string
		output          string
		startMarker     string
		endMarkerPrefix string
		wantStdout      string
		wantExitCode    int
		wantFound       bool
	}{
		{
			name: "simple output",
			output: `some noise
---START-___TEST_BOUNDARY___-abc123---
hello world
---END-___TEST_BOUNDARY___-abc123---0
more noise`,
			startMarker:     "---START-___TEST_BOUNDARY___-abc123---",
			endMarkerPrefix: "---END-___TEST_BOUNDARY___-abc123---",
			wantStdout:      "hello world",
			wantExitCode:    0,
			wantFound:       true,
		},
		{
			name: "multiline output",
			output: `---START-___TEST_BOUNDARY___-def456---
line 1
line 2
line 3
---END-___TEST_BOUNDARY___-def456---0`,
			startMarker:     "---START-___TEST_BOUNDARY___-def456---",
			endMarkerPrefix: "---END-___TEST_BOUNDARY___-def456---",
			wantStdout:      "line 1\nline 2\nline 3",
			wantExitCode:    0,
			wantFound:       true,
		},
		{
			name: "non-zero exit code",
			output: `---START-___TEST_BOUNDARY___-ghi789---
error occurred
---END-___TEST_BOUNDARY___-ghi789---1`,
			startMarker:     "---START-___TEST_BOUNDARY___-ghi789---",
			endMarkerPrefix: "---END-___TEST_BOUNDARY___-ghi789---",
			wantStdout:      "error occurred",
			wantExitCode:    1,
			wantFound:       true,
		},
		{
			name:            "no markers",
			output:          "just some output without markers",
			startMarker:     "---START-___TEST_BOUNDARY___-xyz---",
			endMarkerPrefix: "---END-___TEST_BOUNDARY___-xyz---",
			wantFound:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, exitCode, found := parseBoundaryOutput(tt.output, tt.startMarker, tt.endMarkerPrefix)

			if found != tt.wantFound {
				t.Errorf("found = %v, want %v", found, tt.wantFound)
			}

			if found {
				if strings.TrimSpace(stdout) != tt.wantStdout {
					t.Errorf("stdout = %q, want %q", strings.TrimSpace(stdout), tt.wantStdout)
				}

				if exitCode != tt.wantExitCode {
					t.Errorf("exitCode = %d, want %d", exitCode, tt.wantExitCode)
				}
			}
		})
	}

	_ = marker // Just to use the variable
}

// parseBoundaryOutput is a helper function to parse boundary-marked output
// This mirrors the logic in waitForOutput for testing purposes
func parseBoundaryOutput(output, startMarker, endMarkerPrefix string) (stdout string, exitCode int, found bool) {
	lines := strings.Split(output, "\n")
	var foundStart bool
	var outputLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if !foundStart {
			if strings.Contains(line, startMarker) {
				foundStart = true
			}
			continue
		}

		if strings.HasPrefix(line, endMarkerPrefix) {
			exitCodeStr := strings.TrimPrefix(line, endMarkerPrefix)
			if exitCodeStr != "" {
				fmt.Sscanf(exitCodeStr, "%d", &exitCode)
			}
			return strings.Join(outputLines, "\n"), exitCode, true
		}

		outputLines = append(outputLines, line)
	}

	return "", 0, false
}

func TestSessionManager_Close(t *testing.T) {
	sm := NewSessionManager(testSessionConfig(), testHostsConfig(), config.SSHHostDefaults{}, config.SSHPoolConfig{})

	// Close should not panic
	sm.Close()

	// Double close should be safe
	sm.Close()
}

func TestPersistentSession_CloseIdempotent(t *testing.T) {
	ps := &PersistentSession{
		id:     "test",
		closed: false,
	}

	// First close
	err := ps.close()
	if err != nil {
		t.Errorf("First close() error = %v", err)
	}

	if !ps.closed {
		t.Error("Session should be marked as closed")
	}

	// Second close should be safe
	err = ps.close()
	if err != nil {
		t.Errorf("Second close() error = %v", err)
	}
}
