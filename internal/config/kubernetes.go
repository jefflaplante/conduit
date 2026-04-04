package config

import (
	"fmt"
	"log"
	"os"
)

// KubernetesConfig contains settings for the Kubernetes management tool
type KubernetesConfig struct {
	// Enabled controls whether Kubernetes tools are available
	Enabled bool `json:"enabled"`

	// Clusters defines known Kubernetes clusters that can be targeted
	Clusters []KubernetesCluster `json:"clusters,omitempty"`

	// Defaults provides fallback values for cluster settings
	Defaults KubernetesDefaults `json:"defaults,omitempty"`
}

// KubernetesCluster defines a known Kubernetes cluster
type KubernetesCluster struct {
	// Name is the unique identifier for this cluster (e.g., "prod-us-east")
	Name string `json:"name"`

	// KubeconfigPath is the path to the kubeconfig file for this cluster
	KubeconfigPath string `json:"kubeconfig_path" cfg:"env,path"`

	// Context is the kubeconfig context to use (empty = current context)
	Context string `json:"context,omitempty"`

	// DefaultNamespace is the namespace used when none is specified
	DefaultNamespace string `json:"default_namespace,omitempty"`

	// AllowedNamespaces restricts which namespaces can be accessed (empty = all)
	AllowedNamespaces []string `json:"allowed_namespaces,omitempty"`

	// SafetyLevel controls what operations are permitted on this cluster
	// Values: "read", "modify", "dangerous"
	SafetyLevel string `json:"safety_level,omitempty" validate:"enum=read|modify|dangerous"`
}

// KubernetesDefaults defines default settings for clusters
type KubernetesDefaults struct {
	// Namespace is the default namespace when not specified (default: "default")
	Namespace string `json:"namespace,omitempty"`

	// SafetyLevel is the default safety level for clusters (default: "read")
	SafetyLevel string `json:"safety_level,omitempty" validate:"enum=read|modify|dangerous"`
}

// DefaultKubernetesConfig returns safe default configuration
func DefaultKubernetesConfig() KubernetesConfig {
	return KubernetesConfig{
		Enabled:  false, // Disabled by default for safety
		Clusters: []KubernetesCluster{},
		Defaults: KubernetesDefaults{
			Namespace:   "default",
			SafetyLevel: "read",
		},
	}
}

// Validate validates the entire Kubernetes configuration
func (c *KubernetesConfig) Validate() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}

	if err := validateEnumTags(c); err != nil {
		return err
	}

	// Validate clusters
	clusterNames := make(map[string]bool)
	for i, cluster := range c.Clusters {
		if err := cluster.Validate(); err != nil {
			return fmt.Errorf("invalid cluster %d (%s): %w", i, cluster.Name, err)
		}
		if clusterNames[cluster.Name] {
			return fmt.Errorf("duplicate cluster name: %s", cluster.Name)
		}
		clusterNames[cluster.Name] = true
	}

	return nil
}

// Validate validates a KubernetesCluster
func (c *KubernetesCluster) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("cluster name cannot be empty")
	}

	if c.KubeconfigPath == "" {
		return fmt.Errorf("kubeconfig_path cannot be empty")
	}

	// Warn if kubeconfig file doesn't exist (may not exist on config host)
	if _, err := os.Stat(c.KubeconfigPath); os.IsNotExist(err) {
		log.Printf("WARNING: kubeconfig file does not exist: %s (cluster %s)", c.KubeconfigPath, c.Name)
	}

	if err := validateEnumTags(c); err != nil {
		return err
	}

	return nil
}

// GetCluster returns a cluster configuration by name, or nil if not found
func (c *KubernetesConfig) GetCluster(name string) *KubernetesCluster {
	for i := range c.Clusters {
		if c.Clusters[i].Name == name {
			return &c.Clusters[i]
		}
	}
	return nil
}

// EffectiveSafetyLevel returns the safety level for a cluster, falling back to defaults
func (c *KubernetesConfig) EffectiveSafetyLevel(cluster *KubernetesCluster) string {
	if cluster != nil && cluster.SafetyLevel != "" {
		return cluster.SafetyLevel
	}
	if c.Defaults.SafetyLevel != "" {
		return c.Defaults.SafetyLevel
	}
	return "read"
}

