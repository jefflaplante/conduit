package agent

import (
	"fmt"
	"strings"
	"time"

	"conduit/internal/config"
)

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
	case "sub_agent_canceled":
		// conduit-38cz: SessionsCancel ended a sub-agent this session spawned.
		return fmt.Sprintf(`## Wake Context
This turn was triggered because a sub-agent this session spawned was CANCELED (the "user" message says by whom). Its task did not complete. Do not respawn it unless the cancel was clearly not intended; if the human is waiting on that work, tell them it was stopped. Otherwise reply with %s.
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

// buildTimeContextSection returns the current timestamp. This section is
// registered LAST (P4) so it renders at the very end of the system prompt:
// everything above it is byte-stable between calls, keeping the provider
// prefix cache warm. Do not move it earlier in the section list.
func buildTimeContextSection(params *SectionParams) string {
	now := params.now()
	if params.UserTimezone != "" {
		if loc, err := time.LoadLocation(params.UserTimezone); err == nil {
			now = now.In(loc)
		}
	}

	return fmt.Sprintf(`## Time Context
Current time: %s
`, now.Format("Mon 2006-01-02 15:04 MST"))
}

// computeTimeContext produces a compact time-awareness line.
// It complements the Runtime section's timestamp with contextual hints.
func computeTimeContext(timezone string) string {
	return computeTimeContextAt(timezone, time.Now(), nil)
}

// computeTimeContextAt is computeTimeContext for a given instant. quiet, if
// non-nil, is the configured quiet window (evaluated in its own timezone via
// config/quiet_hours.go); nil falls back to the 23:00-08:00 heuristic in
// timezone. conduit-31jg.60
func computeTimeContextAt(timezone string, now time.Time, quiet *config.AgentHeartbeatConfig) string {
	instant := now
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

	// conduit-31jg.60: use the configured quiet window when available;
	// otherwise the legacy heuristic 23:00-08:00 (the default config).
	var isQuiet bool
	if quiet != nil {
		isQuiet = quiet.IsQuietTime(instant)
	} else {
		isQuiet = hour >= 23 || hour < 8
	}

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
