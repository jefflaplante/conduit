package core

import (
	"fmt"
	"strings"
)

// formatProviderSlots renders the fuel gauge's provider_slots
// (conduit-38cz) for SessionStatus: per provider/model concurrency pool,
// calls in flight and waiting against the limit.
func formatProviderSlots(gauge map[string]interface{}) string {
	slots, ok := gauge["provider_slots"].([]map[string]interface{})
	if !ok || len(slots) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("  Provider slots:\n")
	for _, s := range slots {
		name := fmt.Sprint(s["provider"])
		if m, _ := s["model"].(string); m != "" {
			name += "/" + m
		}
		limit := fmt.Sprint(s["limit"])
		if limit == "0" {
			limit = "unlimited"
		}
		fmt.Fprintf(&b, "    %s: %v in flight, %v waiting (limit %s)\n", name, s["in_flight"], s["waiting"], limit)
	}
	return b.String()
}

// formatSubAgents renders GetSessionStatus's sub_agents (conduit-38cz):
// the sub-agents this session spawned and how each ended.
func formatSubAgents(status map[string]interface{}) string {
	subs, ok := status["sub_agents"].([]map[string]interface{})
	if !ok || len(subs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nSub-agents:\n")
	for _, s := range subs {
		name := fmt.Sprint(s["session_key"])
		if l, _ := s["label"].(string); l != "" {
			name += fmt.Sprintf(" (%q)", l)
		}
		fmt.Fprintf(&b, "  %s: %v", name, s["status"])
		if by, _ := s["canceled_by"].(string); by != "" {
			fmt.Fprintf(&b, " by %s", by)
		}
		if task, _ := s["task"].(string); task != "" {
			fmt.Fprintf(&b, " — %s", task)
		}
		b.WriteString("\n")
	}
	return b.String()
}
