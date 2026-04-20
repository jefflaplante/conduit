package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"conduit/internal/ai"
	"conduit/internal/brain"
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

// buildMemorySection returns memory recall instructions if memory tools are available
func buildMemorySection(params *SectionParams) string {
	if params.IsMinimal {
		return ""
	}

	hasMemorySearch := params.AvailableTools["MemorySearch"]
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

	hasWriteTool := params.AvailableTools["Write"]
	hasBashTool := params.AvailableTools["Bash"]
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

	if params.AvailableTools["StatusUpdate"] {
		builder.WriteString(`
### Progress Updates
During multi-step or long-running tasks, use StatusUpdate to keep the user informed:
- At major phases: "Searching codebase for authentication files..."
- After findings: "Found 5 matching files, analyzing patterns..."
- When pivoting: "Initial search found nothing, trying broader query..."
Keep updates concise (1 sentence). Don't spam — roughly every 3-5 tool calls or when something meaningful happens.
`)
	}

	if params.AvailableTools["Message"] {
		channelOptions := strings.Join(SupportedChannels, "|")
		if len(params.MessageChannels) > 0 {
			channelOptions = strings.Join(params.MessageChannels, "|")
		}

		builder.WriteString(fmt.Sprintf(`
### message tool
- Use %cmessage%c for proactive sends + channel actions (polls, reactions, etc.).
- For %caction=send%c, include %ctarget%c and %cmessage%c.
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
It must be your entire message — no other text, no markdown wrapping, no code blocks.
`, SILENT_REPLY_TOKEN)
}

// buildWakeContextSection emits a short P1 note telling the LLM that this turn was
// triggered by a session wake (e.g. a sub-agent callback or another inter-session
// message) rather than a fresh user message. When wakeSource is empty this returns
// the empty string so the section vanishes on normal turns.
func buildWakeContextSection(wakeSource string) string {
	if wakeSource == "" {
		return ""
	}
	switch wakeSource {
	case "sub_agent_announced":
		return fmt.Sprintf(`## Wake Context
This turn was triggered by a sub-agent callback. The "user" message is the sub-agent's result delivered back to this session. The raw text has ALREADY been posted to the human's channel — they have seen it.
Decide whether any follow-up action or commentary is warranted. If not, reply with %s — do NOT repeat the sub-agent's output back to the human.
`, SILENT_REPLY_TOKEN)
	case "sub_agent_silent":
		return fmt.Sprintf(`## Wake Context
This turn was triggered by a sub-agent callback. The "user" message is the sub-agent's result. The human has NOT seen this output — you are the only path for it to reach them.
If the result is useful to the human, summarize or relay it now. Use %s only when the result is genuinely not worth surfacing.
`, SILENT_REPLY_TOKEN)
	case "sub_agent_callback":
		// Generic sub-agent callback (legacy / unknown announce state). Err toward surfacing.
		return fmt.Sprintf(`## Wake Context
This turn was triggered by a sub-agent callback — the "user" message is the sub-agent's result delivered back to this session, not a new human request.
React appropriately: if the result requires follow-up action or is worth reporting to the human, respond. If it's purely informational and the human has already seen it, reply with %s.
`, SILENT_REPLY_TOKEN)
	case "inter_session":
		return fmt.Sprintf(`## Wake Context
This turn was triggered by another session sending a message to this one. Treat the "user" message as an inter-session callback, not a direct human request.
If follow-up is warranted, respond. Otherwise reply with %s.
`, SILENT_REPLY_TOKEN)
	case "heartbeat":
		return `## Wake Context
This turn was triggered by a heartbeat. Follow heartbeat response rules below.
`
	default:
		return fmt.Sprintf(`## Wake Context
This turn was triggered by a session wake (source: %q). The incoming "user" message is not a direct human request; decide whether follow-up is warranted.
`, wakeSource)
	}
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

	for alias, model := range params.ModelAliases {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", alias, model))
	}

	builder.WriteString("\n")
	return builder.String()
}

// buildRuntimeSection returns runtime context
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

	now := time.Now()
	if params.UserTimezone != "" {
		if loc, err := time.LoadLocation(params.UserTimezone); err == nil {
			now = now.In(loc)
		}
	}

	return fmt.Sprintf(`## Runtime
Runtime: %s
Current time: %s
`, strings.Join(parts, " | "), now.Format("Mon 2006-01-02 15:04 MST"))
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

// buildCronDeliverySection returns instructions for cron/scheduled job output delivery.
// This is injected only for sessions with a "cron_" prefix to ensure scheduled jobs
// use the Message tool and do not attempt shell-based delivery.
func buildCronDeliverySection(params *SectionParams) string {
	if !params.AvailableTools["Message"] {
		return ""
	}

	return `## Cron Delivery
You are running as a scheduled job with no interactive session.
Deliver output ONLY via Message(action="send", target="<chat_id>"). Shell commands cannot reach the user.
If nothing to report, do not send a message.
`
}

// buildBrainSection returns cognitive architecture instructions if the Brain tool is available.
func buildBrainSection(params *SectionParams) string {
	if params.IsMinimal || !params.AvailableTools["Brain"] {
		return ""
	}

	return `## Brain (Cognitive Architecture)
You have a tiered memory system beyond the context window. USE IT.

### Lookup-First Pattern
Before reading any file for a fact you've accessed before this session:
1. ` + "`Brain(action=\"get\", key=\"likely.key.name\")`" + ` — check if it's cached
2. Hit? Use it. Done. Miss? Read the file, then cache the key fact:
   ` + "`Brain(action=\"store\", key=\"solar.panel_count\", value=\"30\", tier=\"working\")`" + `

### What Goes Where

| Tier | What | Examples | Lifetime |
|------|------|----------|----------|
| **longterm** | Core facts, stable preferences, learned patterns | jeff.birthday, solar.panel_count, pets.theo.breed | Survives restarts |
| **working** | Session-extracted facts, current task state | solar.today.production, email.unread_count | Session only (promote if important) |
| **scratch** | Intermediate calculations, temp values | push/pop only | Seconds |

### Key Naming Convention
Use dot-separated namespaces: ` + "`domain.subject.attribute`" + `
- ` + "`jeff.birthday`" + `, ` + "`jeff.favorite_color`" + `
- ` + "`solar.today.production`" + `, ` + "`solar.panel_count`" + `
- ` + "`pets.theo.breed`" + `, ` + "`session.current_topic`" + `

### When to Store
- After reading a file: cache the 2-3 key facts you extracted
- After a tool call returns useful data: cache the summary
- When the user states a fact or preference: store immediately
- When you compute something you might need again: working memory

### When to Promote (working → longterm)
- Facts true across sessions (birthdays, counts, preferences)
- Learned patterns ("user prefers X over Y")
- Infrastructure facts ("solar system has 30 panels")

### When NOT to Store
- Entire file contents (that's what files are for)
- Conversational context (that's what the context window is for)
- One-time responses (just respond, don't cache)

### Consolidation
At session end or handoff, call ` + "`Brain(action=\"consolidate\")`" + ` to auto-promote high-salience working memory to longterm, flush changes to disk, and report what was promoted/evicted.

### Searching
` + "`Brain(action=\"recall\", query=\"solar\")`" + ` searches all tiers by key name and value content. Results return with tier and salience so you know how fresh/reliable each fact is.
`
}

// situationCategory represents one category of Situation Awareness data with its
// priority (lower = more important) and token budget weight.
type situationCategory struct {
	header   string
	priority int // 1=highest, 6=lowest
	entries  []*brain.Entry
}

// buildSituationAwareness builds the Situation Awareness prompt section from
// Brain reflection data and computed time context. Returns empty string if no
// data is available.
func (pb *PromptBuilder) buildSituationAwareness(ctx context.Context, params *SectionParams) string {
	if params.IsMinimal || pb.brainService == nil {
		return ""
	}

	// Query all categories from Brain. Each List call returns entries sorted by
	// key; we re-sort by salience within each category later.
	categories := pb.querySituationCategories(ctx)

	// Compute time context (always available, independent of Brain data).
	timeCtx := computeTimeContext(params.UserTimezone)

	// Check if there's any data at all (besides time context).
	hasData := false
	for _, cat := range categories {
		if len(cat.entries) > 0 {
			hasData = true
			break
		}
	}
	if !hasData {
		return ""
	}

	// Sort entries within each category by salience (highest first).
	for _, cat := range categories {
		sort.Slice(cat.entries, func(i, j int) bool {
			return cat.entries[i].Salience > cat.entries[j].Salience
		})
	}

	// Build section with token budget enforcement (~500 tokens ≈ 2000 chars).
	const budgetChars = 2000
	var builder strings.Builder
	builder.WriteString("## Situation Awareness\n")

	// Add time context first (cheap, always useful).
	if timeCtx != "" {
		builder.WriteString(timeCtx)
		builder.WriteString("\n")
	}

	// Sort categories by priority (ascending = most important first).
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].priority < categories[j].priority
	})

	// Render categories within budget.
	for _, cat := range categories {
		if len(cat.entries) == 0 {
			continue
		}
		rendered := renderCategory(cat)
		if builder.Len()+len(rendered) > budgetChars {
			// Try adding a truncated version (header + first entry only).
			truncated := renderCategoryTruncated(cat)
			if builder.Len()+len(truncated) <= budgetChars {
				builder.WriteString(truncated)
			}
			// Either way, stop adding more categories.
			break
		}
		builder.WriteString(rendered)
	}

	result := strings.TrimSpace(builder.String())
	// If we only produced the header with no content, return empty.
	if result == "## Situation Awareness" {
		return ""
	}
	return result
}

// querySituationCategories queries Brain for all Situation Awareness data.
func (pb *PromptBuilder) querySituationCategories(ctx context.Context) []*situationCategory {
	type prefixDef struct {
		prefix   string
		header   string
		priority int
	}

	defs := []prefixDef{
		{"reflect.patterns.", "Confirmed Patterns", 1},
		{"sense.tasks.", "Active Work", 2},
		{"sense.alerts.", "Recent Alerts", 3},
		{"reflect.learned.", "Learned Patterns", 4},
		{"reflect.clusters.", "Pattern Clusters", 5},
		{"sense.briefing.", "Daily Briefing", 6},
	}

	categories := make([]*situationCategory, 0, len(defs))
	for _, d := range defs {
		entries, err := pb.brainService.List(ctx, d.prefix, "")
		if err != nil {
			continue
		}
		categories = append(categories, &situationCategory{
			header:   d.header,
			priority: d.priority,
			entries:  entries,
		})
	}
	return categories
}

// renderCategory renders a full category with header and bullet points.
func renderCategory(cat *situationCategory) string {
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(cat.header)
	b.WriteString("\n")
	for _, e := range cat.entries {
		// Use value as the display text; truncate long values.
		val := e.Value
		if len(val) > 200 {
			val = val[:197] + "..."
		}
		b.WriteString("- ")
		b.WriteString(val)
		b.WriteString("\n")
	}
	return b.String()
}

// renderCategoryTruncated renders a category with only the first entry.
func renderCategoryTruncated(cat *situationCategory) string {
	if len(cat.entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(cat.header)
	b.WriteString("\n")
	val := cat.entries[0].Value
	if len(val) > 200 {
		val = val[:197] + "..."
	}
	b.WriteString("- ")
	b.WriteString(val)
	b.WriteString("\n")
	if len(cat.entries) > 1 {
		b.WriteString(fmt.Sprintf("  (%d more)\n", len(cat.entries)-1))
	}
	return b.String()
}

// computeTimeContext produces a compact time-awareness line.
// It complements the Runtime section's timestamp with contextual hints.
func computeTimeContext(timezone string) string {
	now := time.Now()
	if timezone != "" {
		if loc, err := time.LoadLocation(timezone); err == nil {
			now = now.In(loc)
		}
	}

	dayOfWeek := now.Weekday().String()
	hour := now.Hour()

	var period string
	switch {
	case hour >= 5 && hour < 12:
		period = "morning"
	case hour >= 12 && hour < 17:
		period = "afternoon"
	case hour >= 17 && hour < 21:
		period = "evening"
	default:
		period = "late night"
	}

	isWeekend := now.Weekday() == time.Saturday || now.Weekday() == time.Sunday

	// Quiet hours heuristic: 23:00-08:00 (matches default config).
	isQuiet := hour >= 23 || hour < 8

	var parts []string
	parts = append(parts, fmt.Sprintf("%s %s", dayOfWeek, period))
	if isWeekend {
		parts = append(parts, "weekend")
	}
	if isQuiet {
		parts = append(parts, "quiet hours")
	}

	return fmt.Sprintf("Time context: %s", strings.Join(parts, " | "))
}
