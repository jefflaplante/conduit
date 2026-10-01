package database

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

// conduit-a636: migration 10 rewrites messages.timestamp and
// sessions.created_at into StoredTimeLayout.

func TestParseStoredTime_Formats(t *testing.T) {
	utc := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-02-13 01:03:25.453024355 +0000 UTC m=+2227.791803474", utc("2026-02-13T01:03:25.453024355Z")},
		{"2026-02-13 01:03:25.453024355 -0800 PST m=+2227.791803474", utc("2026-02-13T09:03:25.453024355Z")},
		{"2026-07-01 12:00:00.5 +0200 CEST", utc("2026-07-01T10:00:00.5Z")},
		{"2026-03-01 10:00:00 +0530 +0530", utc("2026-03-01T04:30:00Z")},
		{"2026-01-06 10:00:00 +1000 ChST m=-0.000001", utc("2026-01-06T00:00:00Z")},
		{"2026-01-06 10:00:00 -0300 -03", utc("2026-01-06T13:00:00Z")},
		{"2026-01-01 00:00:00.000000000", utc("2026-01-01T00:00:00Z")},
		{"2026-01-02 03:04:05", utc("2026-01-02T03:04:05Z")},
		{"2026-01-03T04:05:06Z", utc("2026-01-03T04:05:06Z")},
		{"2026-01-03T04:05:06.123456789+02:00", utc("2026-01-03T02:05:06.123456789Z")},
		{"2026-01-04 05:06:07.1-07:00", utc("2026-01-04T12:06:07.1Z")},
		{"2026-01-04 05:06:07Z", utc("2026-01-04T05:06:07Z")},
		{"2026-01-05T06:07:08", utc("2026-01-05T06:07:08Z")},
		{"2026-01-07", utc("2026-01-07T00:00:00Z")},
		{"  2026-01-07 01:02  ", utc("2026-01-07T01:02:00Z")},
	}
	for _, c := range cases {
		got, ok := ParseStoredTime(c.in)
		if !ok || !got.Equal(c.want) {
			t.Errorf("ParseStoredTime(%q) = %v, %v; want %v", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "not a time", "2026-13-01 00:00:00", "yesterday m=+1", "1700000000"} {
		if got, ok := ParseStoredTime(bad); ok {
			t.Errorf("ParseStoredTime(%q) = %v, want failure", bad, got)
		}
	}

	// Real time.Time.String() output, including a monotonic reading and a
	// fabricated zone, parses back to the same instant.
	now := time.Now()
	for _, v := range []time.Time{now, now.UTC(), now.In(time.FixedZone("XYZ", -3*3600-1800)), now.Round(0)} {
		got, ok := ParseStoredTime(v.String())
		if !ok || !got.Equal(v) {
			t.Errorf("ParseStoredTime(%q) = %v, %v", v.String(), got, ok)
		}
		if FormatStoredTime(v) != v.UTC().Format(StoredTimeLayout) || len(FormatStoredTime(v)) != len(StoredTimeLayout) {
			t.Errorf("FormatStoredTime(%v) = %q", v, FormatStoredTime(v))
		}
	}
}

type tsCase struct {
	id    string
	value any    // what the row holds before migration 10
	want  string // stored text afterwards ("" = unchanged)
}

var messageTSCases = []tsCase{
	{"go-utc", "2026-02-13 01:03:25.453024355 +0000 UTC m=+2227.791803474", "2026-02-13 01:03:25.453024355"},
	{"go-pst", "2026-02-13 01:03:25.453024355 -0800 PST m=+2227.791803474", "2026-02-13 09:03:25.453024355"},
	{"go-cest", "2026-07-01 12:00:00.5 +0200 CEST", "2026-07-01 10:00:00.500000000"},
	{"go-noname", "2026-03-01 10:00:00 +0530 +0530 m=+0.1", "2026-03-01 04:30:00.000000000"},
	{"go-mixedcase", "2026-01-06 10:00:00 +1000 ChST", "2026-01-06 00:00:00.000000000"},
	{"canonical", "2026-01-01 00:00:00.000000000", ""},
	{"current-ts", "2026-01-02 03:04:05", "2026-01-02 03:04:05.000000000"},
	{"rfc3339", "2026-01-03T04:05:06Z", "2026-01-03 04:05:06.000000000"},
	{"rfc3339nano", "2026-01-03T04:05:06.123456789+02:00", "2026-01-03 02:05:06.123456789"},
	{"sqlite-fmt", "2026-01-04 05:06:07.1-07:00", "2026-01-04 12:06:07.100000000"},
	{"zoneless-t", "2026-01-05T06:07:08", "2026-01-05 06:07:08.000000000"},
	{"date-only", "2026-01-07", "2026-01-07 00:00:00.000000000"},
	{"garbage", "not a time", ""},
	{"empty", "", ""},
	{"integer", int64(1700000000), ""},
	{"null", nil, ""},
}

// seedTimestamps writes rows whose timestamps are in every format found in
// old databases, into a database at migration 9.
func seedTimestamps(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, c := range messageTSCases {
		if _, err := db.Exec(`INSERT INTO messages (id, session_key, role, content, timestamp) VALUES (?, 's1', 'user', ?, ?)`,
			c.id, "hello message "+c.id, c.value); err != nil {
			t.Fatalf("insert %s: %v", c.id, err)
		}
		if _, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id, created_at) VALUES (?, 'u', 'c', ?)`,
			"cron_"+c.id, c.value); err != nil {
			t.Fatalf("insert session %s: %v", c.id, err)
		}
	}
}

// rawColumn returns table.column per key as stored (text, or typeof for
// non-text values).
func rawColumn(t *testing.T, db *sql.DB, table, keyCol, col string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT ` + keyCol + `, CASE typeof(` + col + `) WHEN 'text' THEN ` + col +
		` ELSE typeof(` + col + `) || ':' || coalesce(CAST(` + col + ` AS TEXT), '') END FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	return out
}

func triggerSQL(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			t.Fatal(err)
		}
		out[n] = s
	}
	return out
}

func ftsRowids(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT message_id, rowid FROM messages_fts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var r int64
		if err := rows.Scan(&id, &r); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	return out
}

func TestMigration10_NormalizesTimestamps(t *testing.T) {
	db := openDBAtVersion(t, 9)
	seedTimestamps(t, db)
	beforeMsgs := rawColumn(t, db, "messages", "id", "timestamp")
	beforeSess := rawColumn(t, db, "sessions", "key", "created_at")
	beforeTriggers := triggerSQL(t, db)
	beforeFTS := ftsRowids(t, db)
	assertFTSMirrorsMessages(t, db, "before migration 10")

	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	if v, err := getCurrentVersion(db); err != nil || v != 10 {
		t.Fatalf("version = %d, %v", v, err)
	}

	afterMsgs := rawColumn(t, db, "messages", "id", "timestamp")
	afterSess := rawColumn(t, db, "sessions", "key", "created_at")
	for _, c := range messageTSCases {
		for _, got := range []struct {
			name          string
			before, after string
		}{
			{"messages." + c.id, beforeMsgs[c.id], afterMsgs[c.id]},
			{"sessions.cron_" + c.id, beforeSess["cron_"+c.id], afterSess["cron_"+c.id]},
		} {
			want := c.want
			if want == "" {
				want = got.before // unchanged
			}
			if got.after != want {
				t.Errorf("%s: %q -> %q, want %q", got.name, got.before, got.after, want)
				continue
			}
			// Same instant as before.
			if bt, ok := ParseStoredTime(got.before); ok {
				at, ok := ParseStoredTime(got.after)
				if !ok || !at.Equal(bt) {
					t.Errorf("%s: instant moved %v -> %v", got.name, bt, at)
				}
			}
		}
	}
	// The session openDBAtVersion made (CURRENT_TIMESTAMP default) is canonical now.
	if v := afterSess["s1"]; len(v) != len(StoredTimeLayout) {
		t.Errorf("s1 created_at = %q", v)
	}

	// Triggers are back exactly as they were, the FTS index was not
	// touched (same rowids) and still mirrors messages.
	if got := triggerSQL(t, db); !reflect.DeepEqual(got, beforeTriggers) {
		t.Fatalf("triggers changed:\n%v\nwant\n%v", got, beforeTriggers)
	}
	if got := ftsRowids(t, db); !reflect.DeepEqual(got, beforeFTS) {
		t.Fatalf("messages_fts rewritten: %v, before %v", got, beforeFTS)
	}
	assertFTSMirrorsMessages(t, db, "after migration 10")

	// Idempotent: a second pass rewrites nothing.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := normalizeTimestampColumnsTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		if s.Rewritten != 0 {
			t.Errorf("second pass: %s", s)
		}
		if s.Unparseable != 2 || s.NonText != 1 {
			t.Errorf("second pass counts: %s", s)
		}
	}
	if got := rawColumn(t, db, "sessions", "key", "created_at"); !reflect.DeepEqual(got, afterSess) {
		t.Fatalf("second pass changed sessions")
	}
	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}

	// The restored triggers still keep messages_fts in sync.
	if _, err := db.Exec(`UPDATE messages SET content = 'changed words' WHERE id = 'go-pst'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM messages WHERE id = 'garbage'`); err != nil {
		t.Fatal(err)
	}
	assertFTSMirrorsMessages(t, db, "after update/delete")
}

// After migration 10, text comparisons and ORDER BY on the columns are
// chronological, and the driver scans the values back to the same instants.
func TestMigration10_SQLComparisonsWork(t *testing.T) {
	db := openDBAtVersion(t, 9)
	// Same instants in different zones: text order differs from time order.
	base := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	zones := []*time.Location{time.FixedZone("PST", -8*3600), time.FixedZone("CET", 3600), time.UTC, time.FixedZone("IST", 5*3600+1800)}
	var want []string
	for i := 0; i < 8; i++ {
		ts := base.Add(time.Duration(i) * 90 * time.Minute).In(zones[i%len(zones)])
		id := string(rune('a' + i))
		want = append(want, id)
		if _, err := db.Exec(`INSERT INTO messages (id, session_key, role, content, timestamp) VALUES (?, 's1', 'user', 'x', ?)`,
			id, ts.String()+" m=+1.0"); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(q string, args ...any) []string {
		rows, err := db.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	order := `SELECT id FROM messages WHERE session_key = 's1' ORDER BY timestamp`
	if got := ids(order); reflect.DeepEqual(got, want) {
		t.Fatalf("fixture does not exercise mixed zones: %v", got)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	if got := ids(order); !reflect.DeepEqual(got, want) {
		t.Fatalf("ORDER BY timestamp = %v, want %v", got, want)
	}
	cutoff := FormatStoredTime(base.Add(4 * 90 * time.Minute))
	if got := ids(`SELECT id FROM messages WHERE timestamp < ? ORDER BY timestamp`, cutoff); strings.Join(got, "") != "abcd" {
		t.Fatalf("timestamp < cutoff = %v", got)
	}

	var scanned time.Time
	if err := db.QueryRow(`SELECT timestamp FROM messages WHERE id = 'b'`).Scan(&scanned); err != nil {
		t.Fatal(err)
	}
	if !scanned.Equal(base.Add(90 * time.Minute)) {
		t.Fatalf("scanned %v", scanned)
	}
}
