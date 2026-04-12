package gateway

import (
	"context"
	"time"

	"conduit/internal/brain"
	"conduit/internal/workspace"
)

// refreshBeadsPeriodic queries active beads via the `br` CLI and writes a
// summary to Brain's sense.tasks.active namespace. This runs once immediately
// at startup, then repeats on the given interval. It is always non-blocking
// and best-effort: if `br` is not installed, returns an error, or is slow,
// the goroutine silently skips the update.
func (g *Gateway) refreshBeadsPeriodic(ctx context.Context, interval time.Duration) {
	// Wrap Brain.Store as a callback so workspace.RefreshBeadsBrainEntry
	// doesn't need to import the brain package directly.
	// Uses LTM tier because beads data is project-wide (not per-user) and
	// must be visible to all sessions. The value is overwritten on each
	// refresh cycle, so it stays current despite being in long-term storage.
	brainStore := func(storeCtx context.Context, key, value, source string) error {
		return g.brainService.Store(storeCtx, key, value, brain.TierLongTerm, source)
	}

	// Run immediately at startup.
	initCtx, initCancel := context.WithTimeout(ctx, 5*time.Second)
	workspace.RefreshBeadsBrainEntry(initCtx, brainStore)
	initCancel()

	// Then refresh periodically.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, refreshCancel := context.WithTimeout(ctx, 5*time.Second)
			workspace.RefreshBeadsBrainEntry(refreshCtx, brainStore)
			refreshCancel()
		}
	}
}
