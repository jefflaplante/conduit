package rem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// REMReport is the full cycle output
type REMReport struct {
	Date          time.Time
	Triage        *TriageResult
	Consolidation *ConsolidationResult
	Pruning       *PruneResult
	Integration   *IntegrationResult
	Grooming      *GroomResult
	DryRun        bool
}

// Per-phase result structs

type TriageResult struct {
	DailyLogScanned string
	WMKeysFound     int
	NewFacts        []string
	UpdatedFacts    []string
	StaleCandidates []string
}

type ConsolidationResult struct {
	Promoted        []string
	Merged          []MergeRecord
	SalienceDecayed int
	SalienceBoosted int
}

type MergeRecord struct {
	Kept   string
	Merged string
}

type PruneResult struct {
	Archived []ArchiveRecord
	Orphaned []string
}

type ArchiveRecord struct {
	Key    string
	Reason string
}

type IntegrationResult struct {
	RelationshipsCreated int
	Patterns             []string
}

type GroomResult struct {
	FilesChecked       int
	FilesChanged       []string
	KeysUpdated        int
	EntriesMarkedStale int
}

// WriteLog writes the REM report to a markdown file
func (r *REMReport) WriteLog(logPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(logPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Generate filename with date
	filename := fmt.Sprintf("rem-%s.md", r.Date.Format("2006-01-02"))
	fullPath := filepath.Join(logPath, filename)

	// Build report content
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# REM Sleep Cycle Report - %s\n\n", r.Date.Format("2006-01-02 15:04:05")))

	if r.DryRun {
		sb.WriteString("**DRY RUN** - No changes were made\n\n")
	}

	// Triage section
	if r.Triage != nil {
		sb.WriteString("## Phase 1: Triage\n\n")
		sb.WriteString(fmt.Sprintf("- Daily log scanned: `%s`\n", r.Triage.DailyLogScanned))
		sb.WriteString(fmt.Sprintf("- Working memory keys found: %d\n\n", r.Triage.WMKeysFound))

		if len(r.Triage.NewFacts) > 0 {
			sb.WriteString("### New Facts\n")
			for _, fact := range r.Triage.NewFacts {
				sb.WriteString(fmt.Sprintf("- %s\n", fact))
			}
			sb.WriteString("\n")
		}

		if len(r.Triage.UpdatedFacts) > 0 {
			sb.WriteString("### Updated Facts\n")
			for _, fact := range r.Triage.UpdatedFacts {
				sb.WriteString(fmt.Sprintf("- %s\n", fact))
			}
			sb.WriteString("\n")
		}

		if len(r.Triage.StaleCandidates) > 0 {
			sb.WriteString("### Stale Candidates\n")
			for _, candidate := range r.Triage.StaleCandidates {
				sb.WriteString(fmt.Sprintf("- %s\n", candidate))
			}
			sb.WriteString("\n")
		}
	}

	// Consolidation section
	if r.Consolidation != nil {
		sb.WriteString("## Phase 2: Consolidation\n\n")
		sb.WriteString(fmt.Sprintf("- Promoted to LTM: %d entries\n", len(r.Consolidation.Promoted)))
		sb.WriteString(fmt.Sprintf("- Merged duplicates: %d pairs\n", len(r.Consolidation.Merged)))
		sb.WriteString(fmt.Sprintf("- Salience decayed: %d entries\n", r.Consolidation.SalienceDecayed))
		sb.WriteString(fmt.Sprintf("- Salience boosted: %d entries\n\n", r.Consolidation.SalienceBoosted))

		if len(r.Consolidation.Promoted) > 0 {
			sb.WriteString("### Promoted Entries\n")
			for _, key := range r.Consolidation.Promoted {
				sb.WriteString(fmt.Sprintf("- `%s`\n", key))
			}
			sb.WriteString("\n")
		}

		if len(r.Consolidation.Merged) > 0 {
			sb.WriteString("### Merged Entries\n")
			for _, merge := range r.Consolidation.Merged {
				sb.WriteString(fmt.Sprintf("- Kept: `%s` | Merged: `%s`\n", merge.Kept, merge.Merged))
			}
			sb.WriteString("\n")
		}
	}

	// Pruning section
	if r.Pruning != nil {
		sb.WriteString("## Phase 3: Pruning\n\n")
		sb.WriteString(fmt.Sprintf("- Archived: %d entries\n", len(r.Pruning.Archived)))
		sb.WriteString(fmt.Sprintf("- Orphaned: %d entries\n\n", len(r.Pruning.Orphaned)))

		if len(r.Pruning.Archived) > 0 {
			sb.WriteString("### Archived Entries\n")
			for _, archive := range r.Pruning.Archived {
				sb.WriteString(fmt.Sprintf("- `%s` - %s\n", archive.Key, archive.Reason))
			}
			sb.WriteString("\n")
		}

		if len(r.Pruning.Orphaned) > 0 {
			sb.WriteString("### Orphaned Entries\n")
			for _, key := range r.Pruning.Orphaned {
				sb.WriteString(fmt.Sprintf("- `%s`\n", key))
			}
			sb.WriteString("\n")
		}
	}

	// Integration section
	if r.Integration != nil {
		sb.WriteString("## Phase 4: Integration\n\n")
		sb.WriteString(fmt.Sprintf("- Relationships created: %d\n", r.Integration.RelationshipsCreated))
		sb.WriteString(fmt.Sprintf("- Patterns detected: %d\n\n", len(r.Integration.Patterns)))

		if len(r.Integration.Patterns) > 0 {
			sb.WriteString("### Patterns\n")
			for _, pattern := range r.Integration.Patterns {
				sb.WriteString(fmt.Sprintf("- %s\n", pattern))
			}
			sb.WriteString("\n")
		}
	}

	// Grooming section
	if r.Grooming != nil {
		sb.WriteString("## Phase 5: Grooming\n\n")
		sb.WriteString(fmt.Sprintf("- Files checked: %d\n", r.Grooming.FilesChecked))
		sb.WriteString(fmt.Sprintf("- Files changed: %d\n", len(r.Grooming.FilesChanged)))
		sb.WriteString(fmt.Sprintf("- Keys updated: %d\n", r.Grooming.KeysUpdated))
		sb.WriteString(fmt.Sprintf("- Entries marked stale: %d\n\n", r.Grooming.EntriesMarkedStale))

		if len(r.Grooming.FilesChanged) > 0 {
			sb.WriteString("### Changed Files\n")
			for _, file := range r.Grooming.FilesChanged {
				sb.WriteString(fmt.Sprintf("- `%s`\n", file))
			}
			sb.WriteString("\n")
		}
	}

	// Write to file
	if err := os.WriteFile(fullPath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write report: %w", err)
	}

	return nil
}
