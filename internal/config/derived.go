package config

import "path/filepath"

// Values derived from other config keys instead of being hardcoded to one
// deployment (conduit-31jg.40).

// RecallEventsFile is the brain recall-event log's file name inside the
// workspace memory directory.
const RecallEventsFile = "recall-events.jsonl"

// RecallEventsPath returns where the brain logs recall events for
// brain_spread reinforcement: <workspace.context_dir>/memory/recall-events.jsonl.
// Empty when no workspace is configured, which disables the log.
func (c *Config) RecallEventsPath() string {
	if c == nil || c.Workspace.ContextDir == "" {
		return ""
	}
	return filepath.Join(c.Workspace.ContextDir, "memory", RecallEventsFile)
}

// applyDerivedDefaults fills settings whose defaults come from other config
// keys. Called by Load after env expansion, before Validate.
func (c *Config) applyDerivedDefaults() {
	// agent_heartbeat.timezone inherits the top-level timezone (it used to
	// default to one owner's zone); GetLocation falls back to UTC when both
	// are empty.
	if c.AgentHeartbeat.Timezone == "" {
		c.AgentHeartbeat.Timezone = c.Timezone
	}
	// Skills resolve workspace-relative paths (e.g. skills.gog.cleanup_script)
	// against the workspace directory.
	if c.Skills.WorkspaceDir == "" {
		c.Skills.WorkspaceDir = c.Workspace.ContextDir
	}
}
