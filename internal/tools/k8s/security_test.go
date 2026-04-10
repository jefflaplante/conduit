//go:build with_k8s

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyOperation_ReadOps(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	for _, action := range []string{"get", "list", "describe", "logs", "top", "events", "clusters", "namespaces"} {
		c := se.ClassifyOperation(action, "pods", "default")
		assert.Equal(t, TierRead, c.Tier, "action %q should be read", action)
		assert.False(t, c.Blocked)
		assert.Empty(t, c.Warnings)
	}
}

func TestClassifyOperation_ModifyOps(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	for _, action := range []string{"scale", "rollout", "label", "annotate", "cordon", "uncordon"} {
		c := se.ClassifyOperation(action, "deployments", "default")
		assert.Equal(t, TierModify, c.Tier, "action %q should be modify", action)
		assert.False(t, c.Blocked)
	}
}

func TestClassifyOperation_DangerousOps(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	for _, action := range []string{"delete", "apply", "create", "edit", "drain", "exec", "patch"} {
		c := se.ClassifyOperation(action, "pods", "default")
		assert.Equal(t, TierDangerous, c.Tier, "action %q should be dangerous", action)
		assert.False(t, c.Blocked)
		assert.NotEmpty(t, c.Warnings)
	}
}

func TestClassifyOperation_UnknownDefaultsDangerous(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	c := se.ClassifyOperation("teleport", "pods", "default")
	assert.Equal(t, TierDangerous, c.Tier)
	assert.Contains(t, c.Reason, "unknown action")
	assert.NotEmpty(t, c.Warnings)
}

func TestClassifyOperation_BlockedSpecificResource(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{
		BlockedActions: []BlockedAction{
			{Action: "delete", Resource: "namespaces"},
		},
	})

	c := se.ClassifyOperation("delete", "namespaces", "default")
	assert.Equal(t, TierBlocked, c.Tier)
	assert.True(t, c.Blocked)

	// Same action on a different resource should NOT be blocked.
	c2 := se.ClassifyOperation("delete", "pods", "default")
	assert.Equal(t, TierDangerous, c2.Tier)
	assert.False(t, c2.Blocked)
}

func TestClassifyOperation_BlockedWildcard(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{
		BlockedActions: []BlockedAction{
			{Action: "exec", Resource: "*"},
		},
	})

	c := se.ClassifyOperation("exec", "pods", "default")
	assert.Equal(t, TierBlocked, c.Tier)
	assert.True(t, c.Blocked)

	c2 := se.ClassifyOperation("exec", "deployments", "production")
	assert.Equal(t, TierBlocked, c2.Tier)
	assert.True(t, c2.Blocked)
}

func TestClassifyOperation_CaseInsensitive(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	c := se.ClassifyOperation("GET", "Pods", "Default")
	assert.Equal(t, TierRead, c.Tier)
	assert.Equal(t, "get", c.Action)
	assert.Equal(t, "pods", c.Resource)
}

func TestClassifyOperation_PreservesNamespace(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	c := se.ClassifyOperation("get", "pods", "kube-system")
	assert.Equal(t, "kube-system", c.Namespace)
}

func TestValidateNamespace_AllAllowed(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	err := se.ValidateNamespace(nil, "anything")
	assert.NoError(t, err)

	err = se.ValidateNamespace([]string{}, "anything")
	assert.NoError(t, err)
}

func TestValidateNamespace_Restricted(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})

	err := se.ValidateNamespace([]string{"default", "staging"}, "default")
	assert.NoError(t, err)

	err = se.ValidateNamespace([]string{"default", "staging"}, "production")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "production")
}

func TestValidateNamespace_CaseInsensitive(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	err := se.ValidateNamespace([]string{"Default"}, "default")
	assert.NoError(t, err)
}

func TestValidateForCluster_ReadCluster(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})

	readOp := se.ClassifyOperation("get", "pods", "default")
	require.NoError(t, se.ValidateForCluster(readOp, "read"))

	modifyOp := se.ClassifyOperation("scale", "deployments", "default")
	require.Error(t, se.ValidateForCluster(modifyOp, "read"))

	deleteOp := se.ClassifyOperation("delete", "pods", "default")
	require.Error(t, se.ValidateForCluster(deleteOp, "read"))
}

func TestValidateForCluster_ModifyCluster(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})

	readOp := se.ClassifyOperation("get", "pods", "default")
	assert.NoError(t, se.ValidateForCluster(readOp, "modify"))

	modifyOp := se.ClassifyOperation("scale", "deployments", "default")
	assert.NoError(t, se.ValidateForCluster(modifyOp, "modify"))

	deleteOp := se.ClassifyOperation("delete", "pods", "default")
	assert.Error(t, se.ValidateForCluster(deleteOp, "modify"))
}

func TestValidateForCluster_DangerousCluster(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})

	deleteOp := se.ClassifyOperation("delete", "pods", "default")
	assert.NoError(t, se.ValidateForCluster(deleteOp, "dangerous"))
}

func TestRequiresApproval(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{
		RequireApproval: []string{"dangerous", "modify"},
	})

	readOp := se.ClassifyOperation("get", "pods", "default")
	assert.False(t, readOp.RequiresApproval)

	modifyOp := se.ClassifyOperation("scale", "deployments", "default")
	assert.True(t, modifyOp.RequiresApproval)

	deleteOp := se.ClassifyOperation("delete", "pods", "default")
	assert.True(t, deleteOp.RequiresApproval)
}

func TestRequiresApproval_None(t *testing.T) {
	se := NewSecurityEngine(SecurityConfig{})
	c := se.ClassifyOperation("delete", "pods", "default")
	assert.False(t, c.RequiresApproval)
}

func TestTierSeverity_Ordering(t *testing.T) {
	assert.Less(t, tierSeverity(TierRead), tierSeverity(TierModify))
	assert.Less(t, tierSeverity(TierModify), tierSeverity(TierDangerous))
	assert.Less(t, tierSeverity(TierDangerous), tierSeverity(TierBlocked))
}

func TestTierSeverity_Values(t *testing.T) {
	assert.Equal(t, 1, tierSeverity(TierRead))
	assert.Equal(t, 2, tierSeverity(TierModify))
	assert.Equal(t, 3, tierSeverity(TierDangerous))
	assert.Equal(t, 4, tierSeverity(TierBlocked))
}

func TestTierSeverity_UnknownDefaultsDangerous(t *testing.T) {
	assert.Equal(t, 3, tierSeverity(SecurityTier("unknown")))
}
