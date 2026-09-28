package config

import "strings"

// Bash command-policy modes for tools.sandbox.denylist_mode (conduit-23hg).
const (
	// DenylistModeLegacy matches each denylist entry as a case-insensitive
	// substring anywhere in the command. This is the default.
	DenylistModeLegacy = "legacy"
	// DenylistModeCommandPosition tokenizes the command and matches entries
	// only against command words (plus their flags/arguments), pipes into a
	// shell and redirect targets. Quoted text and heredoc bodies are ignored.
	DenylistModeCommandPosition = "command_position"
)

// EffectiveDenylistMode returns the normalized denylist mode. Empty or
// unknown values fall back to legacy, the more aggressive matcher; ok is
// false for a non-empty unknown value so callers can warn about it.
func (s SandboxConfig) EffectiveDenylistMode() (mode string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s.DenylistMode)) {
	case "", DenylistModeLegacy:
		return DenylistModeLegacy, true
	case DenylistModeCommandPosition:
		return DenylistModeCommandPosition, true
	default:
		return DenylistModeLegacy, false
	}
}

// StrictAutonomousEnabled reports whether autonomous sessions (heartbeat,
// cron, sub-agents, wakes) also get legacy substring matching in
// command_position mode. Defaults to true when unset.
func (s SandboxConfig) StrictAutonomousEnabled() bool {
	return s.StrictAutonomous == nil || *s.StrictAutonomous
}
