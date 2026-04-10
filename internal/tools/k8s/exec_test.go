//go:build with_k8s

package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPodExecutor(t *testing.T) {
	pe := NewPodExecutor()
	require.NotNil(t, pe)
	assert.Equal(t, defaultMaxOutputBytes, pe.maxOutputBytes)
	assert.Equal(t, defaultExecTimeout, pe.defaultTimeout)
}

func TestExecResult_JSON(t *testing.T) {
	result := &ExecResult{
		Stdout:   "hello world\n",
		Stderr:   "warning: something\n",
		ExitCode: 0,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded ExecResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "hello world\n", decoded.Stdout)
	assert.Equal(t, "warning: something\n", decoded.Stderr)
	assert.Equal(t, 0, decoded.ExitCode)
	assert.False(t, decoded.TimedOut)
}

func TestExecResult_JSON_TimedOut(t *testing.T) {
	result := &ExecResult{
		Stdout:   "partial",
		Stderr:   "",
		ExitCode: -1,
		TimedOut: true,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded ExecResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, -1, decoded.ExitCode)
	assert.True(t, decoded.TimedOut)
}

func TestExecResult_JSON_OmitsTimedOutWhenFalse(t *testing.T) {
	result := &ExecResult{
		Stdout:   "ok",
		ExitCode: 0,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	// TimedOut should be omitted from JSON when false.
	assert.NotContains(t, string(data), "timed_out")
}

func TestPodExecutor_ResolveContainer_Explicit(t *testing.T) {
	pe := NewPodExecutor()
	tool := setupTestTool(t)

	client, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)

	// When container is explicitly provided, it should be returned as-is.
	name, err := pe.resolveContainer(context.Background(), client, "web-abc123", "default", "my-container")
	require.NoError(t, err)
	assert.Equal(t, "my-container", name)
}

func TestPodExecutor_ResolveContainer_FirstContainer(t *testing.T) {
	pe := NewPodExecutor()
	tool := setupTestTool(t)

	client, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)

	// When container is empty, should resolve to first container in pod spec.
	name, err := pe.resolveContainer(context.Background(), client, "web-abc123", "default", "")
	require.NoError(t, err)
	assert.Equal(t, "web", name)
}

func TestPodExecutor_ResolveContainer_PodNotFound(t *testing.T) {
	pe := NewPodExecutor()
	tool := setupTestTool(t)

	client, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)

	_, err = pe.resolveContainer(context.Background(), client, "nonexistent-pod", "default", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-pod")
}

func TestPodExecutor_Execute_NoRestConfig(t *testing.T) {
	pe := NewPodExecutor()
	// ClusterClient with nil restConfig (fake client has no REST config).
	client := &ClusterClient{
		name:      "test",
		namespace: "default",
	}

	_, err := pe.Execute(context.Background(), client, "pod", "default", "container", "ls", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no REST config")
}

func TestK8sTool_Exec_MissingPodName(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"command": "ls",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "name parameter")
}

func TestK8sTool_Exec_MissingCommand(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "exec",
		"name":   "web-abc123",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "command parameter")
}

func TestK8sTool_Exec_SecurityBlocked(t *testing.T) {
	tool := setupTestTool(t)
	// Set safety level to read-only so exec (dangerous tier) is blocked.
	tool.config.Clusters[0].SafetyLevel = "read"

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "exec",
		"name":    "web-abc123",
		"command": "ls",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds cluster safety level")
}

func TestK8sTool_Exec_Wired(t *testing.T) {
	// Verify exec action is dispatched (no longer returns "not yet implemented").
	// With a fake client (no restConfig), we expect a "no REST config" error,
	// which proves the wiring reaches the executor.
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "exec",
		"name":      "web-abc123",
		"command":   "ls -la",
		"container": "web",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	// The key assertion: we should NOT see "not yet implemented".
	assert.NotContains(t, result.Error, "not yet implemented")
	// We should see the exec error from missing REST config.
	assert.Contains(t, result.Error, "exec failed")
}

func TestLimitedWriter(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &limitedWriter{buf: &buf, max: 100}
		n, err := lw.Write([]byte("hello"))
		assert.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, "hello", buf.String())
	})

	t.Run("exceeds limit", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &limitedWriter{buf: &buf, max: 5}
		n, err := lw.Write([]byte("hello world"))
		assert.NoError(t, err)
		assert.Equal(t, 5, n) // only wrote 5 bytes
		assert.Equal(t, "hello", buf.String())

		// Subsequent writes should be silently discarded.
		n2, err2 := lw.Write([]byte("more"))
		assert.NoError(t, err2)
		assert.Equal(t, 4, n2) // reports full length consumed
		assert.Equal(t, "hello", buf.String())
	})
}
