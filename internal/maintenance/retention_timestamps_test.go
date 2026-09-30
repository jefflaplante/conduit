package maintenance

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/database"
	"conduit/internal/sessions"
)

// conduit-a636: once gateway.db migration 10 has rewritten legacy
// messages.timestamp / sessions.created_at text into the canonical layout,
// an SQL text comparison against the cutoff selects exactly the sessions the
// Go plan selects (before, a non-UTC time.String() value compared wrongly).
func TestRetention_SQLCutoffAgreesAfterMigration10(t *testing.T) {
	db, _ := openGatewayDB(t)
	ctx := context.Background()
	seed(t, db)
	// Active 6h after the cutoff, written in a UTC-12 zone: its legacy text
	// sorts before the cutoff although the instant is after it.
	cutoff := testNow.UTC().AddDate(0, 0, -defaultPolicy().RetentionDays)
	addSession(t, db, "subagent_zone_1", daysAgo(60))
	addMessage(t, db, "subagent_zone_1", "late", goString(cutoff.Add(6*time.Hour).In(time.FixedZone("-12", -12*3600))))
	// A legacy created_at, too.
	if _, err := db.Exec(`UPDATE sessions SET created_at = ? WHERE key = 'subagent_zone_1'`,
		goString(daysAgo(60).In(time.FixedZone("PST", -8*3600)))); err != nil {
		t.Fatal(err)
	}

	sqlOld := func() []string {
		t.Helper()
		rows, err := db.Query(`SELECT key FROM sessions s
			WHERE max(CAST(s.updated_at AS TEXT), coalesce((SELECT max(CAST(timestamp AS TEXT)) FROM messages m WHERE m.session_key = s.key), '')) < ?
			ORDER BY key`, sessions.FormatUpdatedAt(cutoff))
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}
			if matchPrefix(k, config.DefaultPrunableSessionPrefixes) != "" {
				keys = append(keys, k)
			}
		}
		return keys
	}
	planned := func() []string {
		t.Helper()
		rep, err := PlanPrune(ctx, db, defaultPolicy(), testNow)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, c := range rep.candidates {
			keys = append(keys, c.key)
		}
		sort.Strings(keys)
		return keys
	}
	want := planned()
	if strings.Contains(strings.Join(want, ","), "subagent_zone_1") {
		t.Fatalf("plan prunes the active session: %v", want)
	}
	if got := sqlOld(); strings.Join(got, ",") == strings.Join(want, ",") {
		t.Fatalf("fixture does not show the legacy SQL mismatch: %v", got)
	}

	// The rows above were written in legacy formats after the migrations
	// ran; replay migration 10 as an upgrade would.
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 10`); err != nil {
		t.Fatal(err)
	}
	if err := database.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	if bad := count(t, db, `SELECT COUNT(*) FROM messages WHERE NOT (`+database.CanonicalTimeGlobSQL("timestamp")+`)`); bad != 0 {
		t.Fatalf("%d messages not canonical", bad)
	}
	if bad := count(t, db, `SELECT COUNT(*) FROM sessions WHERE NOT (`+database.CanonicalTimeGlobSQL("created_at")+`)`); bad != 0 {
		t.Fatalf("%d sessions.created_at not canonical", bad)
	}
	if got := planned(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plan changed after migration: %v, want %v", got, want)
	}
	if got := sqlOld(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SQL cutoff selects %v, plan %v", got, want)
	}

	// Message-level cutoff: the SQL count equals the Go count.
	var goOld int
	rows, err := db.Query(`SELECT CAST(timestamp AS TEXT) FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		if ts, ok := sessions.ParseStoredTime(v); ok && ts.Before(cutoff) {
			goOld++
		}
	}
	rows.Close()
	if n := count(t, db, `SELECT COUNT(*) FROM messages WHERE timestamp < ?`, sessions.FormatUpdatedAt(cutoff)); n != goOld || n == 0 {
		t.Fatalf("SQL old messages = %d, Go = %d", n, goOld)
	}
}
