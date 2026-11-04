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

// ConfigUpdateApprovalKind is the approval.Action.Kind of an update_config
// approval. The gateway matches it to count denied/expired approvals
// (conduit-2qes).
const ConfigUpdateApprovalKind = "gateway.update_config"

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
	// OldValue is the display rendering of the value before the change;
	// "(absent)" for a new key.
	OldValue string `json:"old_value,omitempty"`
	// SecuritySensitive marks keys that affect sandboxing, credentials,
	// provider endpoints, authentication, approvals or remote access
	// (config.KeyRisk); Risk says why.
	SecuritySensitive bool   `json:"security_sensitive,omitempty"`
	Risk              string `json:"risk,omitempty"`
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
	// Warnings lists, in plain words, how the update makes tools ask a
	// human less often (config.ApprovalWeakenings). The approval prompt
	// shows them first.
	Warnings []string `json:"warnings,omitempty"`
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

// ConfigFileEditor is the optional extension behind update_config's
// full-file mode: the agent reads the redacted config file, edits it, and
// proposes the whole file back. The proposal is turned into a patch and
// goes through PlanConfigUpdate, approval and ApplyConfigUpdate like any
// other update; nothing is written here.
type ConfigFileEditor interface {
	// RedactedConfigFile returns the config file as JSON with secrets
	// replaced by the redaction marker.
	RedactedConfigFile(ctx context.Context) (string, error)
	// ConfigPatchFromFile diffs a proposed full config file against the
	// file on disk and returns the equivalent patch (empty: no change).
	ConfigPatchFromFile(ctx context.Context, proposed string) (map[string]interface{}, error)
}
