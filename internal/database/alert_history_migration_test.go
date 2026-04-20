package database

import (
	"testing"
)

// TestMigration8_AlertHistoryTableCreated verifies migration #8 creates the
// alert_history audit-trail table, its expected columns, and supporting
// indexes. Guards conduit-2uvp: SRE alert/audit trail.
func TestMigration8_AlertHistoryTableCreated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Table exists.
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='alert_history'`,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected alert_history table to exist, got count=%d", count)
	}

	// All expected columns exist.
	expectedCols := map[string]bool{
		"id":            false,
		"fired_at":      false,
		"alert_type":    false,
		"severity":      false,
		"source":        false,
		"message":       false,
		"details":       false,
		"action_taken":  false,
		"action_result": false,
	}
	rows, err := db.Query(`PRAGMA table_info(alert_history)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if _, ok := expectedCols[name]; ok {
			expectedCols[name] = true
		}
	}
	for name, seen := range expectedCols {
		if !seen {
			t.Errorf("expected column %q missing from alert_history", name)
		}
	}

	// Expected indexes exist.
	expectedIndexes := []string{
		"idx_alert_history_fired_at",
		"idx_alert_history_alert_type",
		"idx_alert_history_severity",
	}
	for _, idx := range expectedIndexes {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
			idx,
		).Scan(&got)
		if err != nil {
			t.Errorf("expected index %s to exist: %v", idx, err)
		}
	}

	// Inserting a row exercises the NOT NULL constraints and defaults.
	if _, err := db.Exec(
		`INSERT INTO alert_history (alert_type, severity, message) VALUES (?, ?, ?)`,
		"heartbeat", "warning", "cpu above threshold",
	); err != nil {
		t.Fatalf("insert minimal row: %v", err)
	}

	// fired_at defaults to CURRENT_TIMESTAMP and should be populated.
	var firedAt string
	if err := db.QueryRow(
		`SELECT fired_at FROM alert_history WHERE alert_type='heartbeat'`,
	).Scan(&firedAt); err != nil {
		t.Fatalf("read fired_at: %v", err)
	}
	if firedAt == "" {
		t.Error("fired_at should be populated by CURRENT_TIMESTAMP default")
	}
}
