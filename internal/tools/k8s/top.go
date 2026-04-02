package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

const (
	defaultTopLimit = 10
	maxTopLimit     = 100
)

// PodMetrics represents resource usage for a single pod.
type PodMetrics struct {
	Name       string             `json:"name"`
	Namespace  string             `json:"namespace"`
	CPUCores   string             `json:"cpu_cores"`
	CPUPercent float64            `json:"cpu_percent,omitempty"` // requires node allocatable
	MemoryMB   string             `json:"memory_mb"`
	MemPercent float64            `json:"memory_percent,omitempty"` // requires node allocatable
	Containers []ContainerMetrics `json:"containers"`
}

// ContainerMetrics represents resource usage for a single container.
type ContainerMetrics struct {
	Name     string `json:"name"`
	CPUCores string `json:"cpu_cores"`
	MemoryMB string `json:"memory_mb"`
}

// NodeMetrics represents resource usage for a single node.
type NodeMetrics struct {
	Name              string  `json:"name"`
	CPUCores          string  `json:"cpu_cores"`
	CPUAllocatable    string  `json:"cpu_allocatable"`
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryMB          string  `json:"memory_mb"`
	MemoryAllocatable string  `json:"memory_allocatable"`
	MemPercent        float64 `json:"memory_percent"`
}

// TopResult holds the complete top command result.
type TopResult struct {
	ResourceType string        `json:"resource_type"` // "pods" or "nodes"
	Namespace    string        `json:"namespace,omitempty"`
	SortBy       string        `json:"sort_by"`
	Items        []interface{} `json:"items"`
}

// executeTop handles the top action dispatch.
func (t *K8sTool) executeTop(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	resource := toolargs.GetString(args, "resource", "pods")

	switch normalizeKind(resource) {
	case "pods":
		return t.executeTopPods(ctx, args)
	case "nodes":
		return t.executeTopNodes(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("top supports 'pods' or 'nodes', got: %s", resource),
		}, nil
	}
}

// executeTopPods retrieves CPU and memory metrics for pods.
func (t *K8sTool) executeTopPods(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	sortBy := toolargs.GetString(args, "sort_by", "cpu")
	limit := toolargs.GetInt(args, "limit", defaultTopLimit)
	if limit <= 0 || limit > maxTopLimit {
		limit = defaultTopLimit
	}

	// Use "all" as a sentinel for all namespaces
	if namespace == "" || namespace == "all" {
		namespace = ""
	}

	// Check security (top is a read operation)
	if secErr := t.checkSecurity("top", "pods", namespace, clusterCfg); secErr != nil {
		return secErr, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	if client.restConfig == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "metrics client requires a valid REST config (cluster connection not fully initialized)",
		}, nil
	}

	metricsClient, err := metricsclientset.NewForConfig(client.restConfig)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create metrics client: %v (is metrics-server installed?)", err),
		}, nil
	}

	var podMetricsList *metricsv1beta1.PodMetricsList
	if namespace == "" {
		podMetricsList, err = metricsClient.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
	} else {
		podMetricsList, err = metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		if isMetricsServerNotAvailable(err) {
			return &types.ToolResult{
				Success: false,
				Error:   "metrics-server is not available in this cluster. Install metrics-server to use 'top' action.",
			}, nil
		}
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get pod metrics: %v", err)}, nil
	}

	if len(podMetricsList.Items) == 0 {
		ns := namespace
		if ns == "" {
			ns = "all namespaces"
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("No pod metrics available in %s on cluster %s", ns, clusterName),
			Data:    map[string]interface{}{"items": []interface{}{}, "count": 0},
		}, nil
	}

	// Convert to our metrics type
	pods := make([]PodMetrics, 0, len(podMetricsList.Items))
	for _, pm := range podMetricsList.Items {
		podMetric := convertPodMetrics(&pm)
		pods = append(pods, podMetric)
	}

	// Sort by requested field
	sortPodMetrics(pods, sortBy)

	// Apply limit
	if len(pods) > limit {
		pods = pods[:limit]
	}

	// Convert to interface slice for JSON
	items := make([]interface{}, len(pods))
	for i, p := range pods {
		items[i] = p
	}

	// Build table-like content for AI readability
	content := buildPodMetricsTable(pods, namespace, clusterName, sortBy)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"resource_type": "pods",
			"namespace":     namespace,
			"sort_by":       sortBy,
			"items":         items,
			"count":         len(items),
		},
	}, nil
}

// executeTopNodes retrieves CPU and memory metrics for nodes.
func (t *K8sTool) executeTopNodes(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	sortBy := toolargs.GetString(args, "sort_by", "cpu")
	limit := toolargs.GetInt(args, "limit", defaultTopLimit)
	if limit <= 0 || limit > maxTopLimit {
		limit = defaultTopLimit
	}

	// Check security (top is a read operation)
	if secErr := t.checkSecurity("top", "nodes", "", clusterCfg); secErr != nil {
		return secErr, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	if client.restConfig == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "metrics client requires a valid REST config (cluster connection not fully initialized)",
		}, nil
	}

	metricsClient, err := metricsclientset.NewForConfig(client.restConfig)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create metrics client: %v (is metrics-server installed?)", err),
		}, nil
	}

	nodeMetricsList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		if isMetricsServerNotAvailable(err) {
			return &types.ToolResult{
				Success: false,
				Error:   "metrics-server is not available in this cluster. Install metrics-server to use 'top' action.",
			}, nil
		}
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get node metrics: %v", err)}, nil
	}

	if len(nodeMetricsList.Items) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("No node metrics available on cluster %s", clusterName),
			Data:    map[string]interface{}{"items": []interface{}{}, "count": 0},
		}, nil
	}

	// Get node allocatable resources for percentage calculation
	nodeAllocatable := make(map[string]corev1.ResourceList)
	nodeList, err := client.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil && nodeList != nil {
		for _, node := range nodeList.Items {
			nodeAllocatable[node.Name] = node.Status.Allocatable
		}
	}

	// Convert to our metrics type
	nodes := make([]NodeMetrics, 0, len(nodeMetricsList.Items))
	for _, nm := range nodeMetricsList.Items {
		nodeMetric := convertNodeMetrics(&nm, nodeAllocatable[nm.Name])
		nodes = append(nodes, nodeMetric)
	}

	// Sort by requested field
	sortNodeMetrics(nodes, sortBy)

	// Apply limit
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}

	// Convert to interface slice for JSON
	items := make([]interface{}, len(nodes))
	for i, n := range nodes {
		items[i] = n
	}

	// Build table-like content for AI readability
	content := buildNodeMetricsTable(nodes, clusterName, sortBy)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"resource_type": "nodes",
			"sort_by":       sortBy,
			"items":         items,
			"count":         len(items),
		},
	}, nil
}

// convertPodMetrics converts a metrics API PodMetrics to our PodMetrics type.
func convertPodMetrics(pm *metricsv1beta1.PodMetrics) PodMetrics {
	var totalCPU, totalMem resource.Quantity

	containers := make([]ContainerMetrics, 0, len(pm.Containers))
	for _, c := range pm.Containers {
		cpu := c.Usage.Cpu()
		mem := c.Usage.Memory()

		totalCPU.Add(*cpu)
		totalMem.Add(*mem)

		containers = append(containers, ContainerMetrics{
			Name:     c.Name,
			CPUCores: formatCPU(cpu),
			MemoryMB: formatMemory(mem),
		})
	}

	return PodMetrics{
		Name:       pm.Name,
		Namespace:  pm.Namespace,
		CPUCores:   formatCPU(&totalCPU),
		MemoryMB:   formatMemory(&totalMem),
		Containers: containers,
	}
}

// convertNodeMetrics converts a metrics API NodeMetrics to our NodeMetrics type.
func convertNodeMetrics(nm *metricsv1beta1.NodeMetrics, allocatable corev1.ResourceList) NodeMetrics {
	cpu := nm.Usage.Cpu()
	mem := nm.Usage.Memory()

	result := NodeMetrics{
		Name:     nm.Name,
		CPUCores: formatCPU(cpu),
		MemoryMB: formatMemory(mem),
	}

	if allocatable != nil {
		allocCPU := allocatable.Cpu()
		allocMem := allocatable.Memory()

		result.CPUAllocatable = formatCPU(allocCPU)
		result.MemoryAllocatable = formatMemory(allocMem)

		if allocCPU.MilliValue() > 0 {
			result.CPUPercent = float64(cpu.MilliValue()) / float64(allocCPU.MilliValue()) * 100
		}
		if allocMem.Value() > 0 {
			result.MemPercent = float64(mem.Value()) / float64(allocMem.Value()) * 100
		}
	}

	return result
}

// formatCPU formats CPU quantity as millicores or cores.
func formatCPU(q *resource.Quantity) string {
	milliVal := q.MilliValue()
	if milliVal < 1000 {
		return fmt.Sprintf("%dm", milliVal)
	}
	return fmt.Sprintf("%.2f", float64(milliVal)/1000)
}

// formatMemory formats memory quantity in human-readable units.
func formatMemory(q *resource.Quantity) string {
	bytes := q.Value()
	if bytes < 1024*1024 {
		return fmt.Sprintf("%dKi", bytes/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%dMi", bytes/(1024*1024))
	}
	return fmt.Sprintf("%.1fGi", float64(bytes)/(1024*1024*1024))
}

// sortPodMetrics sorts pod metrics by the specified field (cpu or memory).
func sortPodMetrics(pods []PodMetrics, sortBy string) {
	sort.Slice(pods, func(i, j int) bool {
		if sortBy == "memory" || sortBy == "mem" {
			return parseMemoryValue(pods[i].MemoryMB) > parseMemoryValue(pods[j].MemoryMB)
		}
		// Default to CPU
		return parseCPUValue(pods[i].CPUCores) > parseCPUValue(pods[j].CPUCores)
	})
}

// sortNodeMetrics sorts node metrics by the specified field (cpu or memory).
func sortNodeMetrics(nodes []NodeMetrics, sortBy string) {
	sort.Slice(nodes, func(i, j int) bool {
		if sortBy == "memory" || sortBy == "mem" {
			return parseMemoryValue(nodes[i].MemoryMB) > parseMemoryValue(nodes[j].MemoryMB)
		}
		// Default to CPU
		return parseCPUValue(nodes[i].CPUCores) > parseCPUValue(nodes[j].CPUCores)
	})
}

// parseCPUValue parses formatted CPU string back to millicores for sorting.
func parseCPUValue(s string) int64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "m") {
		var val int64
		fmt.Sscanf(s, "%dm", &val)
		return val
	}
	var val float64
	fmt.Sscanf(s, "%f", &val)
	return int64(val * 1000)
}

// parseMemoryValue parses formatted memory string back to bytes for sorting.
func parseMemoryValue(s string) int64 {
	s = strings.TrimSpace(s)
	var val float64
	if strings.HasSuffix(s, "Ki") {
		fmt.Sscanf(s, "%fKi", &val)
		return int64(val * 1024)
	}
	if strings.HasSuffix(s, "Mi") {
		fmt.Sscanf(s, "%fMi", &val)
		return int64(val * 1024 * 1024)
	}
	if strings.HasSuffix(s, "Gi") {
		fmt.Sscanf(s, "%fGi", &val)
		return int64(val * 1024 * 1024 * 1024)
	}
	return 0
}

// isMetricsServerNotAvailable checks if the error indicates metrics-server is not installed.
func isMetricsServerNotAvailable(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "metrics.k8s.io") ||
		strings.Contains(errStr, "the server could not find the requested resource") ||
		strings.Contains(errStr, "no matches for kind")
}

// buildPodMetricsTable creates a human-readable table of pod metrics.
func buildPodMetricsTable(pods []PodMetrics, namespace, cluster, sortBy string) string {
	var b strings.Builder

	ns := namespace
	if ns == "" {
		ns = "all namespaces"
	}

	fmt.Fprintf(&b, "Pod resource usage on cluster %s (namespace: %s, sorted by: %s)\n\n", cluster, ns, sortBy)

	if len(pods) == 0 {
		fmt.Fprintln(&b, "No pods with metrics found.")
		return b.String()
	}

	// Table header
	fmt.Fprintf(&b, "%-50s %-20s %-12s %-12s\n", "NAME", "NAMESPACE", "CPU", "MEMORY")
	fmt.Fprintln(&b, strings.Repeat("-", 94))

	for _, p := range pods {
		name := p.Name
		if len(name) > 48 {
			name = name[:45] + "..."
		}
		namespace := p.Namespace
		if len(namespace) > 18 {
			namespace = namespace[:15] + "..."
		}
		fmt.Fprintf(&b, "%-50s %-20s %-12s %-12s\n", name, namespace, p.CPUCores, p.MemoryMB)
	}

	fmt.Fprintf(&b, "\nShowing %d pod(s)\n", len(pods))
	return b.String()
}

// buildNodeMetricsTable creates a human-readable table of node metrics.
func buildNodeMetricsTable(nodes []NodeMetrics, cluster, sortBy string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Node resource usage on cluster %s (sorted by: %s)\n\n", cluster, sortBy)

	if len(nodes) == 0 {
		fmt.Fprintln(&b, "No nodes with metrics found.")
		return b.String()
	}

	// Table header
	fmt.Fprintf(&b, "%-40s %-15s %-10s %-15s %-10s\n", "NAME", "CPU", "CPU%", "MEMORY", "MEM%")
	fmt.Fprintln(&b, strings.Repeat("-", 90))

	for _, n := range nodes {
		name := n.Name
		if len(name) > 38 {
			name = name[:35] + "..."
		}
		cpuPct := "-"
		memPct := "-"
		if n.CPUPercent > 0 {
			cpuPct = fmt.Sprintf("%.1f%%", n.CPUPercent)
		}
		if n.MemPercent > 0 {
			memPct = fmt.Sprintf("%.1f%%", n.MemPercent)
		}
		fmt.Fprintf(&b, "%-40s %-15s %-10s %-15s %-10s\n", name, n.CPUCores, cpuPct, n.MemoryMB, memPct)
	}

	fmt.Fprintf(&b, "\nShowing %d node(s)\n", len(nodes))
	return b.String()
}
