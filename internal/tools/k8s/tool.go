//go:build with_k8s

// Package k8s implements the Kubernetes management tool with security controls.
package k8s

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// K8sTool provides Kubernetes cluster management via the tool interface.
type K8sTool struct {
	services      *types.ToolServices
	config        *config.KubernetesConfig
	security      *SecurityEngine
	clients       *ClientManager
	podExecutor   *PodExecutor
	portForwarder *PortForwarder
}

// NewK8sTool creates a new Kubernetes tool with the given services and configuration.
func NewK8sTool(services *types.ToolServices, cfg *config.KubernetesConfig) (*K8sTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kubernetes config is required")
	}

	// conduit-c8ct: the configured require_approval tiers reach the engine
	// (previously an empty SecurityConfig was used, so nothing was ever
	// classified as needing approval). Absent => DefaultRequireApproval.
	requireApproval := cfg.RequireApproval
	if requireApproval == nil {
		requireApproval = DefaultRequireApproval
	}
	security := NewSecurityEngine(SecurityConfig{RequireApproval: requireApproval})

	// Convert config clusters to client manager clusters.
	clusters := make([]ClusterConfig, len(cfg.Clusters))
	for i, c := range cfg.Clusters {
		clusters[i] = ClusterConfig{
			Name:             c.Name,
			KubeconfigPath:   c.KubeconfigPath,
			Context:          c.Context,
			DefaultNamespace: c.DefaultNamespace,
		}
	}

	clients := NewClientManager(clusters)

	return &K8sTool{
		services:      services,
		config:        cfg,
		security:      security,
		clients:       clients,
		podExecutor:   NewPodExecutor(),
		portForwarder: NewPortForwarder(10),
	}, nil
}

// Name returns the tool name.
func (t *K8sTool) Name() string { return "Kubernetes" }

// Description returns a human-readable description of the tool's capabilities.
func (t *K8sTool) Description() string {
	return `Kubernetes cluster management tool. Supported actions:
- get: Get or list resources (pods, deployments, services, etc.)
- describe: Show detailed resource description with events
- logs: Retrieve pod container logs
- scale: Scale a deployment or statefulset replica count
- rollout: Rollout operations (restart, status, history)
- delete: Delete a resource
- clusters: List configured clusters and connection status
- namespaces: List namespaces in a cluster
- events: List events in a namespace
- watch: Watch for resource changes over a bounded time window
- exec: Execute a command in a pod container
- portforward_create: Forward a local port to a pod port
- portforward_close: Close an active port forward
- portforward_list: List active port forwards
- top: Show resource usage metrics (requires metrics-server)
  - resource=pods: CPU/memory per pod (params: namespace, sort_by, limit)
  - resource=nodes: CPU/memory per node (params: sort_by, limit)`
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *K8sTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The Kubernetes operation to perform",
				"enum":        []string{"get", "describe", "logs", "exec", "scale", "rollout", "delete", "watch", "top", "clusters", "namespaces", "events", "portforward_create", "portforward_close", "portforward_list"},
			},
			"cluster": map[string]interface{}{
				"type":        "string",
				"description": "Target cluster name (auto-selected if only one cluster is configured)",
			},
			"resource": map[string]interface{}{
				"type":        "string",
				"description": "Resource kind (e.g., pods, deploy, svc, configmaps, secrets)",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Resource name for get/describe/delete/scale/rollout/logs",
			},
			"namespace": map[string]interface{}{
				"type":        "string",
				"description": "Target namespace (defaults to cluster default or config default)",
			},
			"label_selector": map[string]interface{}{
				"type":        "string",
				"description": "Label filter for get/list (e.g., app=nginx)",
			},
			"container": map[string]interface{}{
				"type":        "string",
				"description": "Container name for logs/exec (defaults to first container)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute in a container (for exec action)",
			},
			"tail_lines": map[string]interface{}{
				"type":        "integer",
				"description": "Number of log lines to retrieve (default 100)",
			},
			"since": map[string]interface{}{
				"type":        "integer",
				"description": "Show logs since this many seconds ago",
			},
			"replicas": map[string]interface{}{
				"type":        "integer",
				"description": "Target replica count for scale action",
			},
			"subaction": map[string]interface{}{
				"type":        "string",
				"description": "Sub-action for rollout: restart, status, or history",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Watch timeout in seconds (default 30, max 120)",
			},
			"local_port": map[string]interface{}{
				"type":        "integer",
				"description": "Local port for port forwarding (0 for auto-assign)",
			},
			"remote_port": map[string]interface{}{
				"type":        "integer",
				"description": "Remote pod port to forward to",
			},
			"forward_id": map[string]interface{}{
				"type":        "string",
				"description": "Port forward ID for close operation",
			},
			"sort_by": map[string]interface{}{
				"type":        "string",
				"description": "Sort field for top action: 'cpu' (default) or 'memory'",
				"enum":        []string{"cpu", "memory"},
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results for top action (default 10, max 100)",
			},
		},
		"required": []string{"action"},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *K8sTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")
	if action == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	switch action {
	case "clusters":
		return t.executeClusters()
	case "get":
		return t.executeGet(ctx, args)
	case "describe":
		return t.executeDescribe(ctx, args)
	case "logs":
		return t.executeLogs(ctx, args)
	case "scale":
		return t.executeScale(ctx, args)
	case "rollout":
		return t.executeRollout(ctx, args)
	case "delete":
		return t.executeDelete(ctx, args)
	case "namespaces":
		return t.executeNamespaces(ctx, args)
	case "events":
		return t.executeEvents(ctx, args)
	case "watch":
		return t.executeWatch(ctx, args)
	case "exec":
		return t.executeExec(ctx, args)
	case "portforward_create":
		return t.executePortForwardCreate(ctx, args)
	case "portforward_close":
		return t.executePortForwardClose(args)
	case "portforward_list":
		return t.executePortForwardList()
	case "top":
		return t.executeTop(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

// ---------- Helper methods ----------

// checkSecurity validates a namespace-scoped operation against security
// policies. It returns the classification, plus a non-nil *ToolResult when
// the operation is denied outright (namespace, cluster safety level, blocked
// policy). Operations in a require_approval tier are not denied here;
// callers route them through authorize (conduit-c8ct). Every action that
// reaches the cluster must pass through this or checkClusterScopedSecurity
// (conduit-39lm).
func (t *K8sTool) checkSecurity(action, resource, namespace string, clusterCfg *config.KubernetesCluster) (*OperationClassification, *types.ToolResult) {
	classification := t.security.ClassifyOperation(action, resource, namespace)

	// Check namespace restrictions
	if clusterCfg != nil && len(clusterCfg.AllowedNamespaces) > 0 {
		if err := t.security.ValidateNamespace(clusterCfg.AllowedNamespaces, namespace); err != nil {
			return classification, &types.ToolResult{
				Success: false,
				Error:   err.Error(),
			}
		}
	}

	return classification, t.checkPolicy(classification, clusterCfg)
}

// checkClusterScopedSecurity is checkSecurity for operations that have no
// target namespace (listing namespaces). It applies the tier, cluster safety
// level and blocked-policy checks but not allowed_namespaces; the caller is
// responsible for restricting its output to allowed namespaces
// (conduit-39lm).
func (t *K8sTool) checkClusterScopedSecurity(action, resource string, clusterCfg *config.KubernetesCluster) (*OperationClassification, *types.ToolResult) {
	classification := t.security.ClassifyOperation(action, resource, "")
	return classification, t.checkPolicy(classification, clusterCfg)
}

// checkPolicy applies the cluster safety level and blocked-action policy.
func (t *K8sTool) checkPolicy(classification *OperationClassification, clusterCfg *config.KubernetesCluster) *types.ToolResult {
	// Check cluster safety level
	if clusterCfg != nil {
		safetyLevel := t.config.EffectiveSafetyLevel(clusterCfg)
		if err := t.security.ValidateForCluster(classification, safetyLevel); err != nil {
			return &types.ToolResult{
				Success: false,
				Error:   err.Error(),
			}
		}
	}

	// If blocked by policy
	if classification.Blocked {
		return &types.ToolResult{
			Success: false,
			Error:   classification.Reason,
		}
	}

	return nil
}

// resolveCluster determines which cluster to target. If only one cluster is
// configured, it is used automatically. Otherwise the cluster param is required.
func (t *K8sTool) resolveCluster(args map[string]interface{}) (string, *config.KubernetesCluster, error) {
	clusterName := toolargs.GetString(args, "cluster", "")

	if clusterName == "" {
		if len(t.config.Clusters) == 1 {
			clusterName = t.config.Clusters[0].Name
		} else if len(t.config.Clusters) == 0 {
			return "", nil, fmt.Errorf("no clusters configured")
		} else {
			names := make([]string, len(t.config.Clusters))
			for i, c := range t.config.Clusters {
				names[i] = c.Name
			}
			return "", nil, fmt.Errorf("cluster parameter is required when multiple clusters are configured: %s", strings.Join(names, ", "))
		}
	}

	clusterCfg := t.config.GetCluster(clusterName)
	if clusterCfg == nil {
		return "", nil, fmt.Errorf("unknown cluster: %s", clusterName)
	}

	return clusterName, clusterCfg, nil
}

// resolveNamespace determines the target namespace from args, cluster config, or defaults.
func (t *K8sTool) resolveNamespace(args map[string]interface{}, cluster *config.KubernetesCluster) string {
	ns := toolargs.GetString(args, "namespace", "")
	if ns != "" {
		return ns
	}
	if cluster != nil && cluster.DefaultNamespace != "" {
		return cluster.DefaultNamespace
	}
	if t.config.Defaults.Namespace != "" {
		return t.config.Defaults.Namespace
	}
	return "default"
}

// IncludeDataInModelOutput opts this tool into having ToolResult.Data
// rendered for the model: ids and lists needed for follow-up calls live
// only in Data (conduit-31jg.39).
func (t *K8sTool) IncludeDataInModelOutput() bool { return true }
