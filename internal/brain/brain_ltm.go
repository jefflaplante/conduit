package brain

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"conduit/internal/database"
)

// storeOpts holds options for Store calls, populated by StoreOption funcs.
type storeOpts struct {
	ttl time.Duration
}

// StoreOption configures an individual Store call.
type StoreOption func(*storeOpts)

// WithTTL sets a time-to-live on the stored entry. After ttl elapses, the
// entry is no longer returned from Get/Recall and is eligible for deletion
// by the next pruneExpired/Consolidate/rem_cycle sweep. A zero duration
// means no expiry.
func WithTTL(d time.Duration) StoreOption {
	return func(o *storeOpts) { o.ttl = d }
}

// ParseDuration parses a duration string with Go's standard units plus
// the convenience suffixes "d" (days) and "w" (weeks), which time.ParseDuration
// does not natively support. Example: "24h", "7d", "2w", "90m".
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	// Check for trailing d or w (days/weeks); otherwise defer to stdlib.
	if n := len(s); n >= 2 {
		last := s[n-1]
		if last == 'd' || last == 'w' {
			var count int
			if _, err := fmt.Sscanf(s[:n-1], "%d", &count); err != nil {
				return 0, fmt.Errorf("parse duration %q: %w", s, err)
			}
			mult := 24 * time.Hour
			if last == 'w' {
				mult = 7 * 24 * time.Hour
			}
			return time.Duration(count) * mult, nil
		}
	}
	return time.ParseDuration(s)
}

func (b *Brain) Store(ctx context.Context, key, value string, tier Tier, source string, opts ...StoreOption) error {
	if err := ValidateSource(source); err != nil {
		log.Printf("Brain: warning: %v (key=%q)", err, key)
	}
	o := &storeOpts{}
	for _, opt := range opts {
		opt(o)
	}
	now := time.Now()
	var expiresAt *time.Time
	if o.ttl > 0 {
		t := now.Add(o.ttl)
		expiresAt = &t
	}
	switch tier {
	case TierLongTerm:
		return b.storeLTM(key, value, source, now, expiresAt)
	case TierWorking:
		userID := userIDFromCtx(ctx)
		b.mu.Lock()
		if b.working[userID] == nil {
			b.working[userID] = make(map[string]*Entry)
		}
		if existing, ok := b.working[userID][key]; ok {
			existing.Value = value
			existing.AccessedAt = now
			existing.AccessCount++
			existing.Source = source
			existing.Salience = b.computeSalience(existing)
			existing.ExpiresAt = expiresAt
		} else {
			b.working[userID][key] = &Entry{
				Key: key, Value: value, Tier: TierWorking,
				CreatedAt: now, AccessedAt: now,
				AccessCount: 1, Salience: 0.5, Source: source,
				ExpiresAt: expiresAt,
			}
		}
		rescue := b.enforceWMCapLocked(userID, map[string]bool{key: true}, now)
		b.mu.Unlock()
		b.promoteEvicted(rescue)
		return nil
	default:
		return fmt.Errorf("unsupported tier for store: %s (use Push for scratch)", tier)
	}
}

// BulkEntry is a single key/value entry for StoreBulk. Tier defaults to
// TierWorking when empty; TierScratch is rejected. Source is optional.
type BulkEntry struct {
	Key    string
	Value  string
	Tier   Tier
	Source string
}

// StoreBulk stores many entries atomically. Long-term entries are written in
// a single SQL transaction (so a failure on any entry rolls back all of the
// LTM writes in the batch); working-memory entries are then applied under the
// brain lock. If the LTM transaction fails, no entries — LTM or WM — are
// persisted. TierScratch is not a valid bulk target and causes an error.
func (b *Brain) StoreBulk(ctx context.Context, entries []BulkEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Normalize + validate up-front so we never partially apply.
	normalized := make([]BulkEntry, len(entries))
	for i, e := range entries {
		if e.Key == "" {
			return fmt.Errorf("bulk entry %d: key is required", i)
		}
		tier := e.Tier
		if tier == "" {
			tier = TierWorking
		}
		switch tier {
		case TierLongTerm, TierWorking:
			// ok
		case TierScratch:
			return fmt.Errorf("bulk entry %d: scratch tier is not a valid bulk target", i)
		default:
			return fmt.Errorf("bulk entry %d: unsupported tier %q", i, tier)
		}
		if err := ValidateSource(e.Source); err != nil {
			// Match Store(): warn but do not fail on weird sources.
			log.Printf("Brain.StoreBulk: warning: %v (key=%q)", err, e.Key)
		}
		normalized[i] = BulkEntry{Key: e.Key, Value: e.Value, Tier: tier, Source: e.Source}
	}

	now := time.Now()
	nowStr := now.UTC().Format("2006-01-02 15:04:05")

	// Partition so we can commit LTM atomically inside a single tx.
	var ltmBatch []BulkEntry
	var wmBatch []BulkEntry
	for _, e := range normalized {
		if e.Tier == TierLongTerm {
			ltmBatch = append(ltmBatch, e)
		} else {
			wmBatch = append(wmBatch, e)
		}
	}

	if len(ltmBatch) > 0 {
		err := database.RetryOnBusy(5, func() error {
			tx, err := b.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			// conduit-31jg.53: store base salience only (access + tier);
			// recency is computed at query time.
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
				VALUES (?, ?, ?, ?, ?, 1, ?)
				ON CONFLICT(key) DO UPDATE SET
					value = excluded.value,
					source = excluded.source,
					accessed_at = excluded.accessed_at,
					access_count = access_count + 1,
					salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
			`)
			if err != nil {
				tx.Rollback()
				return err
			}
			defer stmt.Close()
			for _, e := range ltmBatch {
				if _, err := stmt.ExecContext(ctx, e.Key, e.Value, e.Source, nowStr, nowStr,
					b.ltmBaseSalience(1)); err != nil {
					tx.Rollback()
					return err
				}
			}
			return tx.Commit()
		})
		if err != nil {
			return fmt.Errorf("store bulk LTM: %w", err)
		}

		// Best-effort eviction — same pattern as storeLTM. Every row in this
		// batch has accessed_at = nowStr, so it is inside the grace window.
		// conduit-31jg.28
		b.evictLTMOverCapacity(ctx, now)
		for _, e := range ltmBatch {
			b.markPendingEdge(e.Key)
		}
	}

	if len(wmBatch) > 0 {
		userID := userIDFromCtx(ctx)
		b.mu.Lock()
		if b.working[userID] == nil {
			b.working[userID] = make(map[string]*Entry)
		}
		protect := make(map[string]bool, len(wmBatch))
		for _, e := range wmBatch {
			protect[e.Key] = true
			if existing, ok := b.working[userID][e.Key]; ok {
				existing.Value = e.Value
				existing.AccessedAt = now
				existing.AccessCount++
				existing.Source = e.Source
				existing.Salience = b.computeSalience(existing)
			} else {
				b.working[userID][e.Key] = &Entry{
					Key: e.Key, Value: e.Value, Tier: TierWorking,
					CreatedAt: now, AccessedAt: now,
					AccessCount: 1, Salience: 0.5, Source: e.Source,
				}
			}
		}
		// A bulk larger than the cap can't protect every key it just wrote;
		// in that case the batch's own writes are fair game too.
		if b.maxWMEntriesPerUser > 0 && len(protect) >= b.maxWMEntriesPerUser {
			protect = nil
		}
		rescue := b.enforceWMCapLocked(userID, protect, now)
		b.mu.Unlock()
		b.promoteEvicted(rescue)
	}

	return nil
}

func (b *Brain) storeLTM(key, value, source string, now time.Time, expiresAt *time.Time) error {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	var expiresStr interface{}
	if expiresAt != nil {
		// Use sub-second precision so TTLs shorter than 1s still expire correctly
		// when compared against strftime('%Y-%m-%d %H:%M:%f','now').
		expiresStr = expiresAt.UTC().Format("2006-01-02 15:04:05.000")
	} else {
		expiresStr = nil
	}
	// conduit-31jg.53: the column holds base salience (access + tier) only;
	// recency is computed at query time (see salience.go).
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec(`
			INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience, expires_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				source = excluded.source,
				accessed_at = excluded.accessed_at,
				access_count = access_count + 1,
				expires_at = excluded.expires_at,
				salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
		`, key, value, source, nowStr, nowStr, b.ltmBaseSalience(1), expiresStr)
		return err
	})
	if err != nil {
		return fmt.Errorf("store LTM: %w", err)
	}
	// Best-effort eviction. The row just written has accessed_at = nowStr and
	// is therefore inside the grace window. conduit-31jg.28
	b.evictLTMOverCapacity(context.Background(), now)
	b.markPendingEdge(key)
	return nil
}

// markPendingEdge queues an LTM key for namespace edge materialization on the
// next autoFlush. No-op when spreading is disabled.
func (b *Brain) markPendingEdge(key string) {
	if !b.spreadingEnabled {
		return
	}
	b.mu.Lock()
	if b.pendingEdgeKeys == nil {
		b.pendingEdgeKeys = make(map[string]struct{})
	}
	b.pendingEdgeKeys[key] = struct{}{}
	b.mu.Unlock()
}

func (b *Brain) Get(ctx context.Context, key string) (*Entry, error) {
	userID := userIDFromCtx(ctx)
	now := time.Now()
	// conduit-31jg.32: one write-locked section for the WM lookup + access
	// bump, and callers always get a snapshot copy, never the live *Entry
	// (autoFlush/Consolidate/Store mutate live entries under b.mu).
	b.mu.Lock()
	if wm, ok := b.working[userID]; ok {
		if entry, ok := wm[key]; ok {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				// Expired — delete and fall through to LTM lookup.
				delete(wm, key)
				b.mu.Unlock()
				return b.getLTM(key)
			}
			entry.AccessedAt = now
			entry.AccessCount++
			entry.Salience = b.computeSalience(entry)
			copied := *entry
			b.mu.Unlock()
			return &copied, nil
		}
	}
	// Parent's WM, then the shared bucket (read-only — copies, no access
	// bump). conduit-31jg.30
	for _, bucket := range readOnlyBuckets(userID, parentUserIDFromCtx(ctx)) {
		if entry, ok := b.working[bucket][key]; ok {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue // expired — keep looking, then LTM
			}
			copied := *entry
			b.mu.Unlock()
			return &copied, nil
		}
	}
	b.mu.Unlock()
	entry, err := b.getLTM(key)
	if err == nil && entry != nil {
		// Fire-and-forget: spread activation to neighbours. Errors are non-fatal.
		_ = b.spreadActivation([]string{key})
	}
	return entry, err
}

func (b *Brain) getLTM(key string) (*Entry, error) {
	// conduit-31jg.53: store base salience; return the effective value (just
	// accessed, so recency = 1).
	row := b.db.QueryRow(`
		UPDATE brain_ltm SET
			accessed_at = datetime('now'),
			access_count = access_count + 1,
			salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
		WHERE key = ? AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		RETURNING key, value, created_at, accessed_at, access_count, `+b.EffectiveSalienceSQL()+`, source, stale, expires_at, warmth
	`, key)
	entry := &Entry{Tier: TierLongTerm}
	var staleInt int
	var expiresAt sql.NullTime
	err := row.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
		&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &expiresAt, &entry.Warmth)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get LTM: %w", err)
	}
	entry.Stale = staleInt != 0
	if expiresAt.Valid {
		t := expiresAt.Time
		entry.ExpiresAt = &t
	}
	return entry, nil
}

func (b *Brain) Delete(ctx context.Context, key string) error {
	userID := userIDFromCtx(ctx)
	b.mu.Lock()
	if wm, ok := b.working[userID]; ok {
		delete(wm, key)
	}
	b.mu.Unlock()
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec("DELETE FROM brain_ltm WHERE key = ?", key)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete LTM: %w", err)
	}
	return nil
}

// pruneExpired deletes all brain_ltm rows whose expires_at has passed, and
// also removes expired entries from in-memory working memory. Returns the
// total count of deleted entries.
func (b *Brain) pruneExpired(ctx context.Context) (int, error) {
	var total int
	// LTM: single DELETE for all expired rows.
	var n int64
	err := database.RetryOnBusy(5, func() error {
		res, err := b.db.ExecContext(ctx,
			`DELETE FROM brain_ltm WHERE expires_at IS NOT NULL AND expires_at <= strftime('%Y-%m-%d %H:%M:%f', 'now')`)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("prune expired LTM: %w", err)
	}
	total += int(n)

	// WM: walk every user's working map and drop expired entries.
	now := time.Now()
	b.mu.Lock()
	for _, wm := range b.working {
		for key, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				delete(wm, key)
				total++
			}
		}
	}
	b.mu.Unlock()
	return total, nil
}

// PruneExpired is the exported wrapper for pruneExpired; used by REM cycle
// phases (prune, consolidate) to delete time-expired entries before other work.
func (b *Brain) PruneExpired(ctx context.Context) (int, error) {
	return b.pruneExpired(ctx)
}

// StoreWithTTL is a convenience wrapper around Store that applies WithTTL(ttl).
// A zero ttl is equivalent to calling Store with no options.
func (b *Brain) StoreWithTTL(ctx context.Context, key, value string, tier Tier, source string, ttl time.Duration) error {
	if ttl <= 0 {
		return b.Store(ctx, key, value, tier, source)
	}
	return b.Store(ctx, key, value, tier, source, WithTTL(ttl))
}
