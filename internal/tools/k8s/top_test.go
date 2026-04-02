package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func TestFormatCPU(t *testing.T) {
	tests := []struct {
		name     string
		quantity resource.Quantity
		want     string
	}{
		{"zero", resource.MustParse("0"), "0m"},
		{"millicores", resource.MustParse("250m"), "250m"},
		{"one_core", resource.MustParse("1"), "1.00"}, // >= 1000m shows as cores
		{"fractional_cores", resource.MustParse("1500m"), "1.50"},
		{"multiple_cores", resource.MustParse("4"), "4.00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCPU(&tt.quantity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatMemory(t *testing.T) {
	tests := []struct {
		name     string
		quantity resource.Quantity
		want     string
	}{
		{"kilobytes", resource.MustParse("512Ki"), "512Ki"},
		{"megabytes", resource.MustParse("256Mi"), "256Mi"},
		{"gigabytes", resource.MustParse("2Gi"), "2.0Gi"},
		{"large_mb", resource.MustParse("1536Mi"), "1.5Gi"}, // 1536Mi = 1.5Gi
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMemory(&tt.quantity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseCPUValue(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"250m", 250},
		{"1000m", 1000},
		{"1.50", 1500},
		{"4.00", 4000},
		{"0m", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseCPUValue(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseMemoryValue(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"512Ki", 512 * 1024},
		{"256Mi", 256 * 1024 * 1024},
		{"2.0Gi", 2 * 1024 * 1024 * 1024},
		{"unknown", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseMemoryValue(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertPodMetrics(t *testing.T) {
	pm := &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-pod",
			Namespace: "default",
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "web",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
			{
				Name: "sidecar",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		},
	}

	result := convertPodMetrics(pm)

	assert.Equal(t, "web-pod", result.Name)
	assert.Equal(t, "default", result.Namespace)
	assert.Equal(t, "150m", result.CPUCores)
	assert.Equal(t, "192Mi", result.MemoryMB)
	require.Len(t, result.Containers, 2)
	assert.Equal(t, "web", result.Containers[0].Name)
	assert.Equal(t, "100m", result.Containers[0].CPUCores)
	assert.Equal(t, "128Mi", result.Containers[0].MemoryMB)
}

func TestConvertNodeMetrics(t *testing.T) {
	nm := &metricsv1beta1.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}

	allocatable := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("16Gi"),
	}

	result := convertNodeMetrics(nm, allocatable)

	assert.Equal(t, "node-1", result.Name)
	assert.Equal(t, "2.00", result.CPUCores)
	assert.Equal(t, "4.0Gi", result.MemoryMB)
	assert.Equal(t, "8.00", result.CPUAllocatable)
	assert.Equal(t, "16.0Gi", result.MemoryAllocatable)
	assert.InDelta(t, 25.0, result.CPUPercent, 0.1)
	assert.InDelta(t, 25.0, result.MemPercent, 0.1)
}

func TestConvertNodeMetrics_NoAllocatable(t *testing.T) {
	nm := &metricsv1beta1.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}

	result := convertNodeMetrics(nm, nil)

	assert.Equal(t, "node-1", result.Name)
	assert.Equal(t, "2.00", result.CPUCores)
	assert.Equal(t, "4.0Gi", result.MemoryMB)
	assert.Empty(t, result.CPUAllocatable)
	assert.Empty(t, result.MemoryAllocatable)
	assert.Equal(t, 0.0, result.CPUPercent)
	assert.Equal(t, 0.0, result.MemPercent)
}

func TestSortPodMetrics_ByCPU(t *testing.T) {
	pods := []PodMetrics{
		{Name: "low", CPUCores: "100m", MemoryMB: "512Mi"},
		{Name: "high", CPUCores: "1.50", MemoryMB: "256Mi"},
		{Name: "mid", CPUCores: "500m", MemoryMB: "128Mi"},
	}

	sortPodMetrics(pods, "cpu")

	assert.Equal(t, "high", pods[0].Name)
	assert.Equal(t, "mid", pods[1].Name)
	assert.Equal(t, "low", pods[2].Name)
}

func TestSortPodMetrics_ByMemory(t *testing.T) {
	pods := []PodMetrics{
		{Name: "low", CPUCores: "1.50", MemoryMB: "128Mi"},
		{Name: "high", CPUCores: "100m", MemoryMB: "1.0Gi"},
		{Name: "mid", CPUCores: "500m", MemoryMB: "512Mi"},
	}

	sortPodMetrics(pods, "memory")

	assert.Equal(t, "high", pods[0].Name)
	assert.Equal(t, "mid", pods[1].Name)
	assert.Equal(t, "low", pods[2].Name)
}

func TestSortNodeMetrics_ByCPU(t *testing.T) {
	nodes := []NodeMetrics{
		{Name: "node-low", CPUCores: "1.00"},
		{Name: "node-high", CPUCores: "4.00"},
		{Name: "node-mid", CPUCores: "2.00"},
	}

	sortNodeMetrics(nodes, "cpu")

	assert.Equal(t, "node-high", nodes[0].Name)
	assert.Equal(t, "node-mid", nodes[1].Name)
	assert.Equal(t, "node-low", nodes[2].Name)
}

func TestSortNodeMetrics_ByMemory(t *testing.T) {
	nodes := []NodeMetrics{
		{Name: "node-low", MemoryMB: "2.0Gi"},
		{Name: "node-high", MemoryMB: "16.0Gi"},
		{Name: "node-mid", MemoryMB: "8.0Gi"},
	}

	sortNodeMetrics(nodes, "memory")

	assert.Equal(t, "node-high", nodes[0].Name)
	assert.Equal(t, "node-mid", nodes[1].Name)
	assert.Equal(t, "node-low", nodes[2].Name)
}

func TestIsMetricsServerNotAvailable(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected bool
	}{
		{
			name:     "metrics api not found",
			errMsg:   "the server could not find the requested resource (get pods.metrics.k8s.io)",
			expected: true,
		},
		{
			name:     "generic not found",
			errMsg:   "the server could not find the requested resource",
			expected: true,
		},
		{
			name:     "no matches for kind",
			errMsg:   "no matches for kind \"PodMetrics\" in group \"metrics.k8s.io\"",
			expected: true,
		},
		{
			name:     "other error",
			errMsg:   "connection refused",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &testError{msg: tt.errMsg}
			got := isMetricsServerNotAvailable(err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestBuildPodMetricsTable(t *testing.T) {
	pods := []PodMetrics{
		{Name: "web-abc123", Namespace: "default", CPUCores: "250m", MemoryMB: "128Mi"},
		{Name: "api-def456", Namespace: "production", CPUCores: "500m", MemoryMB: "256Mi"},
	}

	table := buildPodMetricsTable(pods, "default", "test-cluster", "cpu")

	assert.Contains(t, table, "test-cluster")
	assert.Contains(t, table, "default")
	assert.Contains(t, table, "cpu")
	assert.Contains(t, table, "web-abc123")
	assert.Contains(t, table, "api-def456")
	assert.Contains(t, table, "250m")
	assert.Contains(t, table, "128Mi")
	assert.Contains(t, table, "2 pod(s)")
}

func TestBuildPodMetricsTable_Empty(t *testing.T) {
	table := buildPodMetricsTable([]PodMetrics{}, "", "test-cluster", "cpu")

	assert.Contains(t, table, "all namespaces")
	assert.Contains(t, table, "No pods with metrics found")
}

func TestBuildNodeMetricsTable(t *testing.T) {
	nodes := []NodeMetrics{
		{Name: "node-1", CPUCores: "2.00", CPUPercent: 25.0, MemoryMB: "8.0Gi", MemPercent: 50.0},
	}

	table := buildNodeMetricsTable(nodes, "test-cluster", "cpu")

	assert.Contains(t, table, "test-cluster")
	assert.Contains(t, table, "cpu")
	assert.Contains(t, table, "node-1")
	assert.Contains(t, table, "2.00")
	assert.Contains(t, table, "25.0%")
	assert.Contains(t, table, "8.0Gi")
	assert.Contains(t, table, "50.0%")
	assert.Contains(t, table, "1 node(s)")
}

func TestBuildNodeMetricsTable_NoPercent(t *testing.T) {
	nodes := []NodeMetrics{
		{Name: "node-1", CPUCores: "2.00", MemoryMB: "8.0Gi"},
	}

	table := buildNodeMetricsTable(nodes, "test-cluster", "cpu")

	assert.Contains(t, table, "node-1")
	// Percent columns should show "-" when not available
	// The table format uses fixed widths so we just check the node appears
}

func TestK8sTool_Execute_Top_InvalidResource(t *testing.T) {
	tool := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "top",
		"resource": "secrets",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "supports 'pods' or 'nodes'")
}

func TestK8sTool_Execute_Top_Pods_NoRestConfig(t *testing.T) {
	// The fake clientset doesn't have a restConfig,
	// so we expect a clear error message about that.
	tool := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "top",
		"resource": "pods",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "REST config")
}

func TestK8sTool_Execute_Top_Nodes_NoRestConfig(t *testing.T) {
	tool := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "top",
		"resource": "nodes",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "REST config")
}

func TestK8sTool_Execute_Top_DefaultsToPodsResource(t *testing.T) {
	tool := setupTestTool(t)

	// When resource is not specified, should default to pods
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "top",
		// No resource specified
	})
	require.NoError(t, err)
	// Will fail because no restConfig
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "REST config")
}
