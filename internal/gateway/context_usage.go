package gateway

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

// formatContextUsage reads token usage from a session's context and formats
// a human-readable summary including percentages of the context window.
func formatContextUsage(session *sessions.Session) string {
	if session == nil || session.Context == nil {
		return "No context usage data available yet. Send a message first."
	}

	promptStr := session.Context["last_prompt_tokens"]
	completionStr := session.Context["last_completion_tokens"]
	totalStr := session.Context["last_total_tokens"]

	if promptStr == "" && completionStr == "" && totalStr == "" {
		return "No context usage data available yet. Send a message first."
	}

	prompt, _ := strconv.Atoi(promptStr)
	completion, _ := strconv.Atoi(completionStr)
	total, _ := strconv.Atoi(totalStr)

	// Determine model and context window
	model := session.Context["model"]
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	contextWindow := ai.ContextWindowForModel(model)

	// Calculate percentages
	promptPct := float64(prompt) / float64(contextWindow) * 100
	projectedPct := float64(total) / float64(contextWindow) * 100

	// Build the response
	result := fmt.Sprintf("Context Window Usage\n\n"+
		"Prompt tokens:     %s (%.1f%%)\n"+
		"Completion tokens: %s\n"+
		"Total tokens:      %s (%.1f%%)\n"+
		"Context window:    %s\n"+
		"Model:             %s",
		formatNumber(prompt), promptPct,
		formatNumber(completion),
		formatNumber(total), projectedPct,
		formatNumber(contextWindow),
		model,
	)

	// Add a warning if getting close to the limit
	if projectedPct >= 80 {
		result += "\n\nWarning: Context window is nearly full. Consider using /reset to start fresh."
	} else if projectedPct >= 50 {
		result += "\n\nNote: Context window is over half full."
	}

	return result
}

// formatStatusResponse builds the full /status response including session info,
// cost data, context window usage, and global usage stats.
func formatStatusResponse(session *sessions.Session, messageCount int, usageTracker *ai.UsageTracker) string {
	var sb strings.Builder

	sb.WriteString("Session Status\n\n")

	// Session info
	sb.WriteString(fmt.Sprintf("Session:  %s\n", session.Key))
	sb.WriteString(fmt.Sprintf("Messages: %d\n", messageCount))
	currentModel := session.Context["model"]
	if currentModel == "" {
		currentModel = "sonnet (default)"
	}
	sb.WriteString(fmt.Sprintf("Model:    %s\n", currentModel))
	sb.WriteString(fmt.Sprintf("User:     %s\n", session.UserID))

	// Session cost
	costStr := session.Context["session_total_cost"]
	countStr := session.Context["session_request_count"]
	if costStr != "" || countStr != "" {
		cost, _ := strconv.ParseFloat(costStr, 64)
		count, _ := strconv.Atoi(countStr)
		sb.WriteString("\nSession Cost\n")
		sb.WriteString(fmt.Sprintf("Requests: %d\n", count))
		sb.WriteString(fmt.Sprintf("Cost:     $%.4f\n", cost))
	}

	// Context window usage (embed existing helper's logic)
	sb.WriteString("\n")
	sb.WriteString(formatContextUsage(session))

	// Global usage (from UsageTracker, if available)
	if usageTracker != nil {
		snapshot := usageTracker.GetSnapshot()
		if len(snapshot.Providers) > 0 {
			sb.WriteString("\n\nGlobal Usage (this uptime)\n")
			sb.WriteString(fmt.Sprintf("%-12s %-10s %s\n", "Provider", "Requests", "Cost"))

			var totalCost float64
			providers := make([]string, 0, len(snapshot.Providers))
			for name := range snapshot.Providers {
				providers = append(providers, name)
			}
			sort.Strings(providers)
			for _, name := range providers {
				pr := snapshot.Providers[name]
				sb.WriteString(fmt.Sprintf("%-12s %-10s $%.2f\n", name, formatNumber(int(pr.TotalRequests)), pr.TotalCost))
				totalCost += pr.TotalCost
			}
			sb.WriteString(fmt.Sprintf("%-12s %-10s $%.2f", "Total:", "", totalCost))
		}
	}

	return sb.String()
}

// formatNumber formats an integer with comma separators (e.g. 84521 -> "84,521").
func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}

	// Insert commas from the right
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
