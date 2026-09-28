//go:build with_k8s

package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

func (t *K8sTool) executeWatch(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	resource := toolargs.GetString(args, "resource", "")
	if resource == "" {
		return &types.ToolResult{Success: false, Error: "resource parameter is required for watch"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	labelSelector := toolargs.GetString(args, "label_selector", "")
	timeoutSec := toolargs.GetInt(args, "timeout", 30)
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if timeoutSec > 120 {
		timeoutSec = 120
	}

	// Watch is a read operation — same security tier as "get".
	cls, denied := t.checkSecurity("get", resource, namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "watch", Resource: resource,
		Extra: []opField{
			{"label_selector", "Label selector", labelSelector},
			{"timeout", "Timeout (seconds)", strconv.Itoa(timeoutSec)},
		}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
		client, err := t.clients.GetClient(clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
		}

		result, err := WatchResources(ctx, client, resource, namespace, labelSelector, time.Duration(timeoutSec)*time.Second)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("watch failed: %v", err)}, nil
		}

		data, _ := json.Marshal(result)
		var resultMap map[string]interface{}
		_ = json.Unmarshal(data, &resultMap)

		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Watch completed: %d event(s) in %s on cluster %s (completed=%t)",
				len(result.Events), result.Duration, clusterName, result.Completed),
			Data: resultMap,
		}, nil
	})
}

func (t *K8sTool) executeExec(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	pod := toolargs.GetString(args, "name", "")
	if pod == "" {
		return &types.ToolResult{Success: false, Error: "name parameter (pod name) is required for exec"}, nil
	}

	command := toolargs.GetString(args, "command", "")
	if command == "" {
		return &types.ToolResult{Success: false, Error: "command parameter is required for exec"}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)
	container := toolargs.GetString(args, "container", "")

	cls, denied := t.checkSecurity("exec", "pods", namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	timeout := time.Duration(toolargs.GetInt(args, "timeout", 0)) * time.Second

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "exec", Resource: "pods", Name: pod,
		Extra: []opField{
			{"container", "Container", container},
			{"command", "Command", command},
			{"timeout", "Timeout", timeout.String()},
		}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
		client, err := t.clients.GetClient(clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
		}

		result, err := t.podExecutor.Execute(ctx, client, pod, namespace, container, command, timeout)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("exec failed: %v", err)}, nil
		}

		content := result.Stdout
		if result.TimedOut {
			content = fmt.Sprintf("[timed out]\n%s", content)
		}
		if result.Stderr != "" {
			content += fmt.Sprintf("\n--- stderr ---\n%s", result.Stderr)
		}

		data, _ := json.Marshal(result)
		var dataMap map[string]interface{}
		_ = json.Unmarshal(data, &dataMap)

		return &types.ToolResult{
			Success: result.ExitCode == 0 && !result.TimedOut,
			Content: content,
			Data:    dataMap,
		}, nil
	})
}

// ---------- Port forward actions ----------

func (t *K8sTool) executePortForwardCreate(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	pod := toolargs.GetString(args, "name", "")
	if pod == "" {
		return &types.ToolResult{Success: false, Error: "name parameter is required for portforward_create (pod name)"}, nil
	}

	remotePort := toolargs.GetInt(args, "remote_port", 0)
	if remotePort == 0 {
		return &types.ToolResult{Success: false, Error: "remote_port parameter is required for portforward_create"}, nil
	}

	localPort := toolargs.GetInt(args, "local_port", 0)

	// Validate ports early before resolving cluster.
	if err := validatePorts(localPort, remotePort); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	clusterName, clusterCfg, err := t.resolveCluster(args)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	namespace := t.resolveNamespace(args, clusterCfg)

	// Port-forward opens a network path from this host into a pod, the same
	// pods/portforward subresource kubectl gates like pods/exec, so it is
	// classified dangerous (conduit-39lm).
	cls, denied := t.checkSecurity("portforward", "pods", namespace, clusterCfg)
	if denied != nil {
		return denied, nil
	}

	op := k8sOp{Cluster: clusterName, Namespace: namespace, Verb: "portforward", Resource: "pods", Name: pod,
		Extra: []opField{
			{"local_port", "Local port", strconv.Itoa(localPort)},
			{"remote_port", "Remote port", strconv.Itoa(remotePort)},
		}}
	return t.authorize(ctx, cls, op, func(ctx context.Context) (*types.ToolResult, error) {
		client, err := t.clients.GetClient(clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to connect to cluster %s: %v", clusterName, err)}, nil
		}

		fwd, err := t.portForwarder.Create(client, pod, namespace, localPort, remotePort, clusterName)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to create port forward: %v", err)}, nil
		}

		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Port forward created: 127.0.0.1:%d -> %s:%d (pod %s in %s/%s)",
				fwd.LocalPort, pod, remotePort, pod, clusterName, namespace),
			Data: map[string]interface{}{
				"id":          fwd.ID,
				"local_port":  fwd.LocalPort,
				"remote_port": fwd.RemotePort,
				"pod":         fwd.Pod,
				"namespace":   fwd.Namespace,
				"cluster":     fwd.Cluster,
			},
		}, nil
	})
}

// executePortForwardClose and executePortForwardList operate only on the
// in-process forwards this tool created (each of which passed checkSecurity
// and any approval at creation); they make no cluster API call. Closing
// only removes access, so neither is gated (conduit-39lm).
func (t *K8sTool) executePortForwardClose(args map[string]interface{}) (*types.ToolResult, error) {
	id := toolargs.GetString(args, "forward_id", "")
	if id == "" {
		return &types.ToolResult{Success: false, Error: "forward_id parameter is required for portforward_close"}, nil
	}

	if err := t.portForwarder.Close(id); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Port forward %s closed", id),
	}, nil
}

func (t *K8sTool) executePortForwardList() (*types.ToolResult, error) {
	forwards := t.portForwarder.List()

	items := make([]interface{}, len(forwards))
	for i, fwd := range forwards {
		items[i] = map[string]interface{}{
			"id":          fwd.ID,
			"cluster":     fwd.Cluster,
			"pod":         fwd.Pod,
			"namespace":   fwd.Namespace,
			"local_port":  fwd.LocalPort,
			"remote_port": fwd.RemotePort,
			"created_at":  fwd.CreatedAt.Format(time.RFC3339),
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("%d active port forward(s)", len(forwards)),
		Data:    map[string]interface{}{"forwards": items, "count": len(forwards)},
	}, nil
}
