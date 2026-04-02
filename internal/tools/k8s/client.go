package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const maxLogBytes = 32 * 1024 // 32KB max log output

// ClusterConfig holds the configuration needed to connect to a cluster.
type ClusterConfig struct {
	Name             string
	KubeconfigPath   string
	Context          string
	DefaultNamespace string
}

// ClusterClient wraps a connected Kubernetes clientset for a single cluster.
type ClusterClient struct {
	name       string
	clientset  kubernetes.Interface
	restConfig *rest.Config
	namespace  string // effective default namespace
}

// ClientManager manages connections to multiple Kubernetes clusters.
type ClientManager struct {
	clusters map[string]*ClusterConfig
	clients  map[string]*ClusterClient
	mu       sync.RWMutex
}

// ClusterInfo provides summary information about a configured cluster.
type ClusterInfo struct {
	Name             string `json:"name"`
	DefaultNamespace string `json:"default_namespace"`
	Connected        bool   `json:"connected"`
	ServerVersion    string `json:"server_version,omitempty"`
	Error            string `json:"error,omitempty"`
}

// NewClientManager creates a new ClientManager from cluster configurations.
// It stores configs but does NOT eagerly connect to any cluster.
func NewClientManager(clusters []ClusterConfig) *ClientManager {
	cm := &ClientManager{
		clusters: make(map[string]*ClusterConfig, len(clusters)),
		clients:  make(map[string]*ClusterClient),
	}
	for i := range clusters {
		c := clusters[i]
		cm.clusters[c.Name] = &c
	}
	return cm
}

// GetClient returns a connected ClusterClient for the named cluster, lazily
// initializing the connection on first use and caching it for reuse.
func (cm *ClientManager) GetClient(clusterName string) (*ClusterClient, error) {
	// Fast path: check if already connected.
	cm.mu.RLock()
	if client, ok := cm.clients[clusterName]; ok {
		cm.mu.RUnlock()
		return client, nil
	}
	cm.mu.RUnlock()

	// Slow path: build the client.
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Double-check after acquiring write lock.
	if client, ok := cm.clients[clusterName]; ok {
		return client, nil
	}

	cfg, ok := cm.clusters[clusterName]
	if !ok {
		return nil, fmt.Errorf("unknown cluster: %s", clusterName)
	}

	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.KubeconfigPath},
		&clientcmd.ConfigOverrides{CurrentContext: cfg.Context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building rest config for cluster %s: %w", clusterName, err)
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset for cluster %s: %w", clusterName, err)
	}

	ns := cfg.DefaultNamespace
	if ns == "" {
		ns = "default"
	}

	client := &ClusterClient{
		name:       clusterName,
		clientset:  cs,
		restConfig: restCfg,
		namespace:  ns,
	}
	cm.clients[clusterName] = client
	return client, nil
}

// SetClient injects a pre-built ClusterClient (useful for testing with fake clientsets).
func (cm *ClientManager) SetClient(name string, client *ClusterClient) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.clients[name] = client
	if _, ok := cm.clusters[name]; !ok {
		cm.clusters[name] = &ClusterConfig{Name: name, DefaultNamespace: client.namespace}
	}
}

// ListClusters returns information about all configured clusters.
func (cm *ClientManager) ListClusters() []ClusterInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	infos := make([]ClusterInfo, 0, len(cm.clusters))
	for _, cfg := range cm.clusters {
		info := ClusterInfo{
			Name:             cfg.Name,
			DefaultNamespace: cfg.DefaultNamespace,
		}
		if info.DefaultNamespace == "" {
			info.DefaultNamespace = "default"
		}
		if client, ok := cm.clients[cfg.Name]; ok {
			info.Connected = true
			if sv, err := client.clientset.Discovery().ServerVersion(); err == nil {
				info.ServerVersion = sv.GitVersion
			} else {
				info.Error = err.Error()
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// Close performs cleanup. Currently a no-op but provides good interface hygiene.
func (cm *ClientManager) Close() {}

// ---------- Resource kind normalization ----------

// normalizeKind maps common shortnames and singular forms to canonical plural kind names.
func normalizeKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	aliases := map[string]string{
		"pod":                     "pods",
		"po":                      "pods",
		"pods":                    "pods",
		"deployment":              "deployments",
		"deployments":             "deployments",
		"deploy":                  "deployments",
		"service":                 "services",
		"services":                "services",
		"svc":                     "services",
		"configmap":               "configmaps",
		"configmaps":              "configmaps",
		"cm":                      "configmaps",
		"secret":                  "secrets",
		"secrets":                 "secrets",
		"node":                    "nodes",
		"nodes":                   "nodes",
		"no":                      "nodes",
		"namespace":               "namespaces",
		"namespaces":              "namespaces",
		"ns":                      "namespaces",
		"statefulset":             "statefulsets",
		"statefulsets":            "statefulsets",
		"sts":                     "statefulsets",
		"daemonset":               "daemonsets",
		"daemonsets":              "daemonsets",
		"ds":                      "daemonsets",
		"job":                     "jobs",
		"jobs":                    "jobs",
		"cronjob":                 "cronjobs",
		"cronjobs":                "cronjobs",
		"cj":                      "cronjobs",
		"ingress":                 "ingresses",
		"ingresses":               "ingresses",
		"ing":                     "ingresses",
		"persistentvolumeclaim":   "persistentvolumeclaims",
		"persistentvolumeclaims":  "persistentvolumeclaims",
		"pvc":                     "persistentvolumeclaims",
		"event":                   "events",
		"events":                  "events",
		"ev":                      "events",
		"replicaset":              "replicasets",
		"replicasets":             "replicasets",
		"rs":                      "replicasets",
	}
	if normalized, ok := aliases[k]; ok {
		return normalized
	}
	return k
}

// ---------- Namespace resolution ----------

func (cc *ClusterClient) resolveNamespace(ns string) string {
	if ns != "" {
		return ns
	}
	if cc.namespace != "" {
		return cc.namespace
	}
	return "default"
}

// ---------- Helper: object to map ----------

func toMap(obj interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func toMapSlice(obj interface{}) ([]map[string]interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, raw := range items {
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}

// ---------- Secret redaction ----------

func redactSecretData(m map[string]interface{}) {
	if data, ok := m["data"].(map[string]interface{}); ok {
		for k := range data {
			data[k] = "<REDACTED>"
		}
	}
}

// ---------- ClusterClient resource methods ----------

// GetResource retrieves a single resource by kind, name, and namespace.
func (cc *ClusterClient) GetResource(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)
	opts := metav1.GetOptions{}

	var obj interface{}
	var err error

	switch normalized {
	case "pods":
		obj, err = cc.clientset.CoreV1().Pods(ns).Get(ctx, name, opts)
	case "deployments":
		obj, err = cc.clientset.AppsV1().Deployments(ns).Get(ctx, name, opts)
	case "services":
		obj, err = cc.clientset.CoreV1().Services(ns).Get(ctx, name, opts)
	case "configmaps":
		obj, err = cc.clientset.CoreV1().ConfigMaps(ns).Get(ctx, name, opts)
	case "secrets":
		obj, err = cc.clientset.CoreV1().Secrets(ns).Get(ctx, name, opts)
	case "nodes":
		obj, err = cc.clientset.CoreV1().Nodes().Get(ctx, name, opts)
	case "namespaces":
		obj, err = cc.clientset.CoreV1().Namespaces().Get(ctx, name, opts)
	case "statefulsets":
		obj, err = cc.clientset.AppsV1().StatefulSets(ns).Get(ctx, name, opts)
	case "daemonsets":
		obj, err = cc.clientset.AppsV1().DaemonSets(ns).Get(ctx, name, opts)
	case "jobs":
		obj, err = cc.clientset.BatchV1().Jobs(ns).Get(ctx, name, opts)
	case "cronjobs":
		obj, err = cc.clientset.BatchV1().CronJobs(ns).Get(ctx, name, opts)
	case "ingresses":
		obj, err = cc.clientset.NetworkingV1().Ingresses(ns).Get(ctx, name, opts)
	case "persistentvolumeclaims":
		obj, err = cc.clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, opts)
	case "events":
		obj, err = cc.clientset.CoreV1().Events(ns).Get(ctx, name, opts)
	case "replicasets":
		obj, err = cc.clientset.AppsV1().ReplicaSets(ns).Get(ctx, name, opts)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
	if err != nil {
		return nil, err
	}

	m, err := toMap(obj)
	if err != nil {
		return nil, err
	}

	if normalized == "secrets" {
		redactSecretData(m)
	}
	return m, nil
}

// ListResources lists resources of a given kind with an optional label selector.
func (cc *ClusterClient) ListResources(ctx context.Context, kind, namespace, labelSelector string) ([]map[string]interface{}, error) {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)
	opts := metav1.ListOptions{LabelSelector: labelSelector}

	var items interface{}
	var err error

	switch normalized {
	case "pods":
		list, e := cc.clientset.CoreV1().Pods(ns).List(ctx, opts)
		err, items = e, podItems(list)
	case "deployments":
		list, e := cc.clientset.AppsV1().Deployments(ns).List(ctx, opts)
		err, items = e, deploymentItems(list)
	case "services":
		list, e := cc.clientset.CoreV1().Services(ns).List(ctx, opts)
		err, items = e, serviceItems(list)
	case "configmaps":
		list, e := cc.clientset.CoreV1().ConfigMaps(ns).List(ctx, opts)
		err, items = e, configMapItems(list)
	case "secrets":
		list, e := cc.clientset.CoreV1().Secrets(ns).List(ctx, opts)
		err, items = e, secretItems(list)
	case "nodes":
		list, e := cc.clientset.CoreV1().Nodes().List(ctx, opts)
		err, items = e, nodeItems(list)
	case "namespaces":
		list, e := cc.clientset.CoreV1().Namespaces().List(ctx, opts)
		err, items = e, namespaceItems(list)
	case "statefulsets":
		list, e := cc.clientset.AppsV1().StatefulSets(ns).List(ctx, opts)
		err, items = e, statefulSetItems(list)
	case "daemonsets":
		list, e := cc.clientset.AppsV1().DaemonSets(ns).List(ctx, opts)
		err, items = e, daemonSetItems(list)
	case "jobs":
		list, e := cc.clientset.BatchV1().Jobs(ns).List(ctx, opts)
		err, items = e, jobItems(list)
	case "cronjobs":
		list, e := cc.clientset.BatchV1().CronJobs(ns).List(ctx, opts)
		err, items = e, cronJobItems(list)
	case "ingresses":
		list, e := cc.clientset.NetworkingV1().Ingresses(ns).List(ctx, opts)
		err, items = e, ingressItems(list)
	case "persistentvolumeclaims":
		list, e := cc.clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, opts)
		err, items = e, pvcItems(list)
	case "events":
		list, e := cc.clientset.CoreV1().Events(ns).List(ctx, opts)
		err, items = e, eventItems(list)
	case "replicasets":
		list, e := cc.clientset.AppsV1().ReplicaSets(ns).List(ctx, opts)
		err, items = e, replicaSetItems(list)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
	if err != nil {
		return nil, err
	}

	result, err := toMapSlice(items)
	if err != nil {
		return nil, err
	}

	if normalized == "secrets" {
		for i := range result {
			redactSecretData(result[i])
		}
	}
	return result, nil
}

// List item extractors — these pull the .Items slice from typed list objects
// while handling the nil list case that the fake client can return.

func podItems(l *corev1.PodList) []corev1.Pod {
	if l == nil {
		return nil
	}
	return l.Items
}
func deploymentItems(l *appsv1.DeploymentList) []appsv1.Deployment {
	if l == nil {
		return nil
	}
	return l.Items
}
func serviceItems(l *corev1.ServiceList) []corev1.Service {
	if l == nil {
		return nil
	}
	return l.Items
}
func configMapItems(l *corev1.ConfigMapList) []corev1.ConfigMap {
	if l == nil {
		return nil
	}
	return l.Items
}
func secretItems(l *corev1.SecretList) []corev1.Secret {
	if l == nil {
		return nil
	}
	return l.Items
}
func nodeItems(l *corev1.NodeList) []corev1.Node {
	if l == nil {
		return nil
	}
	return l.Items
}
func namespaceItems(l *corev1.NamespaceList) []corev1.Namespace {
	if l == nil {
		return nil
	}
	return l.Items
}
func statefulSetItems(l *appsv1.StatefulSetList) []appsv1.StatefulSet {
	if l == nil {
		return nil
	}
	return l.Items
}
func daemonSetItems(l *appsv1.DaemonSetList) []appsv1.DaemonSet {
	if l == nil {
		return nil
	}
	return l.Items
}
func jobItems(l *batchv1.JobList) []batchv1.Job {
	if l == nil {
		return nil
	}
	return l.Items
}
func cronJobItems(l *batchv1.CronJobList) []batchv1.CronJob {
	if l == nil {
		return nil
	}
	return l.Items
}
func ingressItems(l *networkingv1.IngressList) []networkingv1.Ingress {
	if l == nil {
		return nil
	}
	return l.Items
}
func pvcItems(l *corev1.PersistentVolumeClaimList) []corev1.PersistentVolumeClaim {
	if l == nil {
		return nil
	}
	return l.Items
}
func eventItems(l *corev1.EventList) []corev1.Event {
	if l == nil {
		return nil
	}
	return l.Items
}
func replicaSetItems(l *appsv1.ReplicaSetList) []appsv1.ReplicaSet {
	if l == nil {
		return nil
	}
	return l.Items
}

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
