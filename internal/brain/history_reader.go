package brain

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// HistoryEntry is one archived version of a long-term fact: the full value
// the fact held before it was superseded or deleted. Write-side companion
// of bd-31u's brain_history table; read-side API added in bd-r1f3.
type HistoryEntry struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	CreatedAt  time.Time `json:"created_at"`  // when the fact was born
	ArchivedAt time.Time `json:"archived_at"` // when it stopped being live
	Source     string    `json:"source,omitempty"`
	Tier       Tier      `json:"tier"`
	Salience   float64   `json:"salience"`
	Reason     string    `json:"reason"` // superseded | deleted
}

// ListKeyHistory returns the archived versions of key, newest archive first.
// limit caps the number of rows (<= 0 means no limit). The live value is NOT
// included — the result is empty for facts that have never been superseded
// or deleted. See ValueAsOf for point-in-time reads including live rows.
func (b *Brain) ListKeyHistory(ctx context.Context, key string, limit int) ([]*HistoryEntry, error) {
	return b.queryHistory(ctx, `
		SELECT key, value, created_at, archived_at, COALESCE(source, ''), COALESCE(tier, 'longterm'), COALESCE(salience, 0.0), reason
		FROM brain_history WHERE key = ?`, limit, key)
}

// RecentHistory returns the most recently archived facts across all keys,
// newest archive first. limit caps the number of rows (<= 0 means no limit).
func (b *Brain) RecentHistory(ctx context.Context, limit int) ([]*HistoryEntry, error) {
	return b.queryHistory(ctx, `
		SELECT key, value, created_at, archived_at, COALESCE(source, ''), COALESCE(tier, 'longterm'), COALESCE(salience, 0.0), reason
		FROM brain_history`, limit)
}

// queryHistory runs a brain_history projection, appends the shared ordering
// (newest archive first) and an optional LIMIT, and drains the rows. A limit
// <= 0 means no limit.
func (b *Brain) queryHistory(ctx context.Context, selectSQL string, limit int, whereArgs ...interface{}) ([]*HistoryEntry, error) {
	q := selectSQL + " ORDER BY archived_at DESC, id DESC"
	args := whereArgs
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := b.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()
	var out []*HistoryEntry
	for rows.Next() {
		h := &HistoryEntry{}
		var createdAt, archivedAt sql.NullTime
		if err := rows.Scan(&h.Key, &h.Value, &createdAt, &archivedAt, &h.Source, &h.Tier, &h.Salience, &h.Reason); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}
		if createdAt.Valid {
			h.CreatedAt = createdAt.Time
		}
		if archivedAt.Valid {
			h.ArchivedAt = archivedAt.Time
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history rows: %w", err)
	}
	return out, nil
}

// ValueAsOf returns the value of key as it stood at instant asOf, or
// (nil, nil) when the key had no value at that instant. It never mutates
// live-row bookkeeping (no access-count bumps).
//
// A history row represents a value live on the half-open interval
// [created_at, archived_at) — archived_at is the moment the NEXT value took
// over (superseded) or the fact ceased to exist (deleted). The value live
// at T is therefore carried by the OLDEST archive row whose archived_at is
// still in the future relative to T, not by the newest row at or before T:
// the row archived at T+1h holds the value that was live at T, even though
// its created_at is much older (created_at survives upserts and is shared
// by every generation of a long-lived fact). A deleted-reason row chosen by
// this rule means T is before the deletion instant, so its value was still
// live at T. Only when no archive row lies in the future does the live row
// apply, and only if the fact was already born (created_at <= T); a key
// deleted and later re-created reads as not-found for instants inside the
// gap.
//
// Reading is bounded by storage precision: archived_at is stamped at second
// granularity, so instants within the same second as an archive event are
// resolved to the post-archive value (half-open intervals — the boundary
// instant belongs to the successor).
func (b *Brain) ValueAsOf(ctx context.Context, key string, asOf time.Time) (*Entry, error) {
	asOfStr := asOf.UTC().Format("2006-01-02 15:04:05")

	// Oldest archive event still in the future relative to T. Whichever
	// value it holds was the one live at T (see doc comment).
	var value string
	var createdAt sql.NullTime
	var reason string
	err := b.db.QueryRowContext(ctx, `
		SELECT value, created_at, reason FROM brain_history
		WHERE key = ? AND archived_at > ?
		ORDER BY archived_at, id LIMIT 1`, key, asOfStr).Scan(&value, &createdAt, &reason)
	if err == nil {
		// Birth guard: a row whose created_at is after T describes a value
		// that did not exist yet at T (corrupt history or legacy NULLs).
		// NULL created_at predates migration 10 bookkeeping — accept it.
		if createdAt.Valid && createdAt.Time.UTC().Truncate(time.Second).After(asOf.UTC().Truncate(time.Second)) {
			return nil, nil
		}
		// Superseded and deleted rows alike carry the then-live value: a
		// deleted row selected by the oldest-future rule means T is before
		// the deletion instant.
		return &Entry{
			Key:            key,
			Value:          value,
			Tier:           TierLongTerm,
			CreatedAt:      createdAt.Time,
			ArchivedReason: reason,
		}, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("value as of: %w", err)
	}

	// No archive event after T: the live row applies, if the fact was born
	// by T. A deleted-then-recreated key has no row covering the gap, so it
	// reads as not-found there.
	entry := &Entry{Tier: TierLongTerm}
	err = b.db.QueryRowContext(ctx, `
		SELECT value, created_at FROM brain_ltm
		WHERE key = ? AND created_at <= ?`, key, asOfStr).Scan(&entry.Value, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("value as of: %w", err)
	}
	entry.Key = key
	if createdAt.Valid {
		entry.CreatedAt = createdAt.Time
	}
	return entry, nil
}
