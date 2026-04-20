package heartbeat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AlertHistoryEntry is an audit-trail row for an alert that was fired. It
// captures what was alerted and what was done about it (delivery target,
// success/failure). Used for SRE compliance and post-mortems (conduit-2uvp).
//
// The Details field is stored as JSON in the database. Callers can pass any
// JSON-marshalable value or nil.
type AlertHistoryEntry struct {
	ID           int64     `json:"id,omitempty"`
	FiredAt      time.Time `json:"fired_at,omitempty"`
	AlertType    string    `json:"alert_type"`
	Severity     string    `json:"severity"`
	Source       string    `json:"source,omitempty"`
	Message      string    `json:"message"`
	Details      any       `json:"details,omitempty"`
	ActionTaken  string    `json:"action_taken,omitempty"`
	ActionResult string    `json:"action_result,omitempty"`
}

// AlertAuditor persists alert-history entries. It is intentionally a thin
// wrapper around *sql.DB — the repo uses direct SQL elsewhere (see
// internal/gateway/dlq.go) rather than a full repository layer.
//
// Writes are best-effort: a nil auditor or nil DB short-circuits to a no-op so
// the delivery path does not block on an unavailable audit sink.
type AlertAuditor struct {
	db *sql.DB
}

// NewAlertAuditor creates an auditor bound to the given database handle. The
// alert_history table is created by migration #8.
func NewAlertAuditor(db *sql.DB) *AlertAuditor {
	return &AlertAuditor{db: db}
}

// RecordAlert inserts an audit entry for a fired alert. The fired_at column
// defaults to CURRENT_TIMESTAMP server-side if the caller leaves
// entry.FiredAt zero.
//
// A short context deadline is applied so a stuck DB never blocks the delivery
// path. Failures return an error but are generally treated as non-fatal by the
// caller — the audit trail is best-effort.
func (a *AlertAuditor) RecordAlert(ctx context.Context, entry AlertHistoryEntry) error {
	if a == nil || a.db == nil {
		return nil
	}

	if entry.AlertType == "" {
		return fmt.Errorf("alert_type is required")
	}
	if entry.Severity == "" {
		return fmt.Errorf("severity is required")
	}
	if entry.Message == "" {
		return fmt.Errorf("message is required")
	}

	// Serialize details to JSON. nil details leaves the column NULL.
	var detailsJSON sql.NullString
	if entry.Details != nil {
		b, err := json.Marshal(entry.Details)
		if err != nil {
			return fmt.Errorf("marshal details: %w", err)
		}
		detailsJSON = sql.NullString{String: string(b), Valid: true}
	}

	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Use the caller's FiredAt when provided; otherwise let SQLite default.
	if !entry.FiredAt.IsZero() {
		_, err := a.db.ExecContext(writeCtx,
			`INSERT INTO alert_history
			   (fired_at, alert_type, severity, source, message, details, action_taken, action_result)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.FiredAt.UTC(),
			entry.AlertType, entry.Severity, nullIfEmpty(entry.Source),
			entry.Message, detailsJSON,
			nullIfEmpty(entry.ActionTaken), nullIfEmpty(entry.ActionResult),
		)
		return err
	}

	_, err := a.db.ExecContext(writeCtx,
		`INSERT INTO alert_history
		   (alert_type, severity, source, message, details, action_taken, action_result)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.AlertType, entry.Severity, nullIfEmpty(entry.Source),
		entry.Message, detailsJSON,
		nullIfEmpty(entry.ActionTaken), nullIfEmpty(entry.ActionResult),
	)
	return err
}

// ListRecent returns the most recent alert-history entries, newest first.
// A limit of <=0 is clamped to 100. Callers that need filtering or pagination
// should wait for the follow-up ticket that exposes richer query options.
func (a *AlertAuditor) ListRecent(ctx context.Context, limit int) ([]AlertHistoryEntry, error) {
	if a == nil || a.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(readCtx,
		`SELECT id, fired_at, alert_type, severity,
		        COALESCE(source, ''), message, COALESCE(details, ''),
		        COALESCE(action_taken, ''), COALESCE(action_result, '')
		   FROM alert_history
		  ORDER BY fired_at DESC, id DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query alert_history: %w", err)
	}
	defer rows.Close()

	var out []AlertHistoryEntry
	for rows.Next() {
		var e AlertHistoryEntry
		var detailsRaw string
		if err := rows.Scan(
			&e.ID, &e.FiredAt, &e.AlertType, &e.Severity,
			&e.Source, &e.Message, &detailsRaw,
			&e.ActionTaken, &e.ActionResult,
		); err != nil {
			return nil, fmt.Errorf("scan alert_history row: %w", err)
		}
		if detailsRaw != "" {
			// Decode into a generic interface so callers can inspect arbitrary
			// JSON shapes; leave as nil if decode fails to avoid losing rows.
			var v any
			if err := json.Unmarshal([]byte(detailsRaw), &v); err == nil {
				e.Details = v
			} else {
				e.Details = detailsRaw
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert_history rows: %w", err)
	}
	return out, nil
}

// nullIfEmpty returns a NULL SQL value when s is empty, so empty strings don't
// populate optional TEXT columns.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
