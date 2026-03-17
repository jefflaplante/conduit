package ssh

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
)

// mockSSHClient is a mock SSH client for testing
type mockSSHClient struct {
	execFunc   func(command string) (*ExecResult, error)
	healthy    bool
	closed     bool
	execCalled int
}

func (m *mockSSHClient) Exec(command string) (*ExecResult, error) {
	m.execCalled++
	if m.execFunc != nil {
		return m.execFunc(command)
	}
	return &ExecResult{
		Stdout:   "mock output",
		Stderr:   "",
		ExitCode: 0,
	}, nil
}

func (m *mockSSHClient) ExecWithTimeout(command string, timeout time.Duration) (*ExecResult, error) {
	return m.Exec(command)
}

func (m *mockSSHClient) IsHealthy() bool {
	return m.healthy
}

func (m *mockSSHClient) IsClosed() bool {
	return m.closed
}

func (m *mockSSHClient) Close() {
	m.closed = true
}

// setupTestPool creates a test pool with mock hosts
func setupTestPool(t *testing.T, hostCount int) *Pool {
	hosts := make([]config.SSHHostConfig, hostCount)
	for i := 0; i < hostCount; i++ {
		hosts[i] = config.SSHHostConfig{
			Name:     fmt.Sprintf("host-%d", i+1),
			Hostname: fmt.Sprintf("192.168.1.%d", i+1),
			Port:     22,
			User:     "test",
		}
	}

	defaults := config.SSHHostDefaults{
		Port: 22,
		User: "test",
	}

	poolConfig := config.SSHPoolConfig{
		MaxConnectionsPerHost: 5,
		MaxTotalConnections:   50,
		IdleTimeout:           5 * time.Minute,
		ConnectTimeout:        30 * time.Second,
	}

	return NewPool(hosts, defaults, poolConfig)
}

func TestNewFanoutExecutor(t *testing.T) {
	pool := setupTestPool(t, 5)
	defer pool.Close()

	tests := []struct {
		name        string
		maxParallel int
		expected    int
	}{
		{
			name:        "positive max parallel",
			maxParallel: 5,
			expected:    5,
		},
		{
			name:        "zero max parallel defaults to 10",
			maxParallel: 0,
			expected:    10,
		},
		{
			name:        "negative max parallel defaults to 10",
			maxParallel: -1,
			expected:    10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewFanoutExecutor(pool, tt.maxParallel)
			if executor == nil {
				t.Fatal("expected executor, got nil")
			}
			if executor.maxParallel != tt.expected {
				t.Errorf("maxParallel = %d, want %d", executor.maxParallel, tt.expected)
			}
		})
	}
}

func TestFanoutExecutor_Execute_Success(t *testing.T) {
	pool := setupTestPool(t, 3)
	defer pool.Close()

	// We can't easily mock the pool connections, so we'll test the structure
	// In a real test environment, you'd use integration tests with actual SSH
	executor := NewFanoutExecutor(pool, 2)

	ctx := context.Background()
	hosts := []string{"host-1", "host-2", "host-3"}
	command := "echo 'test'"
	timeout := 30 * time.Second

	// This will fail to connect since these are fake hosts
	// but it tests the structure and error handling
	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// Should have results for all hosts (even if they failed to connect)
	if len(result.Results) != len(hosts) {
		t.Errorf("got %d results, want %d", len(result.Results), len(hosts))
	}

	// Check that duration is set
	if result.Duration == 0 {
		t.Error("expected non-zero duration")
	}

	// Check that summary is generated
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestFanoutExecutor_Execute_EmptyHosts(t *testing.T) {
	pool := setupTestPool(t, 3)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 2)

	ctx := context.Background()
	hosts := []string{}
	command := "echo 'test'"
	timeout := 30 * time.Second

	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	if len(result.Results) != 0 {
		t.Errorf("got %d results, want 0", len(result.Results))
	}

	if !strings.Contains(result.Summary, "No hosts") {
		t.Errorf("expected 'No hosts' in summary, got: %s", result.Summary)
	}
}

func TestFanoutExecutor_Execute_ContextCancellation(t *testing.T) {
	pool := setupTestPool(t, 5)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 2)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately
	cancel()

	hosts := []string{"host-1", "host-2", "host-3"}
	command := "echo 'test'"
	timeout := 30 * time.Second

	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// All hosts should have failed due to context cancellation
	if len(result.Failed) != len(hosts) {
		t.Errorf("got %d failed hosts, want %d", len(result.Failed), len(hosts))
	}

	// Check that results contain context cancellation errors
	for _, hostResult := range result.Results {
		if !strings.Contains(hostResult.Error, "context") && !strings.Contains(hostResult.Error, "cancelled") {
			t.Errorf("expected context cancellation error, got: %s", hostResult.Error)
		}
	}
}

func TestFanoutExecutor_Execute_PartialFailure(t *testing.T) {
	pool := setupTestPool(t, 3)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 2)

	ctx := context.Background()
	hosts := []string{"host-1", "nonexistent-host", "host-3"}
	command := "echo 'test'"
	timeout := 30 * time.Second

	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// Should have results for all hosts
	if len(result.Results) != len(hosts) {
		t.Errorf("got %d results, want %d", len(result.Results), len(hosts))
	}

	// At least one host should have failed (the nonexistent one)
	if len(result.Failed) == 0 {
		t.Error("expected at least one failed host")
	}
}

func TestFanoutResult_GenerateSummary(t *testing.T) {
	pool := setupTestPool(t, 5)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 2)

	tests := []struct {
		name       string
		succeeded  []string
		failed     []string
		wantInSummary []string
	}{
		{
			name:      "all succeeded",
			succeeded: []string{"host-1", "host-2", "host-3"},
			failed:    []string{},
			wantInSummary: []string{
				"Executed on 3 host(s)",
				"Succeeded: 3, Failed: 0",
				"Successful hosts: host-1, host-2, host-3",
			},
		},
		{
			name:      "all failed",
			succeeded: []string{},
			failed:    []string{"host-1", "host-2"},
			wantInSummary: []string{
				"Executed on 2 host(s)",
				"Succeeded: 0, Failed: 2",
				"Failed hosts: host-1, host-2",
			},
		},
		{
			name:      "mixed results",
			succeeded: []string{"host-1", "host-3"},
			failed:    []string{"host-2"},
			wantInSummary: []string{
				"Executed on 3 host(s)",
				"Succeeded: 2, Failed: 1",
				"Successful hosts: host-1, host-3",
				"Failed hosts: host-2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &FanoutResult{
				Results:   make(map[string]*ExecutionResult),
				Succeeded: tt.succeeded,
				Failed:    tt.failed,
				Duration:  100 * time.Millisecond,
			}

			// Add execution results for failed hosts
			for _, host := range tt.failed {
				result.Results[host] = &ExecutionResult{
					Host:     host,
					ExitCode: 1,
					Error:    "connection failed",
				}
			}

			summary := executor.generateSummary(result, len(tt.succeeded)+len(tt.failed))

			for _, want := range tt.wantInSummary {
				if !strings.Contains(summary, want) {
					t.Errorf("summary missing expected content: %q\nGot: %s", want, summary)
				}
			}
		})
	}
}

func TestFanoutExecutor_FormatResults(t *testing.T) {
	pool := setupTestPool(t, 3)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 2)

	result := &FanoutResult{
		Results: map[string]*ExecutionResult{
			"host-1": {
				Host:     "host-1",
				Command:  "uptime",
				ExitCode: 0,
				Stdout:   "up 5 days",
				Stderr:   "",
				Duration: 50 * time.Millisecond,
			},
			"host-2": {
				Host:     "host-2",
				Command:  "uptime",
				ExitCode: 1,
				Stdout:   "",
				Stderr:   "command not found",
				Duration: 20 * time.Millisecond,
				Error:    "execution failed",
			},
		},
		Succeeded: []string{"host-1"},
		Failed:    []string{"host-2"},
		Duration:  100 * time.Millisecond,
		Summary:   "Executed on 2 host(s) in 100ms\nSucceeded: 1, Failed: 1\n",
	}

	t.Run("format with output", func(t *testing.T) {
		formatted := executor.FormatResults(result, true)

		// Check that summary is included
		if !strings.Contains(formatted, "Executed on 2 host(s)") {
			t.Error("formatted output missing summary")
		}

		// Check that detailed results are included
		if !strings.Contains(formatted, "[host-1]") {
			t.Error("formatted output missing host-1 details")
		}

		if !strings.Contains(formatted, "[host-2]") {
			t.Error("formatted output missing host-2 details")
		}

		// Check stdout/stderr are included
		if !strings.Contains(formatted, "up 5 days") {
			t.Error("formatted output missing stdout")
		}

		if !strings.Contains(formatted, "command not found") {
			t.Error("formatted output missing stderr")
		}
	})

	t.Run("format without output", func(t *testing.T) {
		formatted := executor.FormatResults(result, false)

		// Check that summary is included
		if !strings.Contains(formatted, "Executed on 2 host(s)") {
			t.Error("formatted output missing summary")
		}

		// Check that detailed results are NOT included
		if strings.Contains(formatted, "--- Detailed Results ---") {
			t.Error("formatted output should not include detailed results")
		}
	})
}

func TestFanoutExecutor_Execute_Timeout(t *testing.T) {
	pool := setupTestPool(t, 2)
	defer pool.Close()

	executor := NewFanoutExecutor(pool, 1)

	// Use a very short timeout to ensure timeout occurs
	ctx := context.Background()
	hosts := []string{"host-1", "host-2"}
	command := "sleep 10"
	timeout := 1 * time.Millisecond

	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// All hosts should have failed or timed out
	// (they'll fail to connect in this test, but structure is tested)
	if len(result.Results) != len(hosts) {
		t.Errorf("got %d results, want %d", len(result.Results), len(hosts))
	}
}

func TestFanoutExecutor_Execute_BoundedParallelism(t *testing.T) {
	pool := setupTestPool(t, 10)
	defer pool.Close()

	// Test that max parallel limit is respected
	maxParallel := 3
	executor := NewFanoutExecutor(pool, maxParallel)

	if executor.maxParallel != maxParallel {
		t.Errorf("maxParallel = %d, want %d", executor.maxParallel, maxParallel)
	}

	ctx := context.Background()
	hosts := []string{"host-1", "host-2", "host-3", "host-4", "host-5"}
	command := "echo test"
	timeout := 30 * time.Second

	// Execute (will fail to connect, but tests structure)
	result := executor.Execute(ctx, hosts, command, timeout)

	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// Should have attempted all hosts
	if len(result.Results) != len(hosts) {
		t.Errorf("got %d results, want %d", len(result.Results), len(hosts))
	}
}

func TestIsMoreRestrictive(t *testing.T) {
	tests := []struct {
		tier1    string
		tier2    string
		expected bool
	}{
		{"blocked", "dangerous", true},
		{"dangerous", "modify", true},
		{"modify", "read", true},
		{"read", "read", false},
		{"read", "modify", false},
		{"modify", "dangerous", false},
		{"dangerous", "blocked", false},
		{"blocked", "read", true},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.tier1, tt.tier2), func(t *testing.T) {
			result := isMoreRestrictive(tt.tier1, tt.tier2)
			if result != tt.expected {
				t.Errorf("isMoreRestrictive(%q, %q) = %v, want %v",
					tt.tier1, tt.tier2, result, tt.expected)
			}
		})
	}
}
