package types

import "context"

// ConfigUpdater is the optional GatewayService extension behind the Gateway
// tool's update_config action (conduit-rmho). The tool calls
// PlanConfigUpdate (validation only, nothing changes), asks the human to
// approve the plan, and only then calls ApplyConfigUpdate with the same
// patch. Neither method performs the approval itself.
type ConfigUpdater interface {
	// PlanConfigUpdate validates patch against the config on disk and
	// classifies each change; it changes nothing.
	PlanConfigUpdate(ctx context.Context, patch map[string]interface{}) (*ConfigUpdateResult, error)
	// ApplyConfigUpdate re-validates patch, saves it to the config file and
	// applies the live-reloadable part. On error nothing changed.
	ApplyConfigUpdate(ctx context.Context, patch map[string]interface{}) (*ConfigUpdateResult, error)
}

// Config change modes (ConfigChange.Mode).
const (
	// ConfigChangeLive: applied to the running gateway without a restart.
	ConfigChangeLive = "live"
	// ConfigChangeRestart: saved to the config file; takes effect at the
	// next restart.
	ConfigChangeRestart = "requires_restart"
)

// ConfigChange is one changed key of a config update.
type ConfigChange struct {
	// Path is the canonical key path, array elements named by their "name"
	// (e.g. "ai.providers.z-ai.timeout_seconds").
	Path string `json:"path"`
	// Value is a display rendering of the new value; "(removed)" for a
	// deleted key. Never a secret: secret keys only accept ${VAR} refs.
	Value string `json:"value"`
	// Mode is ConfigChangeLive or ConfigChangeRestart.
	Mode string `json:"mode"`
	// SecuritySensitive marks keys that affect sandboxing, credentials,
	// provider endpoints, authentication, approvals or remote access.
	SecuritySensitive bool `json:"security_sensitive,omitempty"`
}

// ConfigUpdateResult describes a planned or applied config update.
type ConfigUpdateResult struct {
	Changes []ConfigChange `json:"changes"`
	// Unchanged lists requested keys that already had the requested value.
	Unchanged []string `json:"unchanged,omitempty"`
	// Applied is true once the update was saved and its live part applied.
	Applied bool `json:"applied"`
	// ConfigPath and BackupPath are set when the config file was written.
	ConfigPath string `json:"config_path,omitempty"`
	BackupPath string `json:"backup_path,omitempty"`
	// Readback maps each changed key to its redacted value as saved.
	Readback map[string]interface{} `json:"readback,omitempty"`
}

// LiveKeys returns the paths of changes applied (or to be applied) live.
func (r *ConfigUpdateResult) LiveKeys() []string { return r.keys(ConfigChangeLive) }

// RestartKeys returns the paths of changes that need a restart.
func (r *ConfigUpdateResult) RestartKeys() []string { return r.keys(ConfigChangeRestart) }

// SecurityKeys returns the paths of security-sensitive changes.
func (r *ConfigUpdateResult) SecurityKeys() []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, c := range r.Changes {
		if c.SecuritySensitive {
			out = append(out, c.Path)
		}
	}
	return out
}

func (r *ConfigUpdateResult) keys(mode string) []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, c := range r.Changes {
		if c.Mode == mode {
			out = append(out, c.Path)
		}
	}
	return out
}
