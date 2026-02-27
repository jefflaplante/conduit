package agent

import (
	"fmt"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

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

const SILENT_REPLY_TOKEN = "NO_REPLY"
const HEARTBEAT_TOKEN = "HEARTBEAT_OK"

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

// buildMemorySection returns memory recall instructions if memory tools are available
func buildMemorySection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasMemorySearch := params.AvailableTools["MemorySearch"] || params.AvailableTools["memory_search"]
	if !hasMemorySearch {
		return ""
	}

	return `## Memory Recall
Before answering anything about prior work, decisions, dates, people, preferences, or todos: run MemorySearch to find relevant content across MEMORY.md, memory/*.md, and session history.
If you need the full file content (e.g., before modifying), use Read instead — MemorySearch is for finding, Read is for reading.
Citations: include Source: <path#line> when it helps the user verify memory snippets.
`
}

// buildMemoryPersistenceSection returns instructions for writing to memory files
func buildMemoryPersistenceSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasWriteTool := params.AvailableTools["Write"] || params.AvailableTools["write"]
	hasBashTool := params.AvailableTools["Bash"] || params.AvailableTools["bash"]
	if !hasWriteTool && !hasBashTool {
		return ""
	}

	return `## Memory Persistence
You have no persistent memory between sessions — files are your brain. Write things down or lose them.

**When to write:**
- Decisions made, preferences learned, facts discovered
- Lessons from mistakes (so you don't repeat them)
- Anything the user says to remember

**Where to write:**
- ` + "`memory/YYYY-MM-DD.md`" + ` — daily logs, raw notes, in-the-moment capture
- ` + "`MEMORY.md`" + ` — curated long-term memory, distilled wisdom

**How to write:**
- **Append new entries:** Use Bash with ` + "`echo \"...\" >> file`" + ` — no need to read first
- **Modify existing content:** Read the file once, make changes, Write the full content back
- **Never read the same file multiple times** in one operation — read once, then write

**Memory hygiene:** Periodically review recent daily files and promote important insights to MEMORY.md. To consolidate: Read the file, reorganize/dedupe mentally, Write it back clean.
`
}

// buildMessagingSection returns messaging tool instructions
func buildMessagingSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(`## Messaging
- Reply in current session → automatically routes to the source channel (Signal, Telegram, etc.)
- Cross-session messaging → use sessions_send(sessionKey, message)
- Never use exec/curl for provider messaging; Conduit handles all routing internally.
`)

	if params.AvailableTools["message"] {
		channelOptions := "telegram|whatsapp|discord|googlechat|slack|signal|imessage"
		if len(params.MessageChannels) > 0 {
			channelOptions = strings.Join(params.MessageChannels, "|")
		}

		builder.WriteString(fmt.Sprintf(`
### message tool
- Use %cmessage%c for proactive sends + channel actions (polls, reactions, etc.).
- For %caction=send%c, include %cto%c and %cmessage%c.
- If multiple channels are configured, pass %cchannel%c (%s).
- If you use %cmessage%c (%caction=send%c) to deliver your user-visible reply, respond with ONLY: %s (avoid duplicate replies).
`, '`', '`', '`', '`', '`', '`', '`', '`', '`', '`', channelOptions, '`', '`', '`', '`', SILENT_REPLY_TOKEN))

		if params.InlineButtons {
			builder.WriteString("- Inline buttons supported. Use `action=send` with `buttons=[[{text,callback_data}]]` (callback_data routes back as a user message).\n")
		}
	}

	builder.WriteString("\n")
	return builder.String()
}

// buildVoiceSection returns TTS instructions if enabled
func buildVoiceSection(params *SectionParams) string {
	if params.IsMinimal || !params.TTSEnabled {
		return ""
	}

	voice := params.TTSVoice
	if voice == "" {
		voice = "default voice"
	}

	return fmt.Sprintf(`## Voice (TTS)
TTS is available via the tts tool. Voice: %s.
Use for audio responses when requested or when TTS mode is enabled.
Copy the MEDIA line exactly when returning audio.
`, voice)
}

// buildReplyTagsSection returns reply tag instructions
func buildReplyTagsSection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return `## Reply Tags
To request a native reply/quote on supported surfaces, include one tag in your reply:
- [[reply_to_current]] replies to the triggering message.
- [[reply_to:<id>]] replies to a specific message id when you have it.
Whitespace inside the tag is allowed (e.g. [[ reply_to_current ]] / [[ reply_to: 123 ]]).
Tags are stripped before sending; support depends on the current channel config.
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

	// TODO: Update URLs when new domains are ready
	return fmt.Sprintf(`## Documentation
Conduit docs: %s
For Conduit behavior, commands, config, or architecture: consult local docs first.
When diagnosing issues, run %cconduit status%c yourself when possible; only ask the user if you lack access (e.g., sandboxed).
`, docsPath, '`', '`')
}

// buildSilentRepliesSection returns detailed silent reply instructions
func buildSilentRepliesSection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return fmt.Sprintf(`## Silent Replies
When you have nothing to say, respond with ONLY: %s
⚠️ Rules:
- It must be your ENTIRE message — nothing else
- Never append it to an actual response (never include "%s" in real replies)
- Never wrap it in markdown or code blocks
❌ Wrong: "Here's help... %s"
❌ Wrong: "%s"
✅ Right: %s
`, SILENT_REPLY_TOKEN, SILENT_REPLY_TOKEN, SILENT_REPLY_TOKEN, SILENT_REPLY_TOKEN, SILENT_REPLY_TOKEN)
}

// buildHeartbeatsSection returns detailed heartbeat instructions
func buildHeartbeatsSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	heartbeatPrompt := params.HeartbeatPrompt
	if heartbeatPrompt == "" {
		heartbeatPrompt = "Read HEARTBEAT.md if it exists (workspace context). Follow it strictly. Do not infer or repeat old tasks from prior chats. If nothing needs attention, reply HEARTBEAT_OK."
	}

	return fmt.Sprintf(`## Heartbeats
Heartbeat prompt: %s
If you receive a heartbeat poll (a user message matching the heartbeat prompt above), and there is nothing that needs attention, reply exactly:
%s
Conduit treats a leading/trailing "%s" as a heartbeat ack (and may discard it).
If something needs attention, do NOT include "%s"; reply with the alert text instead.
`, heartbeatPrompt, HEARTBEAT_TOKEN, HEARTBEAT_TOKEN, HEARTBEAT_TOKEN)
}

// buildReactionsSection returns reaction guidelines if enabled
func buildReactionsSection(params *SectionParams) string {
	if params.IsMinimal || !params.ReactionsEnabled {
		return ""
	}

	mode := params.ReactionsMode
	if mode == "" {
		mode = "MINIMAL"
	}

	var guidance string
	switch strings.ToUpper(mode) {
	case "ALWAYS":
		guidance = "React freely when appropriate."
	case "MINIMAL":
		guidance = `React ONLY when truly relevant:
- Acknowledge important user requests or confirmations
- Express genuine sentiment (humor, appreciation) sparingly
- Avoid reacting to routine messages or your own replies
Guideline: at most 1 reaction per 5-10 exchanges.`
	default:
		guidance = "React when it feels natural."
	}

	return fmt.Sprintf(`## Reactions
Reactions are enabled for %s in %s mode.
%s
`, params.RuntimeChannel, mode, guidance)
}

// buildConduitCLISection returns CLI quick reference
func buildConduitCLISection(isMinimal bool) string {
	if isMinimal {
		return ""
	}

	return `## Conduit CLI Quick Reference
Conduit is controlled via subcommands. Do not invent commands.
To manage the Gateway daemon service (start/stop/restart):
- conduit status
- conduit server
If unsure, ask the user to run ` + "`conduit help`" + ` (or ` + "`conduit --help`" + `) and paste the output.
`
}

// buildSelfUpdateSection returns gateway self-update instructions
func buildSelfUpdateSection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasGatewayTool := params.AvailableTools["gateway"]
	if !hasGatewayTool {
		return ""
	}

	return `## Conduit Self-Update
Get Updates (self-update) is ONLY allowed when the user explicitly asks for it.
Do not run config.apply or update.run unless the user explicitly requests an update or config change; if it's not explicit, ask first.
Actions: config.get, config.schema, config.apply (validate + write full config, then restart), update.run (update deps or git, then restart).
After restart, Conduit pings the last active session automatically.
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

	for alias, model := range params.ModelAliases {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", alias, model))
	}

	builder.WriteString("\n")
	return builder.String()
}

// buildTimezoneSection returns timezone info
func buildTimezoneSection(params *SectionParams) string {
	if params.UserTimezone == "" {
		return ""
	}

	return fmt.Sprintf(`## Current Date & Time
Time zone: %s
`, params.UserTimezone)
}

// buildRuntimeSection returns runtime context
func buildRuntimeSection(params *SectionParams, runtimeInfo map[string]string) string {
	var parts []string

	if v, ok := runtimeInfo["agent"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("agent=%s", v))
	}
	if v, ok := runtimeInfo["host"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("host=%s", v))
	}
	if v, ok := runtimeInfo["repo"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("repo=%s", v))
	}
	if v, ok := runtimeInfo["os"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("os=%s", v))
	}
	if v, ok := runtimeInfo["node"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("node=%s", v))
	}
	if v, ok := runtimeInfo["model"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("model=%s", v))
	}
	if v, ok := runtimeInfo["channel"]; ok && v != "" {
		parts = append(parts, fmt.Sprintf("channel=%s", v))
	}

	now := time.Now()

	return fmt.Sprintf(`## Runtime
Runtime: %s
Current time: %s
`, strings.Join(parts, " | "), now.Format("Mon 2006-01-02 15:04 MST"))
}
