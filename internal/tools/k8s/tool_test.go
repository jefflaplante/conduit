package k8s

import (
	"context"
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func int32Ptr(i int32) *int32 { return &i }

func setupTestTool(t *testing.T) *K8sTool {
	t.Helper()

	fakeClient := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web-abc123",
				Namespace: "default",
				Labels:    map[string]string{"app": "web"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "web", Image: "nginx:1.25"},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "api-def456",
				Namespace: "default",
				Labels:    map[string]string{"app": "api"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{Name: "api", Image: "myapp:v2"},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "web",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(3),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "web"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "web"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "web", Image: "nginx:1.25"},
						},
					},
				},
			},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas:     3,
				AvailableReplicas: 3,
			},
		},
	)

	cfg := &config.KubernetesConfig{
		Enabled: true,
		Clusters: []config.KubernetesCluster{
			{
				Name:             "test-cluster",
				KubeconfigPath:   "/fake/path",
				DefaultNamespace: "default",
				SafetyLevel:      "dangerous",
			},
		},
		Defaults: config.KubernetesDefaults{
			Namespace:   "default",
			SafetyLevel: "read",
		},
	}

	tool, err := NewK8sTool(nil, cfg)
	require.NoError(t, err)

	// Inject the fake client.
	tool.clients.SetClient("test-cluster", &ClusterClient{
		name:      "test-cluster",
		clientset: fakeClient,
		namespace: "default",
	})

	return tool
}

func TestNewK8sTool(t *testing.T) {
	cfg := &config.KubernetesConfig{
		Enabled: true,
		Clusters: []config.KubernetesCluster{
			{Name: "prod", KubeconfigPath: "/path"},
		},
	}
	tool, err := NewK8sTool(nil, cfg)
	require.NoError(t, err)
	assert.NotNil(t, tool)
	assert.NotNil(t, tool.security)
	assert.NotNil(t, tool.clients)
}

func TestNewK8sTool_NilConfig(t *testing.T) {
	_, err := NewK8sTool(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestK8sTool_Name(t *testing.T) {
	tool := setupTestTool(t)
	assert.Equal(t, "Kubernetes", tool.Name())
}

func TestK8sTool_Execute_Clusters(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "clusters",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 configured cluster")
	assert.NotNil(t, result.Data["clusters"])
}

func TestK8sTool_Execute_Namespaces(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "namespaces",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "namespace")
	nsList, ok := result.Data["namespaces"].([]interface{})
	require.True(t, ok)
	assert.Len(t, nsList, 2)
}

func TestK8sTool_Execute_Get_List(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get",
		"resource": "pods",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "2 pods")
	items, ok := result.Data["items"].([]interface{})
	require.True(t, ok)
	assert.Len(t, items, 2)
}

func TestK8sTool_Execute_Get_Single(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get",
		"resource": "pods",
		"name":     "web-abc123",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "web-abc123")
	// Data is the resource map directly.
	assert.NotNil(t, result.Data)
}

func TestK8sTool_Execute_Describe(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "describe",
		"resource": "pods",
		"name":     "web-abc123",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "web-abc123")
	assert.Contains(t, result.Content, "pods")
}

func TestK8sTool_Execute_Scale(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "scale",
		"resource": "deploy",
		"name":     "web",
		"replicas": float64(5),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Scaled")
	assert.Contains(t, result.Content, "5 replicas")
}

func TestK8sTool_Execute_Delete(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "delete",
		"resource": "pods",
		"name":     "web-abc123",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Deleted")

	// Verify it's gone.
	result2, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get",
		"resource": "pods",
		"name":     "web-abc123",
	})
	require.NoError(t, err)
	assert.False(t, result2.Success)
}

func TestK8sTool_Execute_SecurityBlocked(t *testing.T) {
	tool := setupTestTool(t)
	// Override the cluster safety level to read-only.
	tool.config.Clusters[0].SafetyLevel = "read"

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "delete",
		"resource": "pods",
		"name":     "web-abc123",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds cluster safety level")
}

func TestK8sTool_Execute_NamespaceRestricted(t *testing.T) {
	tool := setupTestTool(t)
	// Restrict to only kube-system namespace.
	tool.config.Clusters[0].AllowedNamespaces = []string{"kube-system"}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "get",
		"resource":  "pods",
		"namespace": "default",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not in the allowed list")
}

func TestK8sTool_Execute_UnknownAction(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "terraform",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action")
}

func TestK8sTool_Execute_DefaultCluster(t *testing.T) {
	// Single cluster should auto-select.
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get",
		"resource": "pods",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "test-cluster")
}

func TestK8sTool_Execute_MultiCluster_RequiresParam(t *testing.T) {
	tool := setupTestTool(t)
	// Add a second cluster config.
	tool.config.Clusters = append(tool.config.Clusters, config.KubernetesCluster{
		Name:           "staging",
		KubeconfigPath: "/fake/staging",
		SafetyLevel:    "modify",
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get",
		"resource": "pods",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "cluster parameter is required")
}

func TestK8sTool_Execute_Rollout_Restart(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "rollout",
		"resource":  "deploy",
		"name":      "web",
		"subaction": "restart",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Rolling restart")
}

func TestK8sTool_Execute_Events(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "events",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "events")
}

func TestK8sTool_Execute_Exec_RequiresParams(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "exec",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "name parameter")
}

func TestK8sTool_Execute_Top_NotImplemented(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "top",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not yet implemented")
}

func TestK8sTool_Execute_MissingAction(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "action parameter is required")
}
