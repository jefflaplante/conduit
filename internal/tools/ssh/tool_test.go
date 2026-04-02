package ssh

import (
	"context"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// mockClient implements the Client interface for testing
type mockClient struct {
	executeFunc       func(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error)
	getPoolStatusFunc func() *PoolStatus
}

func (m *mockClient) Execute(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, host, command, timeout)
	}
	return &ExecutionResult{
		Host:     host,
		Command:  command,
		ExitCode: 0,
		Stdout:   "mock output",
		Duration: time.Millisecond * 100,
	}, nil
}

func (m *mockClient) GetPoolStatus() *PoolStatus {
	if m.getPoolStatusFunc != nil {
		return m.getPoolStatusFunc()
	}
	return &PoolStatus{
		TotalConnections:  5,
		ActiveConnections: 2,
		IdleConnections:   3,
		HostStats:         map[string]int{"test-host": 2},
	}
}

func (m *mockClient) Close() error {
	return nil
}

// testSSHConfig creates a test configuration
func testSSHConfig() *config.RemoteSSHConfig {
	enabled := true
	return &config.RemoteSSHConfig{
		Enabled: true,
		Hosts: []config.SSHHostConfig{
			{
				Name:     "test-host",
				Hostname: "192.168.1.100",
				Port:     22,
				User:     "admin",
				Enabled:  &enabled,
			},
			{
				Name:         "prod-web",
				Hostname:     "10.0.0.1",
				Port:         22,
				User:         "deploy",
				SecurityTier: "read",
				Groups:       []string{"production", "web"},
				Enabled:      &enabled,
			},
			{
				Name:     "disabled-host",
				Hostname: "192.168.1.200",
				Enabled:  func() *bool { b := false; return &b }(),
			},
		},
		HostGroups: []config.SSHHostGroup{
			{
				Name:        "production",
				Description: "Production servers",
			},
		},
		Security: config.SSHSecurityConfig{
			DefaultTier:     "dangerous",
			RequireApproval: []string{"dangerous", "blocked"},
			AllowSubshells:  false,
			AllowPipes:      true,
			AllowedCommands: config.SSHCommandTiers{
				Read: []string{
					"ls", "cat", "head", "tail", "grep", "ps", "df", "free", "uptime", "whoami",
				},
				Modify: []string{
					"touch", "mkdir", "cp", "mv",
				},
				Dangerous: []string{
					"rm", "kill", "systemctl",
				},
				Blocked: []string{
					"rm -rf /", "shutdown", "reboot",
				},
			},
			BlockedPatterns: []string{
				`rm\s+(-[rf]+\s+)*/$`,
			},
		},
		Defaults: config.SSHHostDefaults{
			Port: 22,
			User: "root",
		},
	}
}

func TestNewSSHTool(t *testing.T) {
	cfg := testSSHConfig()
	services := &types.ToolServices{}

	tool, err := NewSSHTool(services, cfg)
	if err != nil {
		t.Fatalf("NewSSHTool() error = %v", err)
	}

	if tool == nil {
		t.Fatal("NewSSHTool() returned nil")
	}

	if tool.Name() != "Ssh" {
		t.Errorf("Name() = %s, want Ssh", tool.Name())
	}
}

func TestNewSSHTool_DefaultConfig(t *testing.T) {
	services := &types.ToolServices{}

	tool, err := NewSSHTool(services, nil)
	if err != nil {
		t.Fatalf("NewSSHTool() with nil config error = %v", err)
	}

	if tool == nil {
		t.Fatal("NewSSHTool() returned nil")
	}

	// Default config has SSH disabled
	if tool.config.Enabled {
		t.Error("Default config should have SSH disabled")
	}
}

func TestSSHTool_Name(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	if tool.Name() != "Ssh" {
		t.Errorf("Name() = %s, want Ssh", tool.Name())
	}
}

func TestSSHTool_Description(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() returned empty string")
	}

	// Check for key information
	expectedPhrases := []string{"exec", "hosts", "status", "SSH"}
	for _, phrase := range expectedPhrases {
		if !contains(desc, phrase) {
			t.Errorf("Description() missing %q", phrase)
		}
	}
}

func TestSSHTool_Parameters(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	params := tool.Parameters()

	if params == nil {
		t.Fatal("Parameters() returned nil")
	}

	// Check required properties exist
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() missing properties")
	}

	requiredParams := []string{"action", "host", "command", "timeout"}
	for _, param := range requiredParams {
		if _, exists := props[param]; !exists {
			t.Errorf("Parameters() missing %s", param)
		}
	}
}

func TestSSHTool_Execute_DisabledSSH(t *testing.T) {
	cfg := testSSHConfig()
	cfg.Enabled = false
	tool, _ := NewSSHTool(&types.ToolServices{}, cfg)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "hosts",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail when SSH is disabled")
	}

	if !contains(result.Error, "disabled") {
		t.Errorf("Error should mention disabled, got: %s", result.Error)
	}
}

func TestSSHTool_Execute_InvalidAction(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid action")
	}

	if !contains(result.Error, "unknown action") {
		t.Errorf("Error should mention unknown action, got: %s", result.Error)
	}
}

func TestSSHTool_ListHosts(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "hosts",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	// Check content includes host names
	if !contains(result.Content, "test-host") {
		t.Error("Content should include test-host")
	}

	if !contains(result.Content, "prod-web") {
		t.Error("Content should include prod-web")
	}

	// Disabled hosts should still be in config but marked
	data, ok := result.Data["hosts"].([]map[string]interface{})
	if !ok {
		t.Fatal("Data should contain hosts array")
	}

	// Should only show enabled hosts (2 of 3)
	if len(data) != 2 {
		t.Errorf("Should have 2 enabled hosts, got %d", len(data))
	}
}

func TestSSHTool_ListHosts_Empty(t *testing.T) {
	cfg := testSSHConfig()
	cfg.Hosts = []config.SSHHostConfig{}
	tool, _ := NewSSHTool(&types.ToolServices{}, cfg)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "hosts",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	if !contains(result.Content, "No SSH hosts configured") {
		t.Error("Content should indicate no hosts configured")
	}
}

func TestSSHTool_GetStatus(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	// Check content includes security configuration
	if !contains(result.Content, "Default tier") {
		t.Error("Content should include security tier info")
	}

	// Check data structure
	if result.Data["enabled"] != true {
		t.Error("Data should include enabled status")
	}

	if result.Data["client_ready"] != false {
		t.Error("Data should indicate client not ready (no client set)")
	}
}

func TestSSHTool_GetStatus_WithClient(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	tool.SetClient(&mockClient{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	if result.Data["client_ready"] != true {
		t.Error("Data should indicate client ready")
	}

	pool, ok := result.Data["pool"].(map[string]interface{})
	if !ok {
		t.Fatal("Data should include pool stats")
	}

	if pool["total_connections"] != 5 {
		t.Errorf("Pool total_connections = %v, want 5", pool["total_connections"])
	}
}

func TestSSHTool_Exec_MissingHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"command": "ls -la",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without host")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "host" {
		t.Errorf("ErrorDetails.Parameter = %s, want host", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_Exec_MissingCommand(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "exec",
		"host":   "test-host",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without command")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "command" {
		t.Errorf("ErrorDetails.Parameter = %s, want command", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_Exec_InvalidHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "nonexistent-host",
		"command": "ls",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid host")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	// Should suggest available hosts
	if len(result.ErrorDetails.AvailableValues) == 0 {
		t.Error("Should suggest available hosts")
	}
}

func TestSSHTool_Exec_BlockedCommand(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "test-host",
		"command": "rm -rf /",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with blocked command")
	}

	if !contains(result.Error, "blocked") {
		t.Errorf("Error should mention blocked, got: %s", result.Error)
	}

	// Check classification data
	tier, ok := result.Data["tier"].(string)
	if !ok || tier != "blocked" {
		t.Errorf("Data tier = %v, want blocked", result.Data["tier"])
	}
}

func TestSSHTool_Exec_DangerousCommand_RequiresApproval(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "test-host",
		"command": "rm file.txt",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail for dangerous command requiring approval")
	}

	if !contains(result.Error, "requires approval") {
		t.Errorf("Error should mention approval requirement, got: %s", result.Error)
	}

	// Check that approval is required
	if result.Data["requires_approval"] != true {
		t.Error("Data should indicate approval required")
	}
}

func TestSSHTool_Exec_ReadCommand_NoClient(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "test-host",
		"command": "ls -la",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should succeed but indicate client not connected
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	if result.Data["client_ready"] != false {
		t.Error("Data should indicate client not ready")
	}

	if result.Data["tier"] != "read" {
		t.Errorf("Data tier = %v, want read", result.Data["tier"])
	}
}

func TestSSHTool_Exec_ReadCommand_WithClient(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	client := &mockClient{
		executeFunc: func(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error) {
			return &ExecutionResult{
				Host:     host,
				Command:  command,
				ExitCode: 0,
				Stdout:   "file1.txt\nfile2.txt\n",
				Duration: 50 * time.Millisecond,
			}, nil
		},
	}
	tool.SetClient(client)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "test-host",
		"command": "ls",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	// Check stdout is included
	if !contains(result.Content, "file1.txt") {
		t.Error("Content should include command output")
	}

	// Check data
	if result.Data["exit_code"] != 0 {
		t.Errorf("Data exit_code = %v, want 0", result.Data["exit_code"])
	}
}

func TestSSHTool_Exec_HostSecurityTier(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	// prod-web has security_tier: "read", so modify commands should be blocked
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "prod-web",
		"command": "touch newfile.txt",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail when command tier exceeds host tier")
	}

	if !contains(result.Error, "blocked") {
		t.Errorf("Error should mention blocked, got: %s", result.Error)
	}
}

func TestSSHTool_Exec_SubshellBlocked(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"host":    "test-host",
		"command": "echo $(whoami)",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with subshell when not allowed")
	}

	if !contains(result.Error, "blocked") {
		t.Errorf("Error should mention blocked, got: %s", result.Error)
	}
}

func TestSSHTool_ValidateParameters(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	tests := []struct {
		name      string
		args      map[string]interface{}
		wantValid bool
		wantParam string
	}{
		{
			name:      "valid hosts action",
			args:      map[string]interface{}{"action": "hosts"},
			wantValid: true,
		},
		{
			name:      "valid status action",
			args:      map[string]interface{}{"action": "status"},
			wantValid: true,
		},
		{
			name:      "invalid action",
			args:      map[string]interface{}{"action": "invalid"},
			wantValid: false,
			wantParam: "action",
		},
		{
			name:      "exec missing host",
			args:      map[string]interface{}{"action": "exec", "command": "ls"},
			wantValid: false,
			wantParam: "host",
		},
		{
			name:      "exec missing command",
			args:      map[string]interface{}{"action": "exec", "host": "test-host"},
			wantValid: false,
			wantParam: "command",
		},
		{
			name:      "exec invalid host",
			args:      map[string]interface{}{"action": "exec", "host": "nonexistent", "command": "ls"},
			wantValid: false,
			wantParam: "host",
		},
		{
			name:      "valid exec",
			args:      map[string]interface{}{"action": "exec", "host": "test-host", "command": "ls"},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.ValidateParameters(context.Background(), tt.args)

			if result.Valid != tt.wantValid {
				t.Errorf("ValidateParameters() valid = %v, want %v", result.Valid, tt.wantValid)
			}

			if !tt.wantValid && tt.wantParam != "" {
				if len(result.Errors) == 0 {
					t.Error("Expected validation errors")
				} else if result.Errors[0].Parameter != tt.wantParam {
					t.Errorf("Error parameter = %s, want %s", result.Errors[0].Parameter, tt.wantParam)
				}
			}
		})
	}
}

func TestSSHTool_GetUsageExamples(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	examples := tool.GetUsageExamples()

	if len(examples) == 0 {
		t.Error("GetUsageExamples() returned no examples")
	}

	// Check that examples cover all actions
	actions := make(map[string]bool)
	for _, ex := range examples {
		if action, ok := ex.Args["action"].(string); ok {
			actions[action] = true
		}
	}

	expectedActions := []string{"exec", "hosts", "status", "session_start", "session_send", "session_close", "session_list", "tunnel_create", "tunnel_list", "tunnel_close"}
	for _, action := range expectedActions {
		if !actions[action] {
			t.Errorf("GetUsageExamples() missing example for action %s", action)
		}
	}
}

func TestSSHTool_SetClient(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	if tool.client != nil {
		t.Error("client should be nil initially")
	}

	client := &mockClient{}
	tool.SetClient(client)

	if tool.client == nil {
		t.Error("client should be set after SetClient")
	}
}

// === Session Action Tests ===

func TestSSHTool_SessionList_Empty(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "session_list",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	if !contains(result.Content, "No active") {
		t.Error("Content should indicate no active sessions")
	}

	if result.Data["count"] != 0 {
		t.Errorf("count = %v, want 0", result.Data["count"])
	}
}

func TestSSHTool_SessionStart_MissingHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "session_start",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without host")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "host" {
		t.Errorf("ErrorDetails.Parameter = %s, want host", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_SessionStart_InvalidHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "session_start",
		"host":   "nonexistent-host",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid host")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	// Should suggest available hosts
	if len(result.ErrorDetails.AvailableValues) == 0 {
		t.Error("Should suggest available hosts")
	}
}

func TestSSHTool_SessionStart_DisabledHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "session_start",
		"host":   "disabled-host",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with disabled host")
	}

	if !contains(result.Error, "disabled") {
		t.Errorf("Error should mention disabled, got: %s", result.Error)
	}
}

func TestSSHTool_SessionSend_MissingSessionID(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "session_send",
		"command": "ls",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without session_id")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "session_id" {
		t.Errorf("ErrorDetails.Parameter = %s, want session_id", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_SessionSend_MissingCommand(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "session_send",
		"session_id": "test-session",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without command")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "command" {
		t.Errorf("ErrorDetails.Parameter = %s, want command", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_SessionSend_InvalidSession(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "session_send",
		"session_id": "nonexistent-session",
		"command":    "ls",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid session")
	}

	if !contains(result.Error, "not found") {
		t.Errorf("Error should mention not found, got: %s", result.Error)
	}
}

func TestSSHTool_SessionClose_MissingSessionID(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "session_close",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without session_id")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if result.ErrorDetails.Parameter != "session_id" {
		t.Errorf("ErrorDetails.Parameter = %s, want session_id", result.ErrorDetails.Parameter)
	}
}

func TestSSHTool_SessionClose_InvalidSession(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "session_close",
		"session_id": "nonexistent-session",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid session")
	}

	if !contains(result.Error, "not found") {
		t.Errorf("Error should mention not found, got: %s", result.Error)
	}
}

func TestSSHTool_SessionValidateParameters(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	tests := []struct {
		name      string
		args      map[string]interface{}
		wantValid bool
		wantParam string
	}{
		{
			name:      "valid session_list action",
			args:      map[string]interface{}{"action": "session_list"},
			wantValid: true,
		},
		{
			name:      "session_start missing host",
			args:      map[string]interface{}{"action": "session_start"},
			wantValid: false,
			wantParam: "host",
		},
		{
			name:      "session_start invalid host",
			args:      map[string]interface{}{"action": "session_start", "host": "nonexistent"},
			wantValid: false,
			wantParam: "host",
		},
		{
			name:      "session_send missing session_id",
			args:      map[string]interface{}{"action": "session_send", "command": "ls"},
			wantValid: false,
			wantParam: "session_id",
		},
		{
			name:      "session_send missing command",
			args:      map[string]interface{}{"action": "session_send", "session_id": "test"},
			wantValid: false,
			wantParam: "command",
		},
		{
			name:      "session_close missing session_id",
			args:      map[string]interface{}{"action": "session_close"},
			wantValid: false,
			wantParam: "session_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.ValidateParameters(context.Background(), tt.args)

			if result.Valid != tt.wantValid {
				t.Errorf("ValidateParameters() valid = %v, want %v", result.Valid, tt.wantValid)
			}

			if !tt.wantValid && tt.wantParam != "" {
				if len(result.Errors) == 0 {
					t.Error("Expected validation errors")
				} else if result.Errors[0].Parameter != tt.wantParam {
					t.Errorf("Error parameter = %s, want %s", result.Errors[0].Parameter, tt.wantParam)
				}
			}
		})
	}
}

func TestSSHTool_GetStatus_WithSessionInfo(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	// Check that session info is included
	if !contains(result.Content, "Persistent Sessions") {
		t.Error("Content should include session info")
	}

	// Check data structure has session info
	sessions, ok := result.Data["sessions"].(map[string]interface{})
	if !ok {
		t.Fatal("Data should contain sessions map")
	}

	if sessions["active"] != 0 {
		t.Errorf("sessions.active = %v, want 0", sessions["active"])
	}

	if sessions["max_sessions"] != 5 {
		t.Errorf("sessions.max_sessions = %v, want 5", sessions["max_sessions"])
	}
}

func TestSSHTool_Close(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	// Close should not panic
	tool.Close()

	// Double close should be safe
	tool.Close()
}

func TestSSHTool_GetSessionManager(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	defer tool.Close()

	sm := tool.GetSessionManager()
	if sm == nil {
		t.Error("GetSessionManager() should return session manager")
	}
}

// === Tunnel Action Tests ===

func TestSSHTool_TunnelList_Empty(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "tunnel_list",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	if !contains(result.Content, "No active tunnels") {
		t.Error("Content should indicate no active tunnels")
	}

	count, ok := result.Data["count"].(int)
	if !ok || count != 0 {
		t.Errorf("Data count = %v, want 0", result.Data["count"])
	}
}

func TestSSHTool_TunnelCreate_NoClient(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "tunnel_create",
		"host":        "test-host",
		"local_port":  0,
		"remote_host": "localhost",
		"remote_port": 3306,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail when client is not connected")
	}

	if !contains(result.Error, "not connected") {
		t.Errorf("Error should mention not connected, got: %s", result.Error)
	}
}

func TestSSHTool_TunnelCreate_MissingHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "tunnel_create",
		"remote_host": "localhost",
		"remote_port": 3306,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without host")
	}

	if result.ErrorDetails == nil || result.ErrorDetails.Parameter != "host" {
		t.Error("ErrorDetails should indicate missing host parameter")
	}
}

func TestSSHTool_TunnelCreate_MissingRemoteHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "tunnel_create",
		"host":        "test-host",
		"remote_port": 3306,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without remote_host")
	}

	if result.ErrorDetails == nil || result.ErrorDetails.Parameter != "remote_host" {
		t.Error("ErrorDetails should indicate missing remote_host parameter")
	}
}

func TestSSHTool_TunnelCreate_MissingRemotePort(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "tunnel_create",
		"host":        "test-host",
		"remote_host": "localhost",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without remote_port")
	}

	if result.ErrorDetails == nil || result.ErrorDetails.Parameter != "remote_port" {
		t.Error("ErrorDetails should indicate missing remote_port parameter")
	}
}

func TestSSHTool_TunnelCreate_InvalidHost(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "tunnel_create",
		"host":        "nonexistent-host",
		"remote_host": "localhost",
		"remote_port": 3306,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with invalid host")
	}

	if result.ErrorDetails == nil {
		t.Error("Should have error details")
	}

	if len(result.ErrorDetails.AvailableValues) == 0 {
		t.Error("Should suggest available hosts")
	}
}

func TestSSHTool_TunnelClose_MissingID(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "tunnel_close",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail without tunnel_id")
	}

	if result.ErrorDetails == nil || result.ErrorDetails.Parameter != "tunnel_id" {
		t.Error("ErrorDetails should indicate missing tunnel_id parameter")
	}
}

func TestSSHTool_TunnelClose_NotFound(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "tunnel_close",
		"tunnel_id": "nonexistent-tunnel-id",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Success {
		t.Error("Execute() should fail with nonexistent tunnel")
	}

	if !contains(result.Error, "not found") {
		t.Errorf("Error should mention not found, got: %s", result.Error)
	}
}

func TestSSHTool_ValidateParameters_TunnelCreate(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	tests := []struct {
		name      string
		args      map[string]interface{}
		wantValid bool
		wantParam string
	}{
		{
			name: "tunnel_create missing host",
			args: map[string]interface{}{
				"action":      "tunnel_create",
				"remote_host": "localhost",
				"remote_port": 3306,
			},
			wantValid: false,
			wantParam: "host",
		},
		{
			name: "tunnel_create missing remote_host",
			args: map[string]interface{}{
				"action":      "tunnel_create",
				"host":        "test-host",
				"remote_port": 3306,
			},
			wantValid: false,
			wantParam: "remote_host",
		},
		{
			name: "tunnel_create missing remote_port",
			args: map[string]interface{}{
				"action":      "tunnel_create",
				"host":        "test-host",
				"remote_host": "localhost",
			},
			wantValid: false,
			wantParam: "remote_port",
		},
		{
			name: "tunnel_create invalid host",
			args: map[string]interface{}{
				"action":      "tunnel_create",
				"host":        "nonexistent",
				"remote_host": "localhost",
				"remote_port": 3306,
			},
			wantValid: false,
			wantParam: "host",
		},
		{
			name: "tunnel_close missing tunnel_id",
			args: map[string]interface{}{
				"action": "tunnel_close",
			},
			wantValid: false,
			wantParam: "tunnel_id",
		},
		{
			name: "tunnel_list valid",
			args: map[string]interface{}{
				"action": "tunnel_list",
			},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.ValidateParameters(context.Background(), tt.args)

			if result.Valid != tt.wantValid {
				t.Errorf("ValidateParameters() valid = %v, want %v", result.Valid, tt.wantValid)
			}

			if !tt.wantValid && tt.wantParam != "" {
				if len(result.Errors) == 0 {
					t.Error("Expected validation errors")
				} else if result.Errors[0].Parameter != tt.wantParam {
					t.Errorf("Error parameter = %s, want %s", result.Errors[0].Parameter, tt.wantParam)
				}
			}
		})
	}
}

func TestSSHTool_GetTunnelManager(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	tm := tool.GetTunnelManager()
	if tm == nil {
		t.Error("GetTunnelManager() should return tunnel manager")
	}
}

func TestSSHTool_GetStatus_WithTunnelInfo(t *testing.T) {
	tool, _ := NewSSHTool(&types.ToolServices{}, testSSHConfig())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}

	// Check that tunnel info is included
	if !contains(result.Content, "Active Tunnels") {
		t.Error("Content should include tunnel info")
	}

	// Check data structure has tunnel info
	tunnels, ok := result.Data["tunnels"].(map[string]interface{})
	if !ok {
		t.Fatal("Data should contain tunnels map")
	}

	if tunnels["active"] != 0 {
		t.Errorf("tunnels.active = %v, want 0", tunnels["active"])
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
