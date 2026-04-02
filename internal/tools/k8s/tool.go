// Package k8s implements the Kubernetes management tool with security controls.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// K8sTool provides Kubernetes cluster management via the tool interface.
type K8sTool struct {
	services *types.ToolServices
	config   *config.KubernetesConfig
	security *SecurityEngine
	clients  *ClientManager
}

// NewK8sTool creates a new Kubernetes tool with the given services and configuration.
func NewK8sTool(services *types.ToolServices, cfg *config.KubernetesConfig) (*K8sTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kubernetes config is required")
	}

	// Create security engine with default config (no blocked actions, no approval required).
	security := NewSecurityEngine(SecurityConfig{})

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
		services: services,
		config:   cfg,
		security: security,
		clients:  clients,
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
- exec: Execute a command in a pod container (future)
- top: Show resource usage metrics (future)`
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *K8sTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The Kubernetes operation to perform",
				"enum":        []string{"get", "describe", "logs", "exec", "scale", "rollout", "delete", "top", "clusters", "namespaces", "events"},
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
		},
		"required": []string{"action"},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *K8sTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := getStringArg(args, "action", "")
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
	case "exec":
		return &types.ToolResult{
			Success: false,
			Error:   "exec not yet implemented -- coming in a future phase",
		}, nil
	case "top":
		return &types.ToolResult{
			Success: false,
			Error:   "top not yet implemented -- requires metrics API",
		}, nil
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

// ---------- Action implementations ----------

func (t *K8sTool) executeClusters() (*types.ToolResult, error) {
	infos := t.clients.ListClusters()
	data, _ := json.Marshal(infos)
	var items []interface{}
	json.Unmarshal(data, &items)

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d configured cluster(s)", len(infos)),
		Data:    map[string]interface{}{"clusters": items},
	}, nil
}

func (t *K8sTool) executeGet(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := getStringArg(args, "resource", "")
	if resource == "" {
		return &types.ToolResult{Success: false, Error: "resource parameter is required for get"}, nil
	}

	name := getStringArg(args, "name", "")
	namespace := t.resolveNamespace(args, clusterCfg)
	labelSelector := getStringArg(args, "label_selector", "")

	if err := t.checkSecurity("get", resource, namespace, clusterCfg); err != nil {
		return err, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	if name != "" {
		// Get single resource
		result, err := client.GetResource(ctx, resource, name, namespace)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get %s/%s: %v", resource, name, err)}, nil
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Retrieved %s/%s in namespace %s on cluster %s", resource, name, namespace, clusterName),
			Data:    result,
		}, nil
	}

	// List resources
	results, err := client.ListResources(ctx, resource, namespace, labelSelector)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to list %s: %v", resource, err)}, nil
	}

	items := make([]interface{}, len(results))
	for i, r := range results {
		items[i] = r
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d %s in namespace %s on cluster %s", len(results), resource, namespace, clusterName),
		Data:    map[string]interface{}{"items": items, "count": len(results)},
	}, nil
}

func (t *K8sTool) executeDescribe(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := getStringArg(args, "resource", "")
	name := getStringArg(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for describe"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	if err := t.checkSecurity("describe", resource, namespace, clusterCfg); err != nil {
		return err, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	description, err := client.DescribeResource(ctx, resource, name, namespace)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to describe %s/%s: %v", resource, name, err)}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: description,
	}, nil
}

func (t *K8sTool) executeLogs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	name := getStringArg(args, "name", "")
	if name == "" {
		return &types.ToolResult{Success: false, Error: "name parameter is required for logs"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	container := getStringArg(args, "container", "")
	tailLines := int64(getIntArg(args, "tail_lines", 100))
	since := int64(getIntArg(args, "since", 0))

	if err := t.checkSecurity("logs", "pods", namespace, clusterCfg); err != nil {
		return err, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	logs, err := client.GetLogs(ctx, name, namespace, container, tailLines, since)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get logs for pod %s: %v", name, err)}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: logs,
	}, nil
}

func (t *K8sTool) executeScale(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := getStringArg(args, "resource", "")
	name := getStringArg(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for scale"}, nil
	}

	replicas := getIntArg(args, "replicas", -1)
	if replicas < 0 {
		return &types.ToolResult{Success: false, Error: "replicas parameter is required for scale (must be >= 0)"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	if secErr := t.checkSecurity("scale", resource, namespace, clusterCfg); secErr != nil {
		return secErr, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	if err := client.ScaleResource(ctx, resource, name, namespace, int32(replicas)); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to scale %s/%s: %v", resource, name, err)}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Scaled %s/%s to %d replicas in namespace %s on cluster %s", resource, name, replicas, namespace, clusterName),
	}, nil
}

func (t *K8sTool) executeRollout(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := getStringArg(args, "resource", "")
	name := getStringArg(args, "name", "")
	subaction := getStringArg(args, "subaction", "")
	if resource == "" || name == "" || subaction == "" {
		return &types.ToolResult{Success: false, Error: "resource, name, and subaction parameters are required for rollout"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	if secErr := t.checkSecurity("rollout", resource, namespace, clusterCfg); secErr != nil {
		return secErr, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	switch subaction {
	case "restart":
		if err := client.RolloutRestart(ctx, resource, name, namespace); err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to restart %s/%s: %v", resource, name, err)}, nil
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Rolling restart initiated for %s/%s in namespace %s on cluster %s", resource, name, namespace, clusterName),
		}, nil

	case "status", "history":
		description, err := client.DescribeResource(ctx, resource, name, namespace)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get rollout %s for %s/%s: %v", subaction, resource, name, err)}, nil
		}
		return &types.ToolResult{
			Success: true,
			Content: description,
		}, nil

	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown rollout subaction: %s (valid: restart, status, history)", subaction),
		}, nil
	}
}

func (t *K8sTool) executeDelete(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := getStringArg(args, "resource", "")
	name := getStringArg(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for delete"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	if secErr := t.checkSecurity("delete", resource, namespace, clusterCfg); secErr != nil {
		return secErr, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	if err := client.DeleteResource(ctx, resource, name, namespace); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to delete %s/%s: %v", resource, name, err)}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Deleted %s/%s in namespace %s on cluster %s", resource, name, namespace, clusterName),
	}, nil
}

func (t *K8sTool) executeNamespaces(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, _, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	namespaces, err := client.ListNamespaces(ctx)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to list namespaces: %v", err)}, nil
	}

	nsInterfaces := make([]interface{}, len(namespaces))
	for i, ns := range namespaces {
		nsInterfaces[i] = ns
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d namespaces on cluster %s", len(namespaces), clusterName),
		Data:    map[string]interface{}{"namespaces": nsInterfaces, "count": len(namespaces)},
	}, nil
}

func (t *K8sTool) executeEvents(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	name := getStringArg(args, "name", "")

	client, err := t.clients.GetClient(clusterName)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
	}

	fieldSelector := ""
	if name != "" {
		fieldSelector = fmt.Sprintf("involvedObject.name=%s", name)
	}

	events, err := client.GetEvents(ctx, namespace, fieldSelector)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get events: %v", err)}, nil
	}

	items := make([]interface{}, len(events))
	for i, e := range events {
		items[i] = e
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d events in namespace %s on cluster %s", len(events), namespace, clusterName),
		Data:    map[string]interface{}{"events": items, "count": len(events)},
	}, nil
}

// ---------- Helper methods ----------

// checkSecurity validates the operation against security policies.
// Returns a non-nil *ToolResult if the operation is blocked or has warnings.
func (t *K8sTool) checkSecurity(action, resource, namespace string, clusterCfg *config.KubernetesCluster) *types.ToolResult {
	classification := t.security.ClassifyOperation(action, resource, namespace)

	// Check namespace restrictions
	if clusterCfg != nil && len(clusterCfg.AllowedNamespaces) > 0 {
		if err := t.security.ValidateNamespace(clusterCfg.AllowedNamespaces, namespace); err != nil {
			return &types.ToolResult{
				Success: false,
				Error:   err.Error(),
			}
		}
	}

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
	clusterName := getStringArg(args, "cluster", "")

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
	ns := getStringArg(args, "namespace", "")
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

func getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

func getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return defaultVal
}
