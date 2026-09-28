package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------- Intelligence (.4) ----------

// generateSuggestions analyzes collected context data and produces actionable suggestions.
// It examines project state, beads issues, and workspace configuration to detect
// common patterns that merit the agent's attention.
func (t *ContextTool) generateSuggestions(data map[string]interface{}) []string {
	var suggestions []string

	// Pattern: many dirty files + stale last commit => suggest committing
	if projData, ok := data["project"].(map[string]interface{}); ok {
		dirty, _ := projData["dirty"].(bool)
		changedFiles, _ := projData["changed_files"].(int)

		if dirty && changedFiles >= 5 {
			suggestions = append(suggestions, fmt.Sprintf(
				"You have %d uncommitted changes. Consider committing or stashing your work.", changedFiles))
		}

		// Detect stale commit (more than 24 hours since last commit while dirty)
		if dirty {
			if tsStr, ok := projData["last_commit_timestamp"].(string); ok && tsStr != "" {
				var ts int64
				if _, err := fmt.Sscanf(tsStr, "%d", &ts); err == nil {
					lastCommit := time.Unix(ts, 0)
					if time.Since(lastCommit) > 24*time.Hour {
						suggestions = append(suggestions, fmt.Sprintf(
							"Last commit was %s ago with uncommitted changes. Consider committing.",
							formatDuration(time.Since(lastCommit))))
					}
				}
			}
		}
	}

	// Pattern: beads with blocked/stalled in-progress tasks
	if beadsData, ok := data["beads"].(map[string]interface{}); ok {
		if issuesData, ok := beadsData["issues"].(map[string]interface{}); ok {
			inProgress, _ := issuesData["in_progress"].(int)
			openCount, _ := issuesData["open"].(int)

			if inProgress > 3 {
				suggestions = append(suggestions, fmt.Sprintf(
					"%d tasks are in-progress. Consider finishing some before starting new work.", inProgress))
			}
			if openCount > 10 {
				suggestions = append(suggestions, fmt.Sprintf(
					"%d open tasks in backlog. Consider prioritizing or triaging.", openCount))
			}
		}
	}

	// Pattern: workspace directory does not exist
	if wsData, ok := data["workspace"].(map[string]interface{}); ok {
		if exists, ok := wsData["workspace_exists"].(bool); ok && !exists {
			suggestions = append(suggestions, "Workspace directory does not exist. Run 'make init' to set up.")
		}
	}

	return suggestions
}

// ---------- Memory Integration (.5) ----------

// gatherMemoryInsights cross-references current context with the memory/search system.
// If a SearchService is available, it queries for related content based on active beads tasks.
func (t *ContextTool) gatherMemoryInsights(ctx context.Context, data map[string]interface{}) []string {
	if t.services.Searcher == nil {
		return nil
	}

	var insights []string

	// Cross-reference in-progress beads tasks with memory
	if beadsData, ok := data["beads"].(map[string]interface{}); ok {
		if issuesData, ok := beadsData["issues"].(map[string]interface{}); ok {
			if openIssues, ok := issuesData["open_issues"].([]map[string]interface{}); ok {
				// Search for the first few in-progress issues to find related context
				searched := 0
				for _, issue := range openIssues {
					if searched >= 3 {
						break
					}
					title, _ := issue["title"].(string)
					id, _ := issue["id"].(string)
					if title == "" {
						continue
					}

					results, err := t.services.Searcher.Search(ctx, title, 2)
					if err != nil || len(results) == 0 {
						continue
					}

					for _, r := range results {
						insights = append(insights, fmt.Sprintf(
							"Task %s (%s): related content found in %s", id, title, r.Source))
					}
					searched++
				}
			}
		}
	}

	return insights
}

// ---------- Output Formatting (.6) ----------

// formatSuggestions renders the suggestions and memory insights section with rich formatting.
func (t *ContextTool) formatSuggestions(suggestions []string, memoryInsights []string) string {
	var builder strings.Builder

	if len(suggestions) > 0 {
		builder.WriteString("## Suggestions\n\n")
		for _, s := range suggestions {
			builder.WriteString(fmt.Sprintf("- **Action:** %s\n", s))
		}
		builder.WriteString("\n")
	}

	if len(memoryInsights) > 0 {
		builder.WriteString("## Memory Insights\n\n")
		for _, insight := range memoryInsights {
			builder.WriteString(fmt.Sprintf("- %s\n", insight))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}
