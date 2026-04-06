package rem

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"conduit/internal/brain"
)

// REMConfig holds configuration for the REM sleep cycle
type REMConfig struct {
	PruneAgeDays         int
	SalienceDecayRate    float64
	ConsolidateThreshold float64 // Salience threshold for WM→LTM promotion (default 0.6)
	IntegrationDay       int     // 0 = Sunday
	GroomWithLLM         bool
	LogPath              string // Relative to WorkspaceDir if not absolute
	WorkspaceDir         string // Absolute path to workspace root (e.g. /home/jules/ocgo/workspace)
	MaxLTMEntries        int    // When LTM count is below this, skip pruning/decay (default 10000)
}

// REMCycle orchestrates offline memory consolidation
type REMCycle struct {
	brain  *brain.Brain
	config REMConfig
	db     *sql.DB // direct DB access for archive/relationships
}

// NewREMCycle creates a new REM cycle orchestrator
func NewREMCycle(b *brain.Brain, db *sql.DB, config REMConfig) *REMCycle {
	return &REMCycle{
		brain:  b,
		config: config,
		db:     db,
	}
}

// Run executes the full REM cycle and returns a report
func (r *REMCycle) Run(ctx context.Context, phases []string, dryRun bool) (*REMReport, error) {
	// Default to all phases if none specified
	if len(phases) == 0 {
		phases = []string{"triage", "consolidation", "pruning", "integration", "grooming"}
	}

	report := &REMReport{
		Date:   time.Now(),
		DryRun: dryRun,
	}

	// Execute requested phases in order
	for _, phase := range phases {
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		default:
		}

		var err error
		switch phase {
		case "triage":
			report.Triage, err = r.runTriage(ctx, dryRun)
		case "consolidation":
			report.Consolidation, err = r.runConsolidation(ctx, dryRun)
		case "pruning":
			report.Pruning, err = r.runPruning(ctx, dryRun)
		case "integration":
			report.Integration, err = r.runIntegration(ctx, dryRun)
		case "grooming":
			report.Grooming, err = r.runGrooming(ctx, dryRun)
		default:
			return report, fmt.Errorf("unknown phase: %s", phase)
		}

		if err != nil {
			return report, fmt.Errorf("phase %s failed: %w", phase, err)
		}
	}

	// Write log if configured
	if r.config.LogPath != "" && !dryRun {
		logPath := r.resolvePath(r.config.LogPath)
		if err := report.WriteLog(logPath); err != nil {
			return report, fmt.Errorf("failed to write log: %w", err)
		}
	}

	return report, nil
}

// Individual phase methods delegate to the full implementations in their respective files.

func (r *REMCycle) runTriage(ctx context.Context, dryRun bool) (*TriageResult, error) {
	return r.Triage(ctx, dryRun)
}

func (r *REMCycle) runConsolidation(ctx context.Context, dryRun bool) (*ConsolidationResult, error) {
	return r.Consolidate(ctx, dryRun)
}

func (r *REMCycle) runPruning(ctx context.Context, dryRun bool) (*PruneResult, error) {
	return r.Prune(ctx, dryRun)
}

func (r *REMCycle) runIntegration(ctx context.Context, dryRun bool) (*IntegrationResult, error) {
	return r.Integrate(ctx, dryRun)
}

func (r *REMCycle) runGrooming(ctx context.Context, dryRun bool) (*GroomResult, error) {
	return r.Groom(ctx, dryRun)
}

// resolvePath resolves a path against WorkspaceDir if it's not absolute.
func (r *REMCycle) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if r.config.WorkspaceDir != "" {
		return filepath.Join(r.config.WorkspaceDir, p)
	}
	return p
}
