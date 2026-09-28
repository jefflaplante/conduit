//go:build with_k8s

package k8s

import (
	"context"
	"testing"

	"conduit/internal/approval"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-39lm: events, namespaces and portforward_create go through
// checkSecurity (and authorize) like every other cluster-touching action.

func TestK8sTool_Execute_Events_NamespaceRestricted(t *testing.T) {
	tool := setupTestTool(t)
	tool.config.Clusters[0].AllowedNamespaces = []string{"kube-system"}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "events",
		"namespace": "default",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not in the allowed list")

	// The cluster default namespace is also checked when none is given.
	result, err = tool.Execute(context.Background(), map[string]interface{}{"action": "events"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not in the allowed list")

	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"action":    "events",
		"namespace": "kube-system",
	})
	require.NoError(t, err)
	assert.True(t, result.Success, result.Error)
}

func TestK8sTool_Execute_Events_BlockedPolicy(t *testing.T) {
	tool := setupTestTool(t)
	tool.security = NewSecurityEngine(SecurityConfig{
		BlockedActions: []BlockedAction{{Action: "events", Resource: "*"}},
	})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "events"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "blocked")
}

func TestK8sTool_Execute_Namespaces_FilteredToAllowed(t *testing.T) {
	tool := setupTestTool(t)
	tool.config.Clusters[0].AllowedNamespaces = []string{"KUBE-SYSTEM", "missing"}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "namespaces"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	assert.Equal(t, []interface{}{"kube-system"}, result.Data["namespaces"],
		"only allowed namespaces that exist are listed (case-insensitive match)")
	assert.Equal(t, 1, result.Data["count"])
	assert.Equal(t, true, result.Data["filtered_by_allowed_namespaces"])
	assert.Contains(t, result.Content, "filtered to allowed_namespaces")
}

func TestK8sTool_Execute_Namespaces_Unrestricted(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "namespaces"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	assert.Len(t, result.Data["namespaces"], 2)
	assert.NotContains(t, result.Data, "filtered_by_allowed_namespaces")
}

func TestK8sTool_Execute_Namespaces_BlockedPolicy(t *testing.T) {
	tool := setupTestTool(t)
	tool.security = NewSecurityEngine(SecurityConfig{
		BlockedActions: []BlockedAction{{Action: "namespaces", Resource: "namespaces"}},
	})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "namespaces"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "blocked")
}

func TestK8sTool_Execute_PortForwardCreate_NamespaceRestricted(t *testing.T) {
	tool := setupTestTool(t)
	tool.config.Clusters[0].AllowedNamespaces = []string{"kube-system"}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "portforward_create",
		"name":        "web-abc123",
		"namespace":   "default",
		"remote_port": float64(8080),
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not in the allowed list")
	assert.Empty(t, tool.portForwarder.List())
}

func TestK8sTool_Execute_PortForwardCreate_SafetyLevel(t *testing.T) {
	tool := setupTestTool(t)
	tool.config.Clusters[0].SafetyLevel = "modify"

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "portforward_create",
		"name":        "web-abc123",
		"remote_port": float64(8080),
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds cluster safety level")
	assert.Empty(t, tool.portForwarder.List())
}

func TestSecurityEngine_PortForwardIsDangerous(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	c := se.ClassifyOperation("portforward", "pods", "default")
	assert.Equal(t, TierDangerous, c.Tier)
	assert.Contains(t, c.Reason, "dangerous operation")
}

func portForwardArgs() map[string]interface{} {
	return map[string]interface{}{
		"action": "portforward_create", "name": "web-abc123",
		"local_port": float64(18080), "remote_port": float64(8080),
	}
}

func TestK8sApproval_PortForwardWaitsForApproval(t *testing.T) {
	h := newApprovalHarness(t, []string{"dangerous"})

	res, err := h.tool.Execute(h.interactive(), portForwardArgs())
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Empty(t, h.tool.portForwarder.List(), "port forward opened before approval")

	code, prompt := h.promptCode(t)
	for _, want := range []string{
		"Verb: portforward", "Resource: pods/web-abc123", "Namespace: default",
		"Local port: 18080", "Remote port: 8080", "Risk: dangerous tier",
	} {
		assert.Contains(t, prompt, want)
	}

	// Approval runs the frozen op. The fake client has no REST config, so
	// the forward itself fails, which shows run executed only now.
	assert.True(t, h.reply("YES "+code))
	h.mgr.Wait()
	notices := h.allNotices()
	assert.Contains(t, notices[len(notices)-1].Text, "failed to create port forward")
}

func TestK8sApproval_PortForwardNonInteractiveFailsClosed(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"cron":    approval.WithNonInteractive(context.Background(), "cron"),
		"mcp":     approval.WithNonInteractive(context.Background(), "mcp"),
		"unknown": context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newApprovalHarness(t, []string{"dangerous"})
			res, err := h.tool.Execute(ctx, portForwardArgs())
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
			assert.Empty(t, h.tool.portForwarder.List())
			assert.Empty(t, h.allNotices())
		})
	}
}

func TestK8sApproval_EventsAndNamespacesGatedAtReadTier(t *testing.T) {
	for _, action := range []string{"events", "namespaces"} {
		t.Run(action, func(t *testing.T) {
			h := newApprovalHarness(t, []string{"read"})
			args := map[string]interface{}{"action": action}

			res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"), args)
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])

			res, err = h.tool.Execute(h.interactive(), args)
			require.NoError(t, err)
			assert.Equal(t, "pending", res.Data["approval_status"])
		})
	}
}
