// Convert recall-events.jsonl to brain_spread feed_selection format.
// Usage: go run cmd/recall-events-converter/main.go
// Output is JSON to stdout, suitable for piping to brain_spread feed_selection.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type recallEvent struct {
	Timestamp string   `json:"ts"`
	Query     string   `json:"query"`
	Keys      []string `json:"keys"`
	Tiers     []string `json:"tiers"`
}

type feedSelection struct {
	Type      string   `json:"type"`
	Key       string   `json:"key"`
	Neighbors []string `json:"neighbors"`
}

const defaultHoursAgo = 24

// defaultEventsFile is a var (not const) so tests can redirect it.
var defaultEventsFile = "/home/jules/ocgo/workspace/memory/recall-events.jsonl"

func main() {
	eventsFile := flag.String("file", defaultEventsFile, "Path to recall-events.jsonl")
	hoursAgo := flag.Int("hours", defaultHoursAgo, "Process events from last N hours")
	flag.Parse()

	if err := convertEvents(*eventsFile, *hoursAgo); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func convertEvents(eventsFile string, hoursAgo int) error {
	// Calculate cutoff time
	cutoff := time.Now().Add(-time.Duration(hoursAgo) * time.Hour)

	// Open events file
	file, err := os.Open(eventsFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No events file is OK - just return empty array
			fmt.Println("[]")
			return nil
		}
		return fmt.Errorf("failed to open events file: %w", err)
	}
	defer file.Close()

	var selections []feedSelection
	scanner := bufio.NewScanner(file)
	seen := make(map[string]bool) // Deduplicate by key

	for scanner.Scan() {
		var event recallEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			// Skip malformed lines
			continue
		}

		// Parse timestamp
		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			// Skip events with invalid timestamps
			continue
		}

		// Skip events older than cutoff
		if ts.Before(cutoff) {
			continue
		}

		// Only process LTM entries (skip working memory)
		for i, key := range event.Keys {
			if i >= len(event.Tiers) {
				continue
			}
			if event.Tiers[i] != "longterm" {
				continue
			}

			// Deduplicate: only process each key once
			if seen[key] {
				continue
			}
			seen[key] = true

			// For feed_selection, we need neighbors. In a real implementation,
			// we'd query the brain graph to find actual neighbors. For now,
			// use the other keys from the same recall result as a proxy.
			neighbors := make([]string, 0, len(event.Keys)-1)
			for j, otherKey := range event.Keys {
				if i != j && j < len(event.Tiers) && event.Tiers[j] == "longterm" {
					neighbors = append(neighbors, otherKey)
				}
			}

			selections = append(selections, feedSelection{
				Type:      "selected",
				Key:       key,
				Neighbors: neighbors,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read events file: %w", err)
	}

	// Output as JSON array
	output, err := json.MarshalIndent(selections, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal selections: %w", err)
	}
	fmt.Println(string(output))

	// Optionally truncate processed events after successful conversion
	if len(selections) > 0 {
		truncateEvents(eventsFile, cutoff)
	}

	return nil
}

func truncateEvents(eventsFile string, cutoff time.Time) {
	// Read all events
	file, err := os.Open(eventsFile)
	if err != nil {
		return // Best-effort
	}
	defer file.Close()

	var recentEvents []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event recallEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}

		ts, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			continue
		}

		// Keep only recent events (within last 2 hours)
		if ts.After(cutoff.Add(-time.Duration(2) * time.Hour)) {
			recentEvents = append(recentEvents, scanner.Text())
		}
	}

	if err := scanner.Err(); err != nil {
		return // Best-effort
	}

	// Write back only recent events
	if len(recentEvents) > 0 {
		parentDir := filepath.Dir(eventsFile)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return
		}

		tmpFile := eventsFile + ".tmp"
		f, err := os.Create(tmpFile)
		if err != nil {
			return
		}
		defer f.Close()

		for _, line := range recentEvents {
			if _, err := f.WriteString(line + "\n"); err != nil {
				return
			}
		}

		f.Close()
		os.Rename(tmpFile, eventsFile)
	} else {
		// No recent events - truncate file
		os.Truncate(eventsFile, 0)
	}
}