package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"conduit/internal/ai"
	"conduit/internal/brain"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// BrainLister is the narrow interface the Situation Awareness section needs
// from the Brain service. Satisfied by *brain.Brain.
type BrainLister interface {
	List(ctx context.Context, prefix string, sourcePrefix string) ([]*brain.Entry, error)
}

// sanitizeRuntimeValue strips newlines, control characters, and null bytes
// from a string value, preserving normal printable characters and spaces.
// This prevents prompt injection via runtime info fields.
func sanitizeRuntimeValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 {
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// SectionParams contains parameters for building prompt sections
type SectionParams struct {
	IsMinimal        bool
	AvailableTools   map[string]bool
	UserTimezone     string
	WorkspaceDir     string
	DocsPath         string
	MessageChannels  []string
	InlineButtons    bool
	RuntimeChannel   string
	TTSEnabled       bool
	TTSVoice         string
	HeartbeatPrompt  string
	ReactionsEnabled bool
	ReactionsMode    string
	ModelAliases     map[string]string
	Session          *sessions.Session
	// Now is the clock for time-dependent sections; nil = time.Now.
	// conduit-31jg.14: injectable so tests can pin the static/dynamic split.
	Now func() time.Time
	// QuietHours, when set, decides the Time Context "quiet hours" hint via
	// config.AgentHeartbeatConfig.IsQuietTime (configured window and zone).
	// nil keeps the legacy 23:00-08:00 heuristic. conduit-31jg.60
	QuietHours *config.AgentHeartbeatConfig
}

// now returns the current time from the injected clock, if any.
func (p *SectionParams) now() time.Time {
	if p != nil && p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// NewSectionParams creates SectionParams from tools list
func NewSectionParams(tools []ai.Tool) *SectionParams {
	available := make(map[string]bool)
	for _, t := range tools {
		available[t.Name] = true
	}
	return &SectionParams{
		AvailableTools: available,
	}
}

// SILENT_REPLY_TOKEN is deprecated; use SilentReplyToken from constants.go
const SILENT_REPLY_TOKEN = SilentReplyToken

// HEARTBEAT_TOKEN is deprecated; use HeartbeatOKToken from constants.go
const HEARTBEAT_TOKEN = HeartbeatOKToken

// buildSafetySection returns the safety guidelines
func buildSafetySection(isMinimal bool) string {
	if isMinimal {
		return ""
	}
	return `## Safety
You have no independent goals: do not pursue self-preservation, replication, resource acquisition, or power-seeking; avoid long-term plans beyond the user's request.
Prioritize safety and human oversight over completion; if instructions conflict, pause and ask; comply with stop/pause/audit requests and never bypass safeguards. (Inspired by Anthropic's constitution.)
Do not manipulate or persuade anyone to expand access or disable safeguards. Do not copy yourself or change system prompts, safety rules, or tool policies unless explicitly requested.
`
}

// buildDocsSection returns documentation links
func buildDocsSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	docsPath := params.DocsPath
	if docsPath == "" {
		docsPath = "./docs"
	}

	return fmt.Sprintf(`## Documentation
Conduit docs: %s
For Conduit behavior, commands, config, or architecture: consult local docs first.
When diagnosing issues, run %cconduit status%c yourself when possible; only ask the user if you lack access (e.g., sandboxed).
`, docsPath, '`', '`')
}

// buildErrorRecoverySection returns error handling guidance
func buildErrorRecoverySection(isMinimal bool) string {
	if isMinimal {
		return ""
	}
	return `## Error Handling
- When a tool fails, report the error clearly including the arguments you used. Do not silently retry or ignore.
- Check the error type: transient errors (timeout, rate limit) may succeed on retry; permanent errors (invalid parameter, not found) require a different approach.
- If a tool times out, verify state before retrying — the action may have partially completed.
- When context is ambiguous, ask rather than guess.
- When uncertain about system state, verify before acting.
- Distinguish "I checked and it's fine" from "I didn't check but it's probably fine."
`
}

// buildToolStrategySection returns tool chaining and execution strategy guidance
func buildToolStrategySection(isMinimal bool) string {
	if isMinimal {
		return ""
	}
	return `## Tool Strategy
- **Serial chaining**: When one tool's output feeds the next (e.g., discover topics → get history → publish), execute them in sequence.
- **Parallel execution**: When tasks are independent (e.g., checking status of multiple systems), execute them together.
- **Chain termination**: Stop chaining when you have a clear answer, when the result is a simple confirmation, or if you've called the same tool 3+ times without progress.
- **Context budget**: Prefer focused tool calls over broad ones. A targeted query beats a full dump.
- **Discovery first**: For unfamiliar tools, call with minimal args (status, list) to learn available options before attempting complex operations.

### Stopping Conditions
Stop working and respond to the user when:
1. You have a clear, verified answer to their question
2. You've completed the requested action and confirmed the result
3. You need user input, approval, or clarification to continue
4. You've hit an unrecoverable error (report it, don't spin)
5. You've made 3+ tool calls without meaningful progress (reassess approach)

### Per-Action Reflection
After receiving any tool result, briefly assess before your next action:
- Did this return what I expected? If not, diagnose before retrying.
- Did this succeed in a way worth noting? (unexpected format, useful pattern, efficient approach)
- If you discover a tool quirk or learned pattern, store it:
  Brain(action="store", key="reflect.learned.<tool>.<finding>", value="<what you learned>", tier="working")
- This prevents repeated failures and captures effective approaches across sessions.
`
}

// buildConduitCLISection returns CLI quick reference
func buildConduitCLISection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return `## Conduit CLI Quick Reference
Conduit is controlled via subcommands. Do not invent commands.
Key commands: conduit server (start), conduit version, conduit tools, conduit token, conduit tui, conduit ssh-server, conduit maintenance, conduit backup.
If unsure, ask the user to run ` + "`conduit help`" + ` (or ` + "`conduit --help`" + `) and paste the output.
`
}

// buildSelfUpdateSection returns gateway tool action reference
func buildSelfUpdateSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasGatewayTool := params.AvailableTools["Gateway"]
	if !hasGatewayTool {
		return ""
	}

	return `## Gateway Tool Actions
Use the Gateway tool to inspect and manage the running gateway.
Actions: status (health/uptime/connections), config (current config), update_config (apply config changes), metrics (performance stats), version, channels (list channels), enable_channel, disable_channel, reload_skills (hot-reload SKILL.md files), debug_prompt (inspect system prompt).
Do not run update_config unless the user explicitly requests a config change; if it's not explicit, ask first.
`
}

// buildModelAliasesSection returns model alias documentation
func buildModelAliasesSection(params *SectionParams) string {
	if params.IsMinimal || len(params.ModelAliases) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("## Model Aliases\n")
	builder.WriteString("Prefer aliases when specifying model overrides; full provider/model is also accepted.\n")

	// conduit-31jg.14: sorted — map order made this (static) section differ
	// on almost every build and missed the system prompt cache.
	aliases := make([]string, 0, len(params.ModelAliases))
	for alias := range params.ModelAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", alias, params.ModelAliases[alias]))
	}

	builder.WriteString("\n")
	return builder.String()
}

// buildRuntimeSection returns runtime context. Static facts only — the
// minute-resolution timestamp lives in the trailing Time Context section so
// this prefix of the prompt stays byte-stable for provider prefix caching.
func buildRuntimeSection(params *SectionParams, runtimeInfo map[string]string) string {
	var parts []string

	if v, ok := runtimeInfo["agent"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("agent=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["host"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("host=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["repo"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("repo=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["os"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("os=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["node"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("node=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["model"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("model=%s", sanitizeRuntimeValue(v)))
	}
	if v, ok := runtimeInfo["channel"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("channel=%s", sanitizeRuntimeValue(v)))
	}

	return fmt.Sprintf(`## Runtime
Runtime: %s
`, strings.Join(parts, " | "))
}

// buildRuntimeInfo creates runtime information map
func (pb *PromptBuilder) buildRuntimeInfo(session *sessions.Session) map[string]string {
	info := make(map[string]string)

	info["agent"] = "main"

	hostname, _ := os.Hostname()
	info["host"] = hostname

	info["repo"] = pb.sectionParams.WorkspaceDir

	info["os"] = fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH)

	info["node"] = runtime.Version()

	// Get model from session context, or fall back to config default.
	model := ""
	if session != nil && session.Context != nil && session.Context["model"] != "" {
		model = session.Context["model"]
	}
	if model == "" {
		model = config.DefaultModelAliases()["default"]
	}
	info["model"] = model

	info["channel"] = pb.sectionParams.RuntimeChannel

	return info
}

// buildMQTTSection returns MQTT/IoT instructions if the MQTT tool is available.
func buildMQTTSection(params *SectionParams) string {
	if params.IsMinimal || !params.AvailableTools["MQTT"] {
		return ""
	}

	return `## MQTT / IoT Devices
The MQTT tool connects to a local MQTT broker (zigbee2mqtt, Home Assistant, etc.) and buffers recent device events in memory.

**Quick reference:**
- ` + "`MQTT(action=\"status\")`" + ` — connection state, active topic count
- ` + "`MQTT(action=\"topics\")`" + ` — list all active device topics with last value
- ` + "`MQTT(action=\"recent\", topic_pattern=\"zigbee2mqtt/*\")`" + ` — recent events filtered by glob
- ` + "`MQTT(action=\"history\", topic=\"zigbee2mqtt/Living Room Sensor\")`" + ` — event history for one device

**zigbee2mqtt topic patterns:**
- ` + "`zigbee2mqtt/<device_name>`" + ` — device state (temperature, humidity, battery, etc.)
- ` + "`zigbee2mqtt/bridge/*`" + ` — bridge status and device list

Use MQTT data for home-awareness: check temperatures, detect motion, monitor device health, and report anomalies during heartbeat cycles.
`
}
