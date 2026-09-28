//go:build with_k8s

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

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

	resource := toolargs.GetString(args, "resource", "")
	if resource == "" {
		return &types.ToolResult{Success: false, Error: "resource parameter is required for get"}, nil
	}

	name := toolargs.GetString(args, "name", "")
	namespace := t.resolveNamespace(args, clusterCfg)
	labelSelector := toolargs.GetString(args, "label_selector", "")

	cls, denied := t.checkSecurity("get", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "get", Resource: resource, Name: name}
	if labelSelector != "" {
		op.Extra = append(op.Extra, opField{"label_selector", "Label selector", labelSelector})
	}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}

func (t *K8sTool) executeDescribe(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := toolargs.GetString(args, "resource", "")
	name := toolargs.GetString(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for describe"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	cls, denied := t.checkSecurity("describe", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "describe", Resource: resource, Name: name}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}

func (t *K8sTool) executeLogs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	name := toolargs.GetString(args, "name", "")
	if name == "" {
		return &types.ToolResult{Success: false, Error: "name parameter is required for logs"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	container := toolargs.GetString(args, "container", "")
	tailLines := int64(toolargs.GetInt(args, "tail_lines", 100))
	since := int64(toolargs.GetInt(args, "since", 0))

	cls, denied := t.checkSecurity("logs", "pods", namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "logs", Resource: "pods", Name: name,
		Extra: []opField{
			{"container", "Container", container},
			{"tail_lines", "Tail lines", strconv.FormatInt(tailLines, 10)},
			{"since", "Since (seconds)", strconv.FormatInt(since, 10)},
		}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}

func (t *K8sTool) executeScale(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := toolargs.GetString(args, "resource", "")
	name := toolargs.GetString(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for scale"}, nil
	}

	replicas := toolargs.GetInt(args, "replicas", -1)
	if replicas < 0 {
		return &types.ToolResult{Success: false, Error: "replicas parameter is required for scale (must be >= 0)"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	cls, denied := t.checkSecurity("scale", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "scale", Resource: resource, Name: name,
		Extra: []opField{{"replicas", "Replicas", strconv.Itoa(replicas)}}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}

func (t *K8sTool) executeRollout(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := toolargs.GetString(args, "resource", "")
	name := toolargs.GetString(args, "name", "")
	subaction := toolargs.GetString(args, "subaction", "")
	if resource == "" || name == "" || subaction == "" {
		return &types.ToolResult{Success: false, Error: "resource, name, and subaction parameters are required for rollout"}, nil
	}

	switch subaction {
	case "restart", "status", "history":
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown rollout subaction: %s (valid: restart, status, history)", subaction),
		}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	cls, denied := t.checkSecurity("rollout", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "rollout " + subaction, Resource: resource, Name: name}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
		client, err := t.clients.GetClient(clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
		}

		if subaction == "restart" {
			if err := client.RolloutRestart(ctx, resource, name, namespace); err != nil {
				return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to restart %s/%s: %v", resource, name, err)}, nil
			}
			return &types.ToolResult{
				Success: true,
				Content: fmt.Sprintf("Rolling restart initiated for %s/%s in namespace %s on cluster %s", resource, name, namespace, clusterName),
			}, nil
		}

		// status / history
		description, err := client.DescribeResource(ctx, resource, name, namespace)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get rollout %s for %s/%s: %v", subaction, resource, name, err)}, nil
		}
		return &types.ToolResult{
			Success: true,
			Content: description,
		}, nil
	})
}

func (t *K8sTool) executeDelete(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := toolargs.GetString(args, "resource", "")
	name := toolargs.GetString(args, "name", "")
	if resource == "" || name == "" {
		return &types.ToolResult{Success: false, Error: "resource and name parameters are required for delete"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	cls, denied := t.checkSecurity("delete", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "delete", Resource: resource, Name: name}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}

// executeNamespaces lists namespaces. Listing is a cluster-scoped read, so
// it is checked with checkClusterScopedSecurity; when the cluster has
// allowed_namespaces configured the result is filtered to those namespaces,
// so the tool never reveals namespaces it may not act on (conduit-39lm).
func (t *K8sTool) executeNamespaces(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	cls, denied := t.checkClusterScopedSecurity("namespaces", "namespaces", clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: "(cluster-scoped)", Verb: "list", Resource: "namespaces"}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
		client, err := t.clients.GetClient(clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
		}

		namespaces, err := client.ListNamespaces(ctx)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to list namespaces: %v", err)}, nil
		}

		nsInterfaces := make([]interface{}, 0, len(namespaces))
		for _, ns := range namespaces {
			if len(clusterCfg.AllowedNamespaces) > 0 &&
				t.security.ValidateNamespace(clusterCfg.AllowedNamespaces, ns) != nil {
				continue
			}
			nsInterfaces = append(nsInterfaces, ns)
		}

		data := map[string]interface{}{"namespaces": nsInterfaces, "count": len(nsInterfaces)}
		content := fmt.Sprintf("Found %d namespaces on cluster %s", len(nsInterfaces), clusterName)
		if len(clusterCfg.AllowedNamespaces) > 0 {
			data["filtered_by_allowed_namespaces"] = true
			content += " (filtered to allowed_namespaces)"
		}
		return &types.ToolResult{Success: true, Content: content, Data: data}, nil
	})
}

func (t *K8sTool) executeEvents(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	name := toolargs.GetString(args, "name", "")

	// Events are a namespace-scoped read (conduit-39lm).
	cls, denied := t.checkSecurity("events", "events", namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "events", Resource: "events",
		Extra: []opField{{"involved_object", "Involved object", name}}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
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
	})
}
