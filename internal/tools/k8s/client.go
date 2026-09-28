//go:build with_k8s

package k8s

import (
	"fmt"
	"strings"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

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
		"pod":                    "pods",
		"po":                     "pods",
		"pods":                   "pods",
		"deployment":             "deployments",
		"deployments":            "deployments",
		"deploy":                 "deployments",
		"service":                "services",
		"services":               "services",
		"svc":                    "services",
		"configmap":              "configmaps",
		"configmaps":             "configmaps",
		"cm":                     "configmaps",
		"secret":                 "secrets",
		"secrets":                "secrets",
		"node":                   "nodes",
		"nodes":                  "nodes",
		"no":                     "nodes",
		"namespace":              "namespaces",
		"namespaces":             "namespaces",
		"ns":                     "namespaces",
		"statefulset":            "statefulsets",
		"statefulsets":           "statefulsets",
		"sts":                    "statefulsets",
		"daemonset":              "daemonsets",
		"daemonsets":             "daemonsets",
		"ds":                     "daemonsets",
		"job":                    "jobs",
		"jobs":                   "jobs",
		"cronjob":                "cronjobs",
		"cronjobs":               "cronjobs",
		"cj":                     "cronjobs",
		"ingress":                "ingresses",
		"ingresses":              "ingresses",
		"ing":                    "ingresses",
		"persistentvolumeclaim":  "persistentvolumeclaims",
		"persistentvolumeclaims": "persistentvolumeclaims",
		"pvc":                    "persistentvolumeclaims",
		"event":                  "events",
		"events":                 "events",
		"ev":                     "events",
		"replicaset":             "replicasets",
		"replicasets":            "replicasets",
		"rs":                     "replicasets",
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
