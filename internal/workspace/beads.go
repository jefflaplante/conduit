package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// BeadEntry represents a single bead (task) from the br CLI JSON output.
type BeadEntry struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

// BeadsSummary holds the parsed result of querying active beads.
type BeadsSummary struct {
	Count   int
	Entries []BeadEntry
	Summary string // Formatted summary string ready for Brain storage
}

// priorityLabel maps numeric priority to a human-readable label.
func priorityLabel(p int) string {
	switch p {
	case 0:
		return "P0"
	case 1:
		return "P1"
	case 2:
		return "P2"
	case 3:
		return "P3"
	case 4:
		return "P4"
	default:
		return fmt.Sprintf("P%d", p)
	}
}

// QueryActiveBeads shells out to `br list` to get active beads (tasks) and
// returns a formatted summary. Returns nil if br is not available, returns an
// error, or there are no active tasks. This function is designed to be
// best-effort: it never blocks longer than the context timeout and handles all
// error cases gracefully.
func QueryActiveBeads(ctx context.Context) (*BeadsSummary, error) {
	// Check if br is on PATH
	brPath, err := exec.LookPath("br")
	if err != nil {
		return nil, fmt.Errorf("br not found on PATH: %w", err)
	}

	// Use a short timeout to avoid blocking session startup
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, brPath,
		"list",
		"--status=open",
		"--status=in_progress",
		"--format=json",
		"--no-auto-flush",
		"--no-auto-import",
		"--quiet",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("br list failed: %w", err)
	}

	return ParseBeadsJSON(output)
}

// ParseBeadsJSON parses the JSON output from `br list --format=json` and
// builds a formatted summary string. Exported for testing.
func ParseBeadsJSON(data []byte) (*BeadsSummary, error) {
	data = trimToJSON(data)
	if len(data) == 0 {
		return &BeadsSummary{Count: 0, Summary: ""}, nil
	}

	var entries []BeadEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse br JSON output: %w", err)
	}

	if len(entries) == 0 {
		return &BeadsSummary{Count: 0, Summary: ""}, nil
	}

	return FormatBeadsSummary(entries), nil
}

// trimToJSON finds the first '[' and last ']' in the byte slice to handle
// any non-JSON output before/after the array (e.g. warnings on stderr).
func trimToJSON(data []byte) []byte {
	start := -1
	end := -1
	for i, b := range data {
		if b == '[' {
			start = i
			break
		}
	}
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == ']' {
			end = i
			break
		}
	}
	if start == -1 || end == -1 || end <= start {
		return nil
	}
	return data[start : end+1]
}

// FormatBeadsSummary builds a BeadsSummary from parsed bead entries.
// Sorts by priority (lowest number = highest priority), then by status
// (in_progress before open).
func FormatBeadsSummary(entries []BeadEntry) *BeadsSummary {
	if len(entries) == 0 {
		return &BeadsSummary{Count: 0, Summary: ""}
	}

	// Sort: priority ascending (P0 first), then in_progress before open
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Priority != entries[j].Priority {
			return entries[i].Priority < entries[j].Priority
		}
		// in_progress sorts before open
		if entries[i].Status != entries[j].Status {
			return entries[i].Status == "in_progress"
		}
		return entries[i].Title < entries[j].Title
	})

	// Build concise summary: "N active tasks: [title (P1, in_progress), ...]"
	// Limit to top 10 to keep prompt concise
	displayEntries := entries
	truncated := false
	if len(displayEntries) > 10 {
		displayEntries = displayEntries[:10]
		truncated = true
	}

	var parts []string
	for _, e := range displayEntries {
		title := e.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", title, priorityLabel(e.Priority), e.Status))
	}

	summary := fmt.Sprintf("%d active tasks: %s", len(entries), strings.Join(parts, "; "))
	if truncated {
		summary += fmt.Sprintf(" (+%d more)", len(entries)-10)
	}

	return &BeadsSummary{
		Count:   len(entries),
		Entries: entries,
		Summary: summary,
	}
}

// RefreshBeadsBrainEntry queries active beads and stores the summary in Brain
// under the key "sense.tasks.active". The brainStore function is a callback
// that writes to Brain, decoupling this from a direct Brain import.
//
// This is designed to be called:
//   - Once at gateway startup
//   - Periodically in a background goroutine (e.g. every 5 minutes)
//
// It is always best-effort: errors are logged but never propagated.
func RefreshBeadsBrainEntry(ctx context.Context, brainStore func(ctx context.Context, key, value, source string) error) {
	result, err := QueryActiveBeads(ctx)
	if err != nil {
		log.Printf("[Beads] skipping brain update: %v", err)
		return
	}

	if result.Count == 0 {
		log.Printf("[Beads] no active tasks found, clearing sense.tasks.active")
		// Store empty to clear stale data
		if err := brainStore(ctx, "sense.tasks.active", "No active tasks", "system"); err != nil {
			log.Printf("[Beads] failed to clear brain entry: %v", err)
		}
		return
	}

	if err := brainStore(ctx, "sense.tasks.active", result.Summary, "system"); err != nil {
		log.Printf("[Beads] failed to store brain entry: %v", err)
		return
	}

	log.Printf("[Beads] updated sense.tasks.active: %d active tasks", result.Count)
}
