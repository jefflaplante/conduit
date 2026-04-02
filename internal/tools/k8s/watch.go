package k8s

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

const maxWatchEvents = 100

// WatchEvent represents a single resource change observed during a watch.
type WatchEvent struct {
	Type      string    `json:"type"` // ADDED, MODIFIED, DELETED
	Resource  string    `json:"resource"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Timestamp time.Time `json:"timestamp"`
	Summary   string    `json:"summary"` // brief human-readable change description
}

// WatchResult holds the collected events from a bounded watch session.
type WatchResult struct {
	Events    []WatchEvent `json:"events"`
	Completed bool         `json:"completed"` // true if watch ran to timeout
	Duration  string       `json:"duration"`
}

// WatchResources watches for changes to resources of the given kind and collects
// events until the timeout expires or the max event count is reached. This is NOT
// a persistent background watch — each call is bounded.
func WatchResources(ctx context.Context, client *ClusterClient, kind, namespace, labelSelector string, timeout time.Duration) (*WatchResult, error) {
	normalized := normalizeKind(kind)
	ns := client.resolveNamespace(namespace)

	// Clamp timeout.
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}

	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := metav1.ListOptions{LabelSelector: labelSelector}

	watcher, err := startWatch(watchCtx, client, normalized, ns, opts)
	if err != nil {
		return nil, err
	}
	defer watcher.Stop()

	start := time.Now()
	result := &WatchResult{
		Events: make([]WatchEvent, 0),
	}

	ch := watcher.ResultChan()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				// Channel closed (server-side disconnect or context done).
				result.Duration = time.Since(start).Round(time.Millisecond).String()
				return result, nil
			}

			we := watchEventFrom(evt, normalized)
			result.Events = append(result.Events, we)

			if len(result.Events) >= maxWatchEvents {
				result.Duration = time.Since(start).Round(time.Millisecond).String()
				return result, nil
			}

		case <-watchCtx.Done():
			result.Completed = true
			result.Duration = time.Since(start).Round(time.Millisecond).String()
			return result, nil
		}
	}
}

// startWatch creates a watch.Interface for the given resource kind.
func startWatch(ctx context.Context, client *ClusterClient, normalized, ns string, opts metav1.ListOptions) (watch.Interface, error) {
	switch normalized {
	case "pods":
		return client.clientset.CoreV1().Pods(ns).Watch(ctx, opts)
	case "deployments":
		return client.clientset.AppsV1().Deployments(ns).Watch(ctx, opts)
	case "services":
		return client.clientset.CoreV1().Services(ns).Watch(ctx, opts)
	case "configmaps":
		return client.clientset.CoreV1().ConfigMaps(ns).Watch(ctx, opts)
	case "events":
		return client.clientset.CoreV1().Events(ns).Watch(ctx, opts)
	case "nodes":
		return client.clientset.CoreV1().Nodes().Watch(ctx, opts)
	case "statefulsets":
		return client.clientset.AppsV1().StatefulSets(ns).Watch(ctx, opts)
	case "daemonsets":
		return client.clientset.AppsV1().DaemonSets(ns).Watch(ctx, opts)
	case "secrets":
		return client.clientset.CoreV1().Secrets(ns).Watch(ctx, opts)
	case "namespaces":
		return client.clientset.CoreV1().Namespaces().Watch(ctx, opts)
	case "jobs":
		return client.clientset.BatchV1().Jobs(ns).Watch(ctx, opts)
	case "cronjobs":
		return client.clientset.BatchV1().CronJobs(ns).Watch(ctx, opts)
	case "ingresses":
		return client.clientset.NetworkingV1().Ingresses(ns).Watch(ctx, opts)
	case "persistentvolumeclaims":
		return client.clientset.CoreV1().PersistentVolumeClaims(ns).Watch(ctx, opts)
	case "replicasets":
		return client.clientset.AppsV1().ReplicaSets(ns).Watch(ctx, opts)
	default:
		return nil, fmt.Errorf("watch not supported for resource kind: %s", normalized)
	}
}

// watchEventFrom converts a raw watch.Event into a WatchEvent.
func watchEventFrom(evt watch.Event, resource string) WatchEvent {
	we := WatchEvent{
		Type:      string(evt.Type),
		Resource:  resource,
		Timestamp: time.Now(),
	}

	if obj, ok := evt.Object.(metav1.ObjectMetaAccessor); ok {
		meta := obj.GetObjectMeta()
		we.Name = meta.GetName()
		we.Namespace = meta.GetNamespace()
	}

	we.Summary = buildEventSummary(string(evt.Type), evt.Object)
	return we
}

// buildEventSummary extracts useful status info from the object based on type.
func buildEventSummary(eventType string, obj runtime.Object) string {
	if obj == nil {
		return eventType
	}

	switch o := obj.(type) {
	case *corev1.Pod:
		return fmt.Sprintf("Pod %s %s: %s", o.Name, eventType, o.Status.Phase)

	case *appsv1.Deployment:
		var replicas int32
		if o.Spec.Replicas != nil {
			replicas = *o.Spec.Replicas
		}
		return fmt.Sprintf("Deployment %s %s: %d/%d ready",
			o.Name, eventType, o.Status.ReadyReplicas, replicas)

	case *corev1.Service:
		return fmt.Sprintf("Service %s %s: type=%s clusterIP=%s",
			o.Name, eventType, o.Spec.Type, o.Spec.ClusterIP)

	case *corev1.ConfigMap:
		return fmt.Sprintf("ConfigMap %s %s: %d keys",
			o.Name, eventType, len(o.Data))

	case *corev1.Event:
		return fmt.Sprintf("Event %s %s: %s - %s",
			o.InvolvedObject.Name, eventType, o.Reason, o.Message)

	case *corev1.Node:
		ready := "NotReady"
		for _, c := range o.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				ready = "Ready"
				break
			}
		}
		return fmt.Sprintf("Node %s %s: %s", o.Name, eventType, ready)

	case *appsv1.StatefulSet:
		var replicas int32
		if o.Spec.Replicas != nil {
			replicas = *o.Spec.Replicas
		}
		return fmt.Sprintf("StatefulSet %s %s: %d/%d ready",
			o.Name, eventType, o.Status.ReadyReplicas, replicas)

	case *appsv1.DaemonSet:
		return fmt.Sprintf("DaemonSet %s %s: %d/%d ready",
			o.Name, eventType, o.Status.NumberReady, o.Status.DesiredNumberScheduled)

	case *corev1.Secret:
		return fmt.Sprintf("Secret %s %s: %d keys",
			o.Name, eventType, len(o.Data))

	case *corev1.Namespace:
		return fmt.Sprintf("Namespace %s %s: %s",
			o.Name, eventType, o.Status.Phase)

	default:
		// Fall back to ObjectMeta name if available.
		if accessor, ok := obj.(metav1.ObjectMetaAccessor); ok {
			return fmt.Sprintf("%s %s", accessor.GetObjectMeta().GetName(), eventType)
		}
		return eventType
	}
}
