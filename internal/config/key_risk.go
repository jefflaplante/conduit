package config

import "strings"

// KeyRisk is the deterministic classifier behind update_config's approval
// prompt. Every update needs the owner's approval anyway; KeyRisk marks the
// keys whose change can widen what the agent may do, or where its output
// goes, so the prompt can flag them. It is a fixed table, not a model, so
// content the agent read cannot argue a key into the benign bucket. When
// in doubt a key is sensitive.

// riskSections are top-level sections whose every key is security-
// sensitive, with the reason shown in the prompt.
var riskSections = map[string]string{
	"tools":           "tool enablement, sandbox or command policy",
	"tool_policy":     "tool action policy",
	"auth":            "authentication",
	"mcp":             "MCP server access",
	"ssh":             "SSH access to the gateway",
	"remote_ssh":      "remote command execution",
	"kubernetes":      "Kubernetes access",
	"allowed_origins": "WebSocket origins",
	"websocket":       "WebSocket access",
	"ratelimiting":    "rate limits",
	"channels":        "channels and who can talk to the agent",
	"secrets_file":    "credentials",
	"skills":          "skills (code and instructions the agent runs)",
	"tui":             "terminal UI access",
	"database":        "database location",
	"data_dir":        "data location",
	"workspace":       "files loaded into the agent's prompt",
	"agent_heartbeat": "unattended runs and where alerts go",
	"heartbeat":       "unattended runs",
	"maintenance":     "data retention",
}

// riskKeySubstrings mark a key anywhere in a path.
var riskKeySubstrings = []struct{ sub, reason string }{
	{"approval", "human approval"},
	{"exempt", "human approval"},
	{"sandbox", "sandbox"},
	{"denylist", "command policy"},
	{"allowlist", "allowlist"},
	{"allowed_", "allowlist"},
	{"blocked_", "blocklist"},
	{"require_auth", "authentication"},
	{"pairing", "who can talk to the agent"},
	{"owner", "who counts as the owner"},
	{"publish_allowed", "device control"},
	{"alert_target", "where alerts go"},
}

// KeyRisk returns why changing the key at path is security-sensitive, or
// "" when it is not. path is a canonical key path (JoinPath segments).
func KeyRisk(path []string) string {
	if len(path) == 0 {
		return ""
	}
	if r, ok := riskSections[strings.ToLower(path[0])]; ok {
		return r
	}
	for i, seg := range path {
		s := strings.ToLower(seg)
		if IsSecretKey(s) {
			return "credential"
		}
		if s == "auth" {
			return "authentication"
		}
		if s == "base_url" || s == "broker_url" || s == "url" || strings.HasSuffix(s, "_url") || s == "site" {
			return "where requests and data are sent"
		}
		// Turning an integration with write actions on or off.
		if s == "enabled" && i == 1 {
			switch strings.ToLower(path[0]) {
			case "mqtt", "pagerduty", "datadog", "mcp", "remote_ssh", "kubernetes":
				return "enables an integration that can act"
			}
		}
		for _, k := range riskKeySubstrings {
			if strings.Contains(s, k.sub) {
				return k.reason
			}
		}
	}
	return ""
}
