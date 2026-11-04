package database

import (
	"testing"
	"time"
)

// TestMigration13_TelegramPairingsStatus verifies migration #13 (C1): legacy
// is_active=0 rows, which may be superseded codes rather than approvals,
// fail closed to superseded; pending rows stay pending; new rows default to
// pending; and the L13 time columns are rewritten into StoredTimeLayout.
func TestMigration13_TelegramPairingsStatus(t *testing.T) {
	db := openDBAtVersion(t, 12)
	if topicsColumns(t, db, "telegram_pairings")["status"] {
		t.Fatal("status exists before migration 13")
	}

	// Legacy rows as GeneratePairingCode wrote them: time.Time.String() in
	// a zone west of UTC.
	west := time.FixedZone("PST", -8*3600)
	future := time.Now().Add(30 * time.Minute).In(west)
	past := time.Now().Add(-2 * time.Hour).In(west)
	ins := func(code, user string, created, expires time.Time, active int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO telegram_pairings (code, user_id, created_at, expires_at, is_active) VALUES (?, ?, ?, ?, ?)`,
			code, user, created.String(), expires.String(), active); err != nil {
			t.Fatal(err)
		}
	}
	ins("old-inactive", "123456789", past, past.Add(time.Hour), 0)
	ins("old-pending", "987654321", future.Add(-time.Hour), future, 1)

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if v, err := getCurrentVersion(db); err != nil || v < 13 || v != GetMigrations()[len(GetMigrations())-1].Version {
		t.Fatalf("version = %d, %v; want the latest (>= 13)", v, err)
	}

	status := func(code string) string {
		t.Helper()
		var s string
		if err := db.QueryRow(`SELECT status FROM telegram_pairings WHERE code = ?`, code).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status("old-inactive"); s != "superseded" {
		t.Errorf("legacy inactive row status = %q, want superseded (fail closed)", s)
	}
	if s := status("old-pending"); s != "pending" {
		t.Errorf("legacy active row status = %q, want pending", s)
	}
	var approved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telegram_pairings WHERE status = 'approved'`).Scan(&approved); err != nil || approved != 0 {
		t.Fatalf("approved rows after migration = %d, %v; want 0", approved, err)
	}

	// Times are canonical UTC, so the pending code compares as unexpired
	// against a UTC "now".
	var canonical int
	if err := db.QueryRow(`SELECT COUNT(*) FROM telegram_pairings WHERE ` + CanonicalTimeGlobSQL("expires_at") + ` AND ` + CanonicalTimeGlobSQL("created_at")).Scan(&canonical); err != nil || canonical != 2 {
		t.Fatalf("canonical rows = %d, %v; want 2", canonical, err)
	}
	var live string
	if err := db.QueryRow(`SELECT code FROM telegram_pairings WHERE status = 'pending' AND expires_at > ?`, FormatStoredTime(time.Now())).Scan(&live); err != nil || live != "old-pending" {
		t.Fatalf("unexpired pending = %q, %v; want old-pending", live, err)
	}

	// New rows default to pending.
	if _, err := db.Exec(`INSERT INTO telegram_pairings (code, user_id, expires_at) VALUES ('new', '123456789', ?)`, FormatStoredTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if s := status("new"); s != "pending" {
		t.Errorf("new row status = %q, want pending", s)
	}

	// Replaying the migration keeps an approval made after it.
	if _, err := db.Exec(`UPDATE telegram_pairings SET status = 'approved', is_active = 0 WHERE code = 'old-pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 13`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if s := status("old-pending"); s != "approved" {
		t.Errorf("approved row after replay = %q, want approved", s)
	}
}
