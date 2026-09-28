package brain

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"conduit/internal/database"
)

func (b *Brain) Push(ctx context.Context, userID, value string) error {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scratch[userID] = append(b.scratch[userID], value)
	return nil
}

func (b *Brain) Pop(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stack := b.scratch[userID]
	if len(stack) == 0 {
		return "", fmt.Errorf("scratchpad is empty")
	}
	val := stack[len(stack)-1]
	b.scratch[userID] = stack[:len(stack)-1]
	return val, nil
}

func (b *Brain) Peek(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	stack := b.scratch[userID]
	if len(stack) == 0 {
		return "", fmt.Errorf("scratchpad is empty")
	}
	return stack[len(stack)-1], nil
}

func (b *Brain) Promote(ctx context.Context, key string) error {
	userID := userIDFromCtx(ctx)
	b.mu.Lock()
	var entry *Entry
	if wm, ok := b.working[userID]; ok {
		if e, ok := wm[key]; ok {
			entry = e
			delete(wm, key)
		}
	}
	b.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("key %q not found in working memory", key)
	}
	return b.storeLTM(key, entry.Value, entry.Source, time.Now(), entry.ExpiresAt)
}

// WorkingMemoryEntries returns a snapshot of all WM entries for the given user.
// Expired entries are filtered out.
func (b *Brain) WorkingMemoryEntries(ctx context.Context) []*Entry {
	userID := userIDFromCtx(ctx)
	now := time.Now()
	b.mu.RLock()
	defer b.mu.RUnlock()
	wm, ok := b.working[userID]
	if !ok {
		return nil
	}
	entries := make([]*Entry, 0, len(wm))
	for _, e := range wm {
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		copied := *e
		entries = append(entries, &copied)
	}
	return entries
}

func (b *Brain) Consolidate(ctx context.Context, autoPromote bool) (*ConsolidationReport, error) {
	userID := userIDFromCtx(ctx)
	report := &ConsolidationReport{}

	// Prune expired entries first so we never promote stale data.
	if _, err := b.pruneExpired(ctx); err != nil {
		log.Printf("Brain.Consolidate: pruneExpired failed: %v", err)
	}

	b.mu.Lock()
	wm, ok := b.working[userID]
	if !ok || len(wm) == 0 {
		b.mu.Unlock()
		var ltmSize int
		b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmSize)
		report.LTMSize = ltmSize
		return report, nil
	}

	// conduit-31jg.32: toPromote holds snapshots — storeLTM runs outside the
	// lock while Store/Get/autoFlush keep mutating the live entries.
	var toPromote []Entry
	var toEvict []string
	now := time.Now()
	for key, entry := range wm {
		entry.Salience = b.computeSalience(entry)
		if autoPromote && entry.Salience >= b.consolidateThreshold {
			toPromote = append(toPromote, *entry)
		} else if b.wmEvictable(entry, now) {
			// conduit-31jg.29: hot entries are promoted (or kept, when
			// promotion is off) rather than evicted.
			if b.wmIsHot(entry) {
				if autoPromote {
					toPromote = append(toPromote, *entry)
				}
				continue
			}
			toEvict = append(toEvict, key)
		}
	}
	for _, key := range toEvict {
		delete(wm, key)
	}
	b.mu.Unlock()

	var promotedKeys []string
	for _, entry := range toPromote {
		if err := b.storeLTM(entry.Key, entry.Value, entry.Source, time.Now(), entry.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote %q: %v", entry.Key, err)
			continue
		}
		promotedKeys = append(promotedKeys, entry.Key)
		report.PromotedKeys = append(report.PromotedKeys, entry.Key)
		report.PromotedCount++
	}
	if len(promotedKeys) > 0 {
		b.mu.Lock()
		if wm, ok := b.working[userID]; ok {
			for _, snap := range toPromote {
				// Only drop the WM copy if it still holds the value we
				// promoted; a concurrent Store of a newer value stays in WM.
				if live, ok := wm[snap.Key]; ok && live.Value == snap.Value && containsString(promotedKeys, snap.Key) {
					delete(wm, snap.Key)
				}
			}
		}
		b.mu.Unlock()
	}
	report.EvictedCount = len(toEvict)
	report.EvictedKeys = toEvict

	var ltmSize int
	b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmSize)
	report.LTMSize = ltmSize
	log.Printf("Brain: consolidation — promoted=%d evicted=%d ltm=%d", report.PromotedCount, report.EvictedCount, report.LTMSize)
	return report, nil
}

type brainUserIDKey struct{}

// SharedUserID is the working-memory bucket used by callers that carry no
// user (system writers such as heartbeat alerts under sense.alerts.*). Every
// user can read it (read-only, after their own and their parent's WM); only
// unscoped callers write to it. conduit-31jg.30
const SharedUserID = "default"

// WithUserID scopes working memory and the scratchpad to userID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, brainUserIDKey{}, userID)
}

// UserIDFromContext returns the brain user ID explicitly attached with
// WithUserID, and whether one was set.
func UserIDFromContext(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(brainUserIDKey{}).(string)
	return uid, ok && uid != ""
}

func userIDFromCtx(ctx context.Context) string {
	if uid, ok := UserIDFromContext(ctx); ok {
		return uid
	}
	return SharedUserID
}

// readOnlyBuckets lists the WM buckets a caller may read but not write, in
// lookup order: the parent's (sub-agent sharing), then the shared bucket.
func readOnlyBuckets(userID, parentID string) []string {
	var out []string
	if parentID != "" && parentID != userID {
		out = append(out, parentID)
	}
	if userID != SharedUserID && parentID != SharedUserID {
		out = append(out, SharedUserID)
	}
	return out
}

// WorkingMemoryUserIDs returns the IDs of every user with a non-empty WM
// bucket (used by REM to promote across all users). conduit-31jg.30
func (b *Brain) WorkingMemoryUserIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]string, 0, len(b.working))
	for id, wm := range b.working {
		if len(wm) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

type brainParentUserIDKey struct{}

// WithParentUserID attaches a parent brain user ID to the context, enabling
// read-only fallback to the parent's working memory for sub-agent sessions.
func WithParentUserID(ctx context.Context, parentUserID string) context.Context {
	return context.WithValue(ctx, brainParentUserIDKey{}, parentUserID)
}

func parentUserIDFromCtx(ctx context.Context) string {
	if uid, ok := ctx.Value(brainParentUserIDKey{}).(string); ok {
		return uid
	}
	return ""
}

func (b *Brain) startAutoFlush() {
	if b.autoFlushInterval <= 0 {
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(b.autoFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				b.autoFlush()
			}
		}
	}()
}

func (b *Brain) autoFlush() {
	// conduit-31jg.29: eviction is decided by wmEvictable (access+recency
	// score, idle > wmEvictMinIdle), which is actually reachable. Hot entries
	// that go idle are promoted to LTM first (promote-then-delete, so a
	// failed promotion loses nothing) instead of silently disappearing
	// before the nightly REM cycle can see them.
	now := time.Now()
	var rescue []wmRef
	b.mu.Lock()
	for userID, wm := range b.working {
		for key, entry := range wm {
			entry.Salience = b.computeSalience(entry)
			if !b.wmEvictable(entry, now) {
				continue
			}
			if b.wmIsHot(entry) {
				if b.autoPromote {
					rescue = append(rescue, wmRef{userID: userID, entry: *entry})
				}
				continue
			}
			delete(wm, key)
		}
		if len(wm) == 0 {
			delete(b.working, userID)
		}
	}
	b.mu.Unlock()
	b.promoteThenDrop(rescue)

	// The edge/warmth maintenance below must run without b.mu held:
	// flushPendingEdges re-acquires b.mu, and holding it across DB work blocks
	// every Store/Get/Recall in the gateway for the duration of the flush.
	if b.spreadingEnabled {
		// Warmth decay: cools activated entries each flush cycle.
		// 0.85^3 = 61.4% warmth at 3 flushes (good isolation), 0.85^5 = 44.4%
		// Previously 0.95 gave 85.7% at 3 flushes, causing cross-domain noise.
		wd := b.warmthDecay
		_ = database.RetryOnBusy(3, func() error {
			_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = CASE WHEN warmth * ? < 0.01 THEN 0.0 ELSE warmth * ? END WHERE warmth > 0.0`, wd, wd)
			return err
		})
		_ = b.flushPendingEdges()
		_ = b.DecayEdgeConfidence(b.edgeDecay, 0.1, 6*time.Hour)
	}
}
