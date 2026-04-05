package rem

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Triage scans daily logs and working memory to identify what needs processing
func (r *REMCycle) Triage(ctx context.Context, dryRun bool) (*TriageResult, error) {
	result := &TriageResult{
		NewFacts:        []string{},
		UpdatedFacts:    []string{},
		StaleCandidates: []string{},
	}

	// 1. Scan today's daily log file: memory/YYYY-MM-DD.md
	memoryDir := r.getMemoryDir()
	today := time.Now().Format("2006-01-02")
	dailyLogPath := filepath.Join(memoryDir, today+".md")
	result.DailyLogScanned = dailyLogPath

	// Parse daily log for facts
	newFacts, updatedFacts, err := r.scanDailyLog(ctx, dailyLogPath)
	if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("scan daily log: %w", err)
	}
	result.NewFacts = newFacts
	result.UpdatedFacts = updatedFacts

	// 2. Scan working memory for unpromoted keys
	wmEntries, err := r.brain.List(ctx, "")
	if err != nil {
		return result, fmt.Errorf("list working memory: %w", err)
	}

	result.WMKeysFound = len(wmEntries)

	// Count entries that should be promoted (high salience/access count)
	for _, entry := range wmEntries {
		if entry.Tier == "working" {
			// Check if this key is mentioned in the new/updated facts
			mentioned := false
			key := entry.Key
			for _, fact := range append(newFacts, updatedFacts...) {
				if strings.Contains(fact, key) {
					mentioned = true
					break
				}
			}
			// If mentioned in daily log, add to appropriate list
			if mentioned {
				isNew := false
				for _, fact := range newFacts {
					if strings.Contains(fact, key) {
						isNew = true
						break
					}
				}
				if isNew && !contains(result.NewFacts, key) {
					result.NewFacts = append(result.NewFacts, key)
				} else if !isNew && !contains(result.UpdatedFacts, key) {
					result.UpdatedFacts = append(result.UpdatedFacts, key)
				}
			}
		}
	}

	// 3. Identify stale candidates from LTM
	staleCandidates, err := r.findStaleCandidates(ctx)
	if err != nil {
		return result, fmt.Errorf("find stale candidates: %w", err)
	}
	result.StaleCandidates = staleCandidates

	return result, nil
}

// getMemoryDir returns the memory directory path from config or default
func (r *REMCycle) getMemoryDir() string {
	if r.config.LogPath != "" {
		// LogPath may contain the parent directory where memory/ lives
		// or it may be the directory where REM logs go
		// Default to using it as the workspace root
		return filepath.Join(filepath.Dir(r.config.LogPath), "memory")
	}
	return "memory"
}

// scanDailyLog parses a daily log file for facts
func (r *REMCycle) scanDailyLog(ctx context.Context, logPath string) (newFacts, updatedFacts []string, err error) {
	file, err := os.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	// Patterns to detect facts
	learnedPattern := regexp.MustCompile(`(?i)learned:\s*(.+)`)
	notedPattern := regexp.MustCompile(`(?i)noted:\s*(.+)`)
	rememberedPattern := regexp.MustCompile(`(?i)remembered:\s*(.+)`)
	updatedPattern := regexp.MustCompile(`(?i)updated:\s*(.+)`)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Check for new fact patterns
		if matches := learnedPattern.FindStringSubmatch(line); len(matches) > 1 {
			newFacts = append(newFacts, strings.TrimSpace(matches[1]))
		} else if matches := notedPattern.FindStringSubmatch(line); len(matches) > 1 {
			newFacts = append(newFacts, strings.TrimSpace(matches[1]))
		} else if matches := rememberedPattern.FindStringSubmatch(line); len(matches) > 1 {
			newFacts = append(newFacts, strings.TrimSpace(matches[1]))
		}

		// Check for updated fact patterns
		if matches := updatedPattern.FindStringSubmatch(line); len(matches) > 1 {
			updatedFacts = append(updatedFacts, strings.TrimSpace(matches[1]))
		}
	}

	if err := scanner.Err(); err != nil {
		return newFacts, updatedFacts, fmt.Errorf("scan log: %w", err)
	}

	return newFacts, updatedFacts, nil
}

// findStaleCandidates queries LTM for entries not accessed recently
func (r *REMCycle) findStaleCandidates(ctx context.Context) ([]string, error) {
	if r.config.PruneAgeDays <= 0 {
		return []string{}, nil
	}

	// Query LTM entries not accessed in > PruneAgeDays
	cutoff := time.Now().AddDate(0, 0, -r.config.PruneAgeDays)
	cutoffStr := cutoff.UTC().Format("2006-01-02 15:04:05")

	rows, err := r.db.Query(`
		SELECT key FROM brain_ltm
		WHERE accessed_at < ?
		ORDER BY accessed_at ASC
		LIMIT 100
	`, cutoffStr)
	if err != nil {
		return nil, fmt.Errorf("query stale entries: %w", err)
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			continue
		}
		candidates = append(candidates, key)
	}

	return candidates, nil
}

// contains checks if a string slice contains a value
func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}
