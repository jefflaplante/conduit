//go:build with_k8s

package k8s

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// SelfTest performs a functional check of the Kubernetes tool.
// It verifies cluster configuration and connectivity.
func (t *K8sTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check cluster configuration
	configDep := types.DependencyStatus{
		Name:     "KubernetesConfig",
		Required: true,
	}

	if t.config == nil || len(t.config.Clusters) == 0 {
		configDep.Available = false
		configDep.Status = "not_configured"
		configDep.Message = "No Kubernetes clusters configured"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Kubernetes is not configured"
		result.Suggestions = []string{
			"Add 'kubernetes' section to config with enabled: true",
			"Configure at least one cluster in kubernetes.clusters",
		}
		deps = append(deps, configDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	configDep.Available = true
	configDep.Status = "configured"
	configDep.Message = fmt.Sprintf("%d cluster(s) configured", len(t.config.Clusters))
	deps = append(deps, configDep)

	// Check client manager
	clientMgrDep := types.DependencyStatus{
		Name:     "ClientManager",
		Required: true,
	}

	if t.clients == nil {
		clientMgrDep.Available = false
		clientMgrDep.Status = "not_initialized"
		clientMgrDep.Message = "Client manager not initialized"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Kubernetes client manager not initialized"
		deps = append(deps, clientMgrDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	clientMgrDep.Available = true
	clientMgrDep.Status = "initialized"
	deps = append(deps, clientMgrDep)

	// Check cluster connectivity
	clusterInfos := t.clients.ListClusters()
	connectedCount := 0
	var clusterDetails []map[string]interface{}

	for _, info := range clusterInfos {
		clusterDep := types.DependencyStatus{
			Name:     fmt.Sprintf("Cluster:%s", info.Name),
			Required: false, // Individual clusters are optional; at least one is required
		}

		if info.Connected {
			connectedCount++
			clusterDep.Available = true
			clusterDep.Status = "connected"
			if info.ServerVersion != "" {
				clusterDep.Message = fmt.Sprintf("K8s %s", info.ServerVersion)
			}
		} else {
			clusterDep.Available = false
			clusterDep.Status = "disconnected"
			if info.Error != "" {
				clusterDep.Message = info.Error
			} else {
				clusterDep.Message = "Not connected (lazy connect on first use)"
			}
		}
		deps = append(deps, clusterDep)

		if opts.Verbose {
			clusterDetails = append(clusterDetails, map[string]interface{}{
				"name":              info.Name,
				"default_namespace": info.DefaultNamespace,
				"connected":         info.Connected,
				"server_version":    info.ServerVersion,
				"error":             info.Error,
			})
		}
	}

	// Determine overall status
	totalClusters := len(clusterInfos)
	if connectedCount == 0 {
		// No clusters connected yet — this is normal for lazy-connect
		result.Status = types.SelfTestStatusDegraded
		result.Message = fmt.Sprintf("%d cluster(s) configured, none connected yet (connects on first use)", totalClusters)
		result.Capabilities = []string{"clusters", "namespaces"}
		result.UnavailableCapabilities = []string{"get", "describe", "logs", "scale", "rollout", "delete", "watch", "exec", "portforward", "top", "events"}
		result.Suggestions = []string{
			"Run action=clusters to see configured clusters",
			"Run action=get with resource=pods to trigger connection",
		}
	} else if connectedCount < totalClusters {
		result.Status = types.SelfTestStatusDegraded
		result.Message = fmt.Sprintf("%d of %d cluster(s) connected", connectedCount, totalClusters)
		result.Capabilities = []string{"get", "describe", "logs", "scale", "rollout", "delete", "watch", "exec", "portforward", "top", "events", "clusters", "namespaces"}
	} else {
		result.Status = types.SelfTestStatusOK
		result.Message = fmt.Sprintf("All %d cluster(s) connected", totalClusters)
		result.Capabilities = []string{"get", "describe", "logs", "scale", "rollout", "delete", "watch", "exec", "portforward", "top", "events", "clusters", "namespaces"}
	}

	// Add verbose details
	if opts.Verbose {
		result.Details = map[string]interface{}{
			"total_clusters":     totalClusters,
			"connected_clusters": connectedCount,
			"clusters":           clusterDetails,
			"default_namespace":  t.config.Defaults.Namespace,
			"default_safety":     t.config.Defaults.SafetyLevel,
		}
	}

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	// Include examples if requested and tool is functional
	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "List clusters",
				Description: "Show configured clusters and their status",
				Args: map[string]interface{}{
					"action": "clusters",
				},
				Expected: "List of clusters with connection status",
			},
			{
				Name:        "Get pods",
				Description: "List pods in a namespace",
				Args: map[string]interface{}{
					"action":    "get",
					"resource":  "pods",
					"namespace": "default",
				},
				Expected: "List of pods with status info",
			},
			{
				Name:        "Get deployment logs",
				Description: "View logs from a pod",
				Args: map[string]interface{}{
					"action":     "logs",
					"name":       "my-pod",
					"namespace":  "default",
					"tail_lines": 50,
				},
				Expected: "Pod container logs",
			},
		}
	}

	return result
}
