package gateway

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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
	currentProvider := session.Context["provider"]
	if currentProvider == "" {
		currentProvider = "default"
	}
	sb.WriteString(fmt.Sprintf("Provider: %s\n", currentProvider))
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

// formatCostResponse builds a detailed cost breakdown showing session cost,
// per-provider token usage, per-model summary, and cache efficiency.
func formatCostResponse(session *sessions.Session, usageTracker *ai.UsageTracker) string {
	var sb strings.Builder

	sb.WriteString("Cost Report\n")

	// Session cost
	if session != nil && session.Context != nil {
		costStr := session.Context["session_total_cost"]
		countStr := session.Context["session_request_count"]
		cost, _ := strconv.ParseFloat(costStr, 64)
		count, _ := strconv.Atoi(countStr)
		sb.WriteString("\nSession Cost\n")
		sb.WriteString(fmt.Sprintf("  Requests: %d\n", count))
		sb.WriteString(fmt.Sprintf("  Cost:     $%.4f\n", cost))
	}

	// Global usage
	if usageTracker == nil {
		sb.WriteString("\nNo global usage data yet.")
		return sb.String()
	}

	snapshot := usageTracker.GetSnapshot()
	if len(snapshot.Providers) == 0 {
		sb.WriteString("\nNo global usage data yet.")
		return sb.String()
	}

	uptime := formatDuration(snapshot.Snapshot.Sub(snapshot.Since))
	sb.WriteString(fmt.Sprintf("\nGlobal Cost (uptime: %s)\n", uptime))

	// Per-provider breakdown, sorted alphabetically
	providerNames := make([]string, 0, len(snapshot.Providers))
	for name := range snapshot.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	var totalCost float64
	for _, name := range providerNames {
		pr := snapshot.Providers[name]
		totalCost += pr.TotalCost

		sb.WriteString(fmt.Sprintf("\n  Provider: %s\n", name))
		sb.WriteString("  ──────────────────────────────────\n")
		sb.WriteString(fmt.Sprintf("  Requests:      %s\n", formatNumber(int(pr.TotalRequests))))
		sb.WriteString(fmt.Sprintf("  Input tokens:  %s\n", formatNumber(int(pr.TotalInputTokens))))
		sb.WriteString(fmt.Sprintf("  Output tokens: %s\n", formatNumber(int(pr.TotalOutputTokens))))

		// Cache lines only when there is cache activity
		if pr.TotalCacheWriteTokens > 0 || pr.TotalCacheReadTokens > 0 {
			sb.WriteString(fmt.Sprintf("  Cache writes:  %s\n", formatNumber(int(pr.TotalCacheWriteTokens))))
			sb.WriteString(fmt.Sprintf("  Cache reads:   %s\n", formatNumber(int(pr.TotalCacheReadTokens))))
			if pr.CacheSavings > 0 {
				sb.WriteString(fmt.Sprintf("  Cache savings: $%.2f\n", pr.CacheSavings))
			}
		}

		if pr.ErrorCount > 0 {
			sb.WriteString(fmt.Sprintf("  Errors:        %s\n", formatNumber(int(pr.ErrorCount))))
		}

		sb.WriteString(fmt.Sprintf("  Cost:          $%.4f\n", pr.TotalCost))
	}

	// Per-model summary sorted by cost descending
	if len(snapshot.Models) > 0 {
		type modelEntry struct {
			name         string
			requests     int64
			cost         float64
			cacheHitRate float64
		}
		models := make([]modelEntry, 0, len(snapshot.Models))
		for _, mr := range snapshot.Models {
			models = append(models, modelEntry{
				name:         mr.Model,
				requests:     mr.TotalRequests,
				cost:         mr.TotalCost,
				cacheHitRate: mr.CacheHitRate,
			})
		}
		sort.Slice(models, func(i, j int) bool {
			return models[i].cost > models[j].cost
		})

		sb.WriteString("\n  Models\n")
		for _, m := range models {
			line := fmt.Sprintf("    %-36s %4d reqs  $%.4f", m.name, m.requests, m.cost)
			if m.cacheHitRate > 0 {
				line += fmt.Sprintf("  cache: %.1f%%", m.cacheHitRate*100)
			}
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString(fmt.Sprintf("\n  Total: $%.4f", totalCost))

	return sb.String()
}

// formatDuration formats a duration as a human-friendly string like "3h 27m" or "2d 5h".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

// contextWarningThresholds defines the percentage levels at which proactive warnings fire.
// Each threshold fires only once per session (tracked in session context).
var contextWarningThresholds = []struct {
	pct  float64
	key  string // session context key to track if this warning was sent
	icon string
	msg  string
}{
	{80, "context_warned_80", "🔴", "Context window is 80%+ full — quality is degrading. Use /reset to start a fresh session."},
	{60, "context_warned_60", "🟡", "Context window is over 60% full. Consider /reset soon to keep responses sharp."},
}

// ContextWarning holds a proactive warning to append to a response.
type ContextWarning struct {
	Text string // formatted warning to append (empty = no warning)
	Key  string // session context key to set to "true" after sending
}

// contextWarningIfNeeded checks prompt token usage against the context window and returns
// a warning to append to the response. Returns empty Text if no warning needed.
// Each threshold fires only once per session to avoid nagging.
// The caller is responsible for persisting Key via SetSessionContext after sending.
func contextWarningIfNeeded(session *sessions.Session, promptTokens int, model string) ContextWarning {
	if session == nil || promptTokens == 0 {
		return ContextWarning{}
	}

	contextWindow := ai.ContextWindowForModel(model)
	if contextWindow == 0 {
		return ContextWarning{}
	}

	pct := float64(promptTokens) / float64(contextWindow) * 100

	for _, t := range contextWarningThresholds {
		if pct >= t.pct {
			// Check if we already warned at this level
			if session.Context != nil && session.Context[t.key] == "true" {
				return ContextWarning{}
			}
			text := fmt.Sprintf("\n\n%s **Context Warning:** %s (%.0f%% of %sk tokens used)", t.icon, t.msg, pct, formatNumber(contextWindow/1000))
			return ContextWarning{Text: text, Key: t.key}
		}
	}

	return ContextWarning{}
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
