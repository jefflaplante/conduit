package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/brain"
)

// situationCategory represents one category of Situation Awareness data with its
// priority (lower = more important) and token budget weight.
type situationCategory struct {
	header   string
	priority int // 1=highest, 7=lowest
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
	timeCtx := computeTimeContextAt(params.UserTimezone, params.now(), params.QuietHours)

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
		// conduit-31jg.54: SPAR pivot/circular patterns that recurred across
		// sessions, consolidated nightly by REM Reflect.
		{"reflect.tools.", "Tool Pitfalls", 5},
		{"reflect.clusters.", "Pattern Clusters", 6},
		{"sense.briefing.", "Daily Briefing", 7},
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
