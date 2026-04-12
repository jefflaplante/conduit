package reflection

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolStat holds aggregated tool outcome statistics for REM phase analysis.
type ToolStat struct {
	Tool        string
	Outcome     Outcome
	Count       int
	AvgDuration time.Duration
	AvgRetries  float64
}

// ReflectionStore is the SQLite data access layer for brain_reflections.
type ReflectionStore struct {
	db *sql.DB
}

// NewStore creates a ReflectionStore backed by the given database connection.
// The caller is responsible for ensuring the brain_reflections table exists
// (typically via Brain's migration system).
func NewStore(db *sql.DB) *ReflectionStore {
	return &ReflectionStore{db: db}
}

// Insert writes a single ReflectionEntry to the brain_reflections table.
func (s *ReflectionStore) Insert(ctx context.Context, entry *ReflectionEntry) error {
	tagsJSON, err := marshalJSONArray(entry.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	relatedJSON, err := marshalJSONArray(entry.RelatedKeys)
	if err != nil {
		return fmt.Errorf("marshal related_keys: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO brain_reflections (
			id, session_key, timestamp, source, type, tool, outcome,
			retry_count, duration_ms, insight, score, tags, related_keys, rem_processed
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		entry.ID,
		entry.SessionKey,
		entry.Timestamp.UTC().Format("2006-01-02 15:04:05"),
		entry.Source,
		string(entry.Type),
		nullableString(entry.Tool),
		string(entry.Outcome),
		entry.RetryCount,
		entry.Duration.Milliseconds(),
		nullableString(entry.Insight),
		entry.Score,
		tagsJSON,
		relatedJSON,
	)
	if err != nil {
		return fmt.Errorf("insert reflection: %w", err)
	}
	return nil
}

// InsertBatch writes multiple ReflectionEntries in a single transaction.
func (s *ReflectionStore) InsertBatch(ctx context.Context, entries []*ReflectionEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch insert: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO brain_reflections (
			id, session_key, timestamp, source, type, tool, outcome,
			retry_count, duration_ms, insight, score, tags, related_keys, rem_processed
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close()

	for _, entry := range entries {
		tagsJSON, err := marshalJSONArray(entry.Tags)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("marshal tags: %w", err)
		}
		relatedJSON, err := marshalJSONArray(entry.RelatedKeys)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("marshal related_keys: %w", err)
		}

		_, err = stmt.ExecContext(ctx,
			entry.ID,
			entry.SessionKey,
			entry.Timestamp.UTC().Format("2006-01-02 15:04:05"),
			entry.Source,
			string(entry.Type),
			nullableString(entry.Tool),
			string(entry.Outcome),
			entry.RetryCount,
			entry.Duration.Milliseconds(),
			nullableString(entry.Insight),
			entry.Score,
			tagsJSON,
			relatedJSON,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("insert reflection %s: %w", entry.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch insert: %w", err)
	}
	return nil
}

// QueryBySession returns all reflection entries for the given session key,
// ordered by timestamp ascending.
func (s *ReflectionStore) QueryBySession(ctx context.Context, sessionKey string) ([]*ReflectionEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_key, timestamp, source, type, tool, outcome,
		       retry_count, duration_ms, insight, score, tags, related_keys, rem_processed
		FROM brain_reflections
		WHERE session_key = ?
		ORDER BY timestamp ASC`, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("query by session: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// QueryUnprocessed returns all reflection entries that have not yet been
// processed by a REM cycle, ordered by timestamp ascending.
func (s *ReflectionStore) QueryUnprocessed(ctx context.Context) ([]*ReflectionEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_key, timestamp, source, type, tool, outcome,
		       retry_count, duration_ms, insight, score, tags, related_keys, rem_processed
		FROM brain_reflections
		WHERE rem_processed = 0
		ORDER BY timestamp ASC`)
	if err != nil {
		return nil, fmt.Errorf("query unprocessed: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// MarkProcessed sets rem_processed = 1 for the given entry IDs.
// An empty slice is a no-op.
func (s *ReflectionStore) MarkProcessed(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE brain_reflections SET rem_processed = 1 WHERE id IN (%s)",
			strings.Join(placeholders, ",")),
		args...)
	if err != nil {
		return fmt.Errorf("mark processed: %w", err)
	}
	return nil
}

// Groom deletes entries that have been processed by REM and are older than
// retentionDays. Returns the number of deleted rows.
func (s *ReflectionStore) Groom(ctx context.Context, retentionDays int) (int, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	cutoffStr := cutoff.Format("2006-01-02 15:04:05")

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM brain_reflections
		WHERE rem_processed = 1 AND timestamp < ?`, cutoffStr)
	if err != nil {
		return 0, fmt.Errorf("groom reflections: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("groom rows affected: %w", err)
	}
	return int(count), nil
}

// QueryToolStats returns aggregated tool outcome statistics for entries
// with a timestamp on or after the given time. Results are grouped by
// tool name and outcome.
func (s *ReflectionStore) QueryToolStats(ctx context.Context, since time.Time) ([]ToolStat, error) {
	sinceStr := since.UTC().Format("2006-01-02 15:04:05")

	rows, err := s.db.QueryContext(ctx, `
		SELECT tool, outcome, COUNT(*) AS cnt,
		       AVG(duration_ms) AS avg_dur_ms,
		       AVG(retry_count) AS avg_retries
		FROM brain_reflections
		WHERE tool IS NOT NULL AND tool != '' AND timestamp >= ?
		GROUP BY tool, outcome
		ORDER BY tool, outcome`, sinceStr)
	if err != nil {
		return nil, fmt.Errorf("query tool stats: %w", err)
	}
	defer rows.Close()

	var stats []ToolStat
	for rows.Next() {
		var ts ToolStat
		var avgDurMs float64
		if err := rows.Scan(&ts.Tool, &ts.Outcome, &ts.Count, &avgDurMs, &ts.AvgRetries); err != nil {
			return nil, fmt.Errorf("scan tool stat: %w", err)
		}
		ts.AvgDuration = time.Duration(avgDurMs * float64(time.Millisecond))
		stats = append(stats, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tool stats rows: %w", err)
	}
	return stats, nil
}

// --- helpers ---

// scanEntries reads all rows from a query into ReflectionEntry slices.
func scanEntries(rows *sql.Rows) ([]*ReflectionEntry, error) {
	var entries []*ReflectionEntry
	for rows.Next() {
		var (
			e            ReflectionEntry
			tsStr        string
			typeStr      string
			outcomeStr   string
			tool         sql.NullString
			insight      sql.NullString
			tagsJSON     sql.NullString
			relatedJSON  sql.NullString
			durationMs   int64
			remProcessed int
		)
		if err := rows.Scan(
			&e.ID, &e.SessionKey, &tsStr, &e.Source, &typeStr,
			&tool, &outcomeStr, &e.RetryCount, &durationMs,
			&insight, &e.Score, &tagsJSON, &relatedJSON, &remProcessed,
		); err != nil {
			return nil, fmt.Errorf("scan reflection: %w", err)
		}

		ts, err := parseTimestamp(tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		e.Timestamp = ts
		e.Type = ReflectionType(typeStr)
		e.Outcome = Outcome(outcomeStr)
		e.Duration = time.Duration(durationMs) * time.Millisecond

		if tool.Valid {
			e.Tool = tool.String
		}
		if insight.Valid {
			e.Insight = insight.String
		}
		if tagsJSON.Valid && tagsJSON.String != "" {
			if err := json.Unmarshal([]byte(tagsJSON.String), &e.Tags); err != nil {
				return nil, fmt.Errorf("unmarshal tags: %w", err)
			}
		}
		if relatedJSON.Valid && relatedJSON.String != "" {
			if err := json.Unmarshal([]byte(relatedJSON.String), &e.RelatedKeys); err != nil {
				return nil, fmt.Errorf("unmarshal related_keys: %w", err)
			}
		}

		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return entries, nil
}

// marshalJSONArray marshals a string slice to a JSON array string.
// Nil or empty slices produce nil (SQL NULL).
func marshalJSONArray(s []string) (interface{}, error) {
	if len(s) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// parseTimestamp tries multiple formats that SQLite/modernc may return.
func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %q", s)
}

// nullableString returns nil for empty strings, allowing SQL NULL storage.
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
