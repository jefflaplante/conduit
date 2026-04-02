package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKubernetesConfig_Validate_Disabled(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: false,
		// Invalid cluster data that should be ignored when disabled
		Clusters: []KubernetesCluster{
			{Name: "", KubeconfigPath: ""},
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestKubernetesConfig_Validate_ValidCluster(t *testing.T) {
	// Create a real kubeconfig file so the warning path isn't triggered
	tmpDir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpDir, "kubeconfig")
	err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config\n"), 0600)
	require.NoError(t, err)

	cfg := KubernetesConfig{
		Enabled: true,
		Clusters: []KubernetesCluster{
			{
				Name:              "prod",
				KubeconfigPath:    kubeconfigPath,
				Context:           "prod-context",
				DefaultNamespace:  "app",
				AllowedNamespaces: []string{"app", "monitoring"},
				SafetyLevel:       "read",
			},
		},
		Defaults: KubernetesDefaults{
			Namespace:   "default",
			SafetyLevel: "read",
		},
	}
	err = cfg.Validate()
	assert.NoError(t, err)
}

func TestKubernetesConfig_Validate_EmptyName(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: true,
		Clusters: []KubernetesCluster{
			{
				Name:           "",
				KubeconfigPath: "/some/path",
			},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster name cannot be empty")
}

func TestKubernetesConfig_Validate_EmptyKubeconfigPath(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: true,
		Clusters: []KubernetesCluster{
			{
				Name:           "test",
				KubeconfigPath: "",
			},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubeconfig_path cannot be empty")
}

func TestKubernetesConfig_Validate_InvalidSafetyLevel(t *testing.T) {
	tests := []struct {
		name  string
		level string
	}{
		{"admin level", "admin"},
		{"write level", "write"},
		{"root level", "root"},
		{"empty-ish level", "MODIFY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := KubernetesConfig{
				Enabled: true,
				Clusters: []KubernetesCluster{
					{
						Name:           "test",
						KubeconfigPath: "/some/path",
						SafetyLevel:    tt.level,
					},
				},
			}
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid safety_level")
		})
	}
}

func TestKubernetesConfig_Validate_InvalidDefaultSafetyLevel(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: true,
		Defaults: KubernetesDefaults{
			SafetyLevel: "admin",
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid defaults safety_level")
}

func TestKubernetesConfig_Validate_DuplicateClusterName(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: true,
		Clusters: []KubernetesCluster{
			{Name: "prod", KubeconfigPath: "/path/a"},
			{Name: "prod", KubeconfigPath: "/path/b"},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate cluster name: prod")
}

func TestKubernetesConfig_GetCluster(t *testing.T) {
	cfg := KubernetesConfig{
		Clusters: []KubernetesCluster{
			{Name: "prod", KubeconfigPath: "/prod/config"},
			{Name: "staging", KubeconfigPath: "/staging/config"},
		},
	}

	// Found
	cluster := cfg.GetCluster("prod")
	require.NotNil(t, cluster)
	assert.Equal(t, "prod", cluster.Name)
	assert.Equal(t, "/prod/config", cluster.KubeconfigPath)

	// Found second
	cluster = cfg.GetCluster("staging")
	require.NotNil(t, cluster)
	assert.Equal(t, "staging", cluster.Name)

	// Not found
	cluster = cfg.GetCluster("dev")
	assert.Nil(t, cluster)
}

func TestKubernetesConfig_EffectiveSafetyLevel(t *testing.T) {
	cfg := KubernetesConfig{
		Defaults: KubernetesDefaults{
			SafetyLevel: "modify",
		},
	}

	// Cluster with override
	cluster := &KubernetesCluster{SafetyLevel: "dangerous"}
	assert.Equal(t, "dangerous", cfg.EffectiveSafetyLevel(cluster))

	// Cluster without override — falls back to defaults
	cluster = &KubernetesCluster{}
	assert.Equal(t, "modify", cfg.EffectiveSafetyLevel(cluster))

	// Nil cluster — falls back to defaults
	assert.Equal(t, "modify", cfg.EffectiveSafetyLevel(nil))

	// No defaults set — falls back to "read"
	cfg.Defaults.SafetyLevel = ""
	assert.Equal(t, "read", cfg.EffectiveSafetyLevel(nil))
}

func TestDefaultKubernetesConfig(t *testing.T) {
	cfg := DefaultKubernetesConfig()

	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.Clusters)
	assert.Equal(t, "default", cfg.Defaults.Namespace)
	assert.Equal(t, "read", cfg.Defaults.SafetyLevel)
}

func TestKubernetesConfig_Validate_ValidSafetyLevels(t *testing.T) {
	for _, level := range []string{"read", "modify", "dangerous"} {
		t.Run(level, func(t *testing.T) {
			cfg := KubernetesConfig{
				Enabled: true,
				Clusters: []KubernetesCluster{
					{
						Name:           "test",
						KubeconfigPath: "/some/path",
						SafetyLevel:    level,
					},
				},
			}
			err := cfg.Validate()
			assert.NoError(t, err)
		})
	}
}

func TestKubernetesConfig_Validate_EmptySafetyLevelIsValid(t *testing.T) {
	cfg := KubernetesConfig{
		Enabled: true,
		Clusters: []KubernetesCluster{
			{
				Name:           "test",
				KubeconfigPath: "/some/path",
				SafetyLevel:    "", // empty should be valid, defaults to "read"
			},
		},
	}
	err := cfg.Validate()
	assert.NoError(t, err)
}
