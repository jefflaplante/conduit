//go:build with_k8s

package k8s

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const maxLogBytes = 32 * 1024 // 32KB max log output

// DescribeResource produces a human-readable summary of a resource, similar to
// kubectl describe. It includes metadata, spec summary, status, conditions, and
// related events.
func (cc *ClusterClient) DescribeResource(ctx context.Context, kind, name, namespace string) (string, error) {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)

	var b strings.Builder
	fmt.Fprintf(&b, "Name:         %s\n", name)
	fmt.Fprintf(&b, "Namespace:    %s\n", ns)
	fmt.Fprintf(&b, "Kind:         %s\n", normalized)

	switch normalized {
	case "pods":
		pod, err := cc.clientset.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Node:         %s\n", pod.Spec.NodeName)
		fmt.Fprintf(&b, "Status:       %s\n", pod.Status.Phase)
		fmt.Fprintf(&b, "IP:           %s\n", pod.Status.PodIP)
		if len(pod.Spec.Containers) > 0 {
			fmt.Fprintf(&b, "Containers:\n")
			for _, c := range pod.Spec.Containers {
				fmt.Fprintf(&b, "  - %s (image: %s)\n", c.Name, c.Image)
			}
		}
		if len(pod.Status.Conditions) > 0 {
			fmt.Fprintf(&b, "Conditions:\n")
			for _, c := range pod.Status.Conditions {
				fmt.Fprintf(&b, "  %s: %s\n", c.Type, c.Status)
			}
		}

	case "deployments":
		dep, err := cc.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		var replicas int32
		if dep.Spec.Replicas != nil {
			replicas = *dep.Spec.Replicas
		}
		fmt.Fprintf(&b, "Replicas:     %d desired | %d ready | %d available\n",
			replicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas)
		fmt.Fprintf(&b, "Strategy:     %s\n", dep.Spec.Strategy.Type)
		if len(dep.Status.Conditions) > 0 {
			fmt.Fprintf(&b, "Conditions:\n")
			for _, c := range dep.Status.Conditions {
				fmt.Fprintf(&b, "  %s: %s (%s)\n", c.Type, c.Status, c.Message)
			}
		}

	case "services":
		svc, err := cc.clientset.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Type:         %s\n", svc.Spec.Type)
		fmt.Fprintf(&b, "ClusterIP:    %s\n", svc.Spec.ClusterIP)
		if len(svc.Spec.Ports) > 0 {
			fmt.Fprintf(&b, "Ports:\n")
			for _, p := range svc.Spec.Ports {
				fmt.Fprintf(&b, "  %s %d/%s -> %d\n", p.Name, p.Port, p.Protocol, p.TargetPort.IntValue())
			}
		}

	default:
		// Generic: fall back to getting the resource as a map and printing key fields.
		m, err := cc.GetResource(ctx, kind, name, namespace)
		if err != nil {
			return "", err
		}
		if metadata, ok := m["metadata"].(map[string]interface{}); ok {
			if labels, ok := metadata["labels"].(map[string]interface{}); ok {
				fmt.Fprintf(&b, "Labels:\n")
				for k, v := range labels {
					fmt.Fprintf(&b, "  %s=%v\n", k, v)
				}
			}
		}
		if status, ok := m["status"].(map[string]interface{}); ok {
			if conditions, ok := status["conditions"].([]interface{}); ok {
				fmt.Fprintf(&b, "Conditions:\n")
				for _, c := range conditions {
					if cm, ok := c.(map[string]interface{}); ok {
						fmt.Fprintf(&b, "  %v: %v\n", cm["type"], cm["status"])
					}
				}
			}
		}
	}

	// Append related events.
	events, err := cc.clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", name),
	})
	if err == nil && events != nil && len(events.Items) > 0 {
		fmt.Fprintf(&b, "Events:\n")
		limit := len(events.Items)
		if limit > 10 {
			limit = 10
		}
		for _, e := range events.Items[:limit] {
			fmt.Fprintf(&b, "  %s  %s  %s: %s\n", e.Type, e.Reason, e.Source.Component, e.Message)
		}
	}

	return b.String(), nil
}

// GetLogs returns pod logs as a string. NOT streaming — the full result is
// returned, truncated to 32KB.
func (cc *ClusterClient) GetLogs(ctx context.Context, pod, namespace, container string, tailLines int64, sinceSeconds int64) (string, error) {
	ns := cc.resolveNamespace(namespace)
	opts := &corev1.PodLogOptions{}
	if container != "" {
		opts.Container = container
	}
	if tailLines > 0 {
		opts.TailLines = &tailLines
	}
	if sinceSeconds > 0 {
		dur := sinceSeconds
		opts.SinceSeconds = &dur
	}

	req := cc.clientset.CoreV1().Pods(ns).GetLogs(pod, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("opening log stream: %w", err)
	}
	defer stream.Close()

	limited := io.LimitReader(stream, maxLogBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("reading logs: %w", err)
	}

	result := string(data)
	if len(data) > maxLogBytes {
		result = result[:maxLogBytes] + "\n... [truncated at 32KB]"
	}
	return result, nil
}

// ScaleResource scales a deployment or statefulset to the specified replica count.
func (cc *ClusterClient) ScaleResource(ctx context.Context, kind, name, namespace string, replicas int32) error {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)

	switch normalized {
	case "deployments":
		dep, err := cc.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		dep.Spec.Replicas = &replicas
		_, err = cc.clientset.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{})
		return err

	case "statefulsets":
		sts, err := cc.clientset.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		sts.Spec.Replicas = &replicas
		_, err = cc.clientset.AppsV1().StatefulSets(ns).Update(ctx, sts, metav1.UpdateOptions{})
		return err

	default:
		return fmt.Errorf("scaling not supported for kind: %s", kind)
	}
}

// RolloutRestart triggers a rolling restart by patching the pod template
// annotation with the current timestamp.
func (cc *ClusterClient) RolloutRestart(ctx context.Context, kind, name, namespace string) error {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)

	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	patchBytes := []byte(patch)

	switch normalized {
	case "deployments":
		_, err := cc.clientset.AppsV1().Deployments(ns).Patch(ctx, name, "application/strategic-merge-patch+json", patchBytes, metav1.PatchOptions{})
		return err
	case "statefulsets":
		_, err := cc.clientset.AppsV1().StatefulSets(ns).Patch(ctx, name, "application/strategic-merge-patch+json", patchBytes, metav1.PatchOptions{})
		return err
	case "daemonsets":
		_, err := cc.clientset.AppsV1().DaemonSets(ns).Patch(ctx, name, "application/strategic-merge-patch+json", patchBytes, metav1.PatchOptions{})
		return err
	default:
		return fmt.Errorf("rollout restart not supported for kind: %s", kind)
	}
}

// DeleteResource deletes a resource by kind, name, and namespace.
func (cc *ClusterClient) DeleteResource(ctx context.Context, kind, name, namespace string) error {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)
	opts := metav1.DeleteOptions{}

	switch normalized {
	case "pods":
		return cc.clientset.CoreV1().Pods(ns).Delete(ctx, name, opts)
	case "deployments":
		return cc.clientset.AppsV1().Deployments(ns).Delete(ctx, name, opts)
	case "services":
		return cc.clientset.CoreV1().Services(ns).Delete(ctx, name, opts)
	case "configmaps":
		return cc.clientset.CoreV1().ConfigMaps(ns).Delete(ctx, name, opts)
	case "secrets":
		return cc.clientset.CoreV1().Secrets(ns).Delete(ctx, name, opts)
	case "nodes":
		return cc.clientset.CoreV1().Nodes().Delete(ctx, name, opts)
	case "namespaces":
		return cc.clientset.CoreV1().Namespaces().Delete(ctx, name, opts)
	case "statefulsets":
		return cc.clientset.AppsV1().StatefulSets(ns).Delete(ctx, name, opts)
	case "daemonsets":
		return cc.clientset.AppsV1().DaemonSets(ns).Delete(ctx, name, opts)
	case "jobs":
		return cc.clientset.BatchV1().Jobs(ns).Delete(ctx, name, opts)
	case "cronjobs":
		return cc.clientset.BatchV1().CronJobs(ns).Delete(ctx, name, opts)
	case "ingresses":
		return cc.clientset.NetworkingV1().Ingresses(ns).Delete(ctx, name, opts)
	case "persistentvolumeclaims":
		return cc.clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, opts)
	case "replicasets":
		return cc.clientset.AppsV1().ReplicaSets(ns).Delete(ctx, name, opts)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

// ListNamespaces returns the names of all namespaces in the cluster.
func (cc *ClusterClient) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := cc.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	names := make([]string, len(list.Items))
	for i, ns := range list.Items {
		names[i] = ns.Name
	}
	return names, nil
}

// GetEvents retrieves events in a namespace with an optional field selector.
func (cc *ClusterClient) GetEvents(ctx context.Context, namespace, fieldSelector string) ([]map[string]interface{}, error) {
	ns := cc.resolveNamespace(namespace)
	opts := metav1.ListOptions{FieldSelector: fieldSelector}

	list, err := cc.clientset.CoreV1().Events(ns).List(ctx, opts)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0, len(list.Items))
	for _, e := range list.Items {
		m, err := toMap(e)
		if err != nil {
			continue
		}
		result = append(result, m)
	}
	return result, nil
}
