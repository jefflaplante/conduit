package statehook

import (
	"strings"

	"conduit/internal/ai"
)

// Scheduler session key prefixes (mirrors gateway.scheduledSessionPrefixes;
// duplicated as literals to keep this package gateway-free). Sessions with
// these keys are throwaway runs whose state writes would be noise: cron
// jobs, heartbeats and sub-agents are excluded by IsValidSessionKey.
var excludedKeyPrefixes = []string{"cron_", "heartbeat_", "subagent_"}

// IsValidSessionKey reports whether a session is a "main" interactive
// session the hook may write state from (memo §7.1: exclude scheduler and
// sub-agent sessions entirely). Case-insensitive on the prefixes; a bare
// prefix ("cron") counts as a scheduler key.
func IsValidSessionKey(key string) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	lower := strings.ToLower(key)
	for _, p := range excludedKeyPrefixes {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	// Exact bare-prefix keys (e.g. "subagent") are scheduler-owned too.
	switch lower {
	case "cron", "heartbeat", "subagent":
		return false
	}
	return true
}

// BuildDistillMessages assembles the Path B one-shot request: a system
// message with the distill contract and a user message carrying the
// transcript tail ("role: content" lines, newest last).
func BuildDistillMessages(transcript []string) []ai.ChatMessage {
	var b strings.Builder
	b.WriteString("Transcript tail of the session (oldest first, format role: content):\n")
	for _, ln := range transcript {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if len(transcript) == 0 {
		b.WriteString("(transcript unavailable)\n")
	}
	return []ai.ChatMessage{
		{Role: "system", Content: DistillStatePrompt("idle-session")},
		{Role: "user", Content: b.String()},
	}
}
