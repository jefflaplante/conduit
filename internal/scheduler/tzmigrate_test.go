package scheduler

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// conduit-31jg.60 tests.

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

// During PDT (UTC-7): reference instant for "today's wall clock".
var refPDT = time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)

func TestConvertScheduleTZ(t *testing.T) {
	utc := time.UTC
	la := mustLoc(t, "America/Los_Angeles")
	opts := TZMigrateOptions{From: utc, To: la, Now: refPDT}

	tests := []struct {
		name    string
		in      string
		want    string // "" with skip/refuse
		skip    bool
		refuse  string // substring of the refusal
		optsNow time.Time
	}{
		{name: "daily morning", in: "0 5 13 * * *", want: "CRON_TZ=America/Los_Angeles 0 5 6 * * *"},
		{name: "weekday list unchanged same day", in: "0 5 13 * * 1,2,4,5", want: "CRON_TZ=America/Los_Angeles 0 5 6 * * 1,2,4,5"},
		{name: "5-field keeps 5 fields", in: "10 15 * * 1-5", want: "CRON_TZ=America/Los_Angeles 10 8 * * 1-5"},
		// 02:00 UTC is 19:00 the previous Pacific day; unrestricted days need no shift.
		{name: "wrap, no day restriction", in: "0 0 2 * * *", want: "CRON_TZ=America/Los_Angeles 0 0 19 * * *"},
		// Wraparound with DOW restriction: 02:00 UTC Monday = 19:00 PDT Sunday.
		{name: "wrap shifts DOW", in: "0 0 2 * * 1", want: "CRON_TZ=America/Los_Angeles 0 0 19 * * 0"},
		{name: "wrap shifts DOW range and wraps sunday", in: "0 30 3 * * 0-2", want: "CRON_TZ=America/Los_Angeles 0 30 20 * * 0,1,6"},
		{name: "wrap shifts DOW names", in: "0 0 1 * * MON-FRI", want: "CRON_TZ=America/Los_Angeles 0 0 18 * * 0-4"},
		// Wraparound with DOM restriction: 03:00 UTC on the 15th = 20:00 PDT on the 14th.
		{name: "wrap shifts DOM", in: "0 0 3 15 * *", want: "CRON_TZ=America/Los_Angeles 0 0 20 14 * *"},
		{name: "wrap DOM 1 refused", in: "0 0 3 1 * *", refuse: "cross a month boundary"},
		{name: "wrap DOM 31 refused", in: "0 0 3 31 * *", refuse: "cross a month boundary"},
		{name: "wrap with month restriction refused", in: "0 0 3 * 12 *", refuse: "month field"},
		{name: "mixed days with DOW restriction refused", in: "0 0 6,10 * * 1", refuse: "different"},
		{name: "mixed days unrestricted ok", in: "0 0 16,18,20,22,0 * * *", want: "CRON_TZ=America/Los_Angeles 0 0 9,11,13,15,17 * * *"},
		{name: "hour step expands", in: "0 0 */10 * * *", want: "CRON_TZ=America/Los_Angeles 0 0 3,13,17 * * *"},
		{name: "hour range", in: "0 0 14-18 * * *", want: "CRON_TZ=America/Los_Angeles 0 0 7-11 * * *"},
		{name: "idempotent: already zoned", in: "CRON_TZ=America/Los_Angeles 0 0 9 * * *", skip: true},
		{name: "TZ= prefix skipped", in: "TZ=UTC 0 0 9 * * *", skip: true},
		{name: "interval skipped", in: "@every 5m", skip: true},
		{name: "hour star skipped", in: "0 */10 * * * *", skip: true},
		{name: "daily descriptor", in: "@daily", want: "CRON_TZ=America/Los_Angeles 0 0 17 * * *"},
		// Date-pinned (one-shot style) in PST: keep the instant, offset -8.
		{name: "date-pinned january uses PST", in: "0 0 9 4 1 *", want: "CRON_TZ=America/Los_Angeles 0 0 1 4 1 *"},
		{name: "date-pinned october uses PDT", in: "0 0 16 14 10 *", want: "CRON_TZ=America/Los_Angeles 0 0 9 14 10 *"},
		{name: "month list spanning DST refused", in: "0 0 16 1 1,7 *", refuse: "different UTC offsets"},
		// Reference instant in PST (winter): 13:00 UTC is 05:00 PST.
		{name: "reference in PST", in: "0 0 13 * * *", want: "CRON_TZ=America/Los_Angeles 0 0 5 * * *", optsNow: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{name: "bad field", in: "0 0 25 * * *", refuse: "hour"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := opts
			if !tc.optsNow.IsZero() {
				o.Now = tc.optsNow
			}
			got, _, err := ConvertScheduleTZ(tc.in, o)
			var skip errSkip
			switch {
			case tc.skip:
				if !errors.As(err, &skip) {
					t.Fatalf("want skip, got %q err=%v", got, err)
				}
			case tc.refuse != "":
				if err == nil || errors.As(err, &skip) || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want refusal containing %q, got %q err=%v", tc.refuse, got, err)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				// Must be accepted by the scheduler's own normalizer.
				if _, err := normalizeSchedule(got, JobTypeGo); err != nil {
					t.Fatalf("scheduler rejects %q: %v", got, err)
				}
			}
		})
	}
}

// Across the Nov 1 2026 DST boundary, the migrated job stays on the Pacific
// wall clock while the old UTC expression drifts an hour.
func TestConvertScheduleTZ_DSTBoundary(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	opts := TZMigrateOptions{From: time.UTC, To: la, Now: refPDT}
	cases := []struct {
		old     string
		wantH   int
		wantDow time.Weekday // -1 = any
	}{
		{"0 5 13 * * 1,2,4,5", 6, -1},
		{"0 0 2 * * 1", 19, time.Sunday}, // wraps to previous day
		{"0 0 3 15 * *", 20, -1},
	}
	for _, c := range cases {
		newExpr, _, err := ConvertScheduleTZ(c.old, opts)
		if err != nil {
			t.Fatalf("%s: %v", c.old, err)
		}
		oldS, _ := ParseJobSchedule(c.old, JobTypeGo)
		newS, err := ParseJobSchedule(newExpr, JobTypeGo)
		if err != nil {
			t.Fatalf("parse %q: %v", newExpr, err)
		}
		// Before DST ends both agree.
		before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		if a, b := oldS.Next(before), newS.Next(before); !a.Equal(b) {
			t.Errorf("%s: before DST end old %v != new %v", c.old, a, b)
		}
		// After DST ends: new keeps wall-clock hour, old drifts by -1h.
		after := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
		for i, tt := 0, after; i < 3; i++ {
			tt = newS.Next(tt)
			lt := tt.In(la)
			if lt.Hour() != c.wantH {
				t.Errorf("%s -> %s: fire %v at hour %d, want %d", c.old, newExpr, lt, lt.Hour(), c.wantH)
			}
			if c.wantDow >= 0 && lt.Weekday() != c.wantDow {
				t.Errorf("%s -> %s: fire on %v, want %v", c.old, newExpr, lt.Weekday(), c.wantDow)
			}
			if c.old == "0 0 3 15 * *" && lt.Day() != 14 {
				t.Errorf("DOM shift: fire on day %d, want 14", lt.Day())
			}
		}
		if lt := oldS.Next(after).In(la); lt.Hour() == c.wantH {
			t.Errorf("%s: expected old UTC expression to drift after DST, got hour %d", c.old, lt.Hour())
		}
	}
}

const sampleJobs = `[
  {
    "id": "a1",
    "name": "Morning Briefing",
    "schedule": "0 5 13 * * 1,2,4,5",
    "type": "go",
    "command": "brief & send > now",
    "target": "123",
    "enabled": true,
    "skills": [
      "briefing"
    ],
    "created_at": "2026-08-31T14:57:12.963525126Z",
    "last_run": "2026-09-25T13:05:00.001131976Z",
    "next_run": "2026-09-28T13:05:00Z",
    "run_count": 7,
    "metadata": {"schedule": "not this one", "n": 1.50}
  },
  {
    "id": "b2",
    "name": "Heartbeat",
    "schedule": "0 */10 * * * *",
    "type": "go",
    "command": "hb",
    "enabled": true,
    "created_at": "2026-08-25T20:52:54.005206736Z",
    "run_count": 0
  },
  {
    "id": "c3",
    "name": "Sys",
    "schedule": "0 13 * * *",
    "type": "system",
    "command": "echo",
    "enabled": true,
    "created_at": "2026-08-25T20:52:54Z",
    "run_count": 0
  },
  {
    "id": "d4",
    "name": "Disabled check",
    "schedule": "0 0 2 * * 1",
    "type": "go",
    "command": "x",
    "enabled": false,
    "created_at": "2026-08-25T20:52:54Z",
    "run_count": 0
  },
  {
    "id": "e5",
    "name": "Bad split",
    "schedule": "0 0 6,10 * * 1",
    "type": "go",
    "command": "x",
    "enabled": true,
    "created_at": "2026-08-25T20:52:54Z",
    "run_count": 0
  }
]
`

func TestPlanTZMigration(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	jobs, err := PlanTZMigration([]byte(sampleJobs), TZMigrateOptions{From: time.UTC, To: la, Now: refPDT})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TZAction{"a1": TZConvert, "b2": TZSkip, "c3": TZSkip, "d4": TZConvert, "e5": TZRefuse}
	for _, j := range jobs {
		if j.Action != want[j.ID] {
			t.Errorf("%s: action %s, want %s (%s)", j.ID, j.Action, want[j.ID], j.Reason)
		}
		if j.Action != TZConvert && j.NewSchedule != j.OldSchedule {
			t.Errorf("%s: non-converted job changed schedule", j.ID)
		}
	}
	if jobs[3].NewSchedule != "CRON_TZ=America/Los_Angeles 0 0 19 * * 0" {
		t.Errorf("d4: %q", jobs[3].NewSchedule)
	}
	if !strings.Contains(strings.Join(jobs[3].Flags, ";"), "disabled") {
		t.Errorf("d4 should be flagged disabled: %v", jobs[3].Flags)
	}
}

func TestPlanTZMigration_NameClockFlag(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	data := `[{"id":"x","name":"Report (Weekday 8am)","schedule":"0 0 13 * * 1-5","type":"go","command":"c","enabled":true,"created_at":"2026-01-01T00:00:00Z","run_count":0}]`
	jobs, err := PlanTZMigration([]byte(data), TZMigrateOptions{From: time.UTC, To: la, Now: refPDT})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(jobs[0].Flags, ";"), `name says "8am"`) {
		t.Errorf("expected name/clock mismatch flag, got %v", jobs[0].Flags)
	}
}

func TestRewriteSchedules_PreservesEverythingElse(t *testing.T) {
	data := []byte(sampleJobs)
	out, err := RewriteSchedules(data, map[int]string{0: "CRON_TZ=America/Los_Angeles 0 5 6 * * 1,2,4,5"})
	if err != nil {
		t.Fatal(err)
	}
	wantOut := strings.Replace(sampleJobs, `"schedule": "0 5 13 * * 1,2,4,5"`, `"schedule": "CRON_TZ=America/Los_Angeles 0 5 6 * * 1,2,4,5"`, 1)
	if string(out) != wantOut {
		t.Fatalf("rewrite changed more than the schedule literal:\n%s", out)
	}
	// Nested metadata "schedule" and escaped command untouched.
	if !strings.Contains(string(out), `"schedule": "not this one"`) || !strings.Contains(string(out), `&`) {
		t.Fatal("nested/escaped content altered")
	}
}

func TestApplyTZMigration_BackupAtomicIdempotent(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	dir := t.TempDir()
	path := filepath.Join(dir, "cron_jobs.json")
	if err := os.WriteFile(path, []byte(sampleJobs), 0600); err != nil {
		t.Fatal(err)
	}
	opts := TZMigrateOptions{From: time.UTC, To: la, Now: refPDT}

	res, err := ApplyTZMigration(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 {
		t.Fatalf("changed %d, want 2", res.Changed)
	}
	wantBackup := path + ".bak-20260927T010000Z"
	if res.BackupPath != wantBackup {
		t.Fatalf("backup path %q, want %q", res.BackupPath, wantBackup)
	}
	bak, err := os.ReadFile(wantBackup)
	if err != nil || string(bak) != sampleJobs {
		t.Fatalf("backup content mismatch: %v", err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("unexpected files: %v", names)
	}

	// Preserved fields: every field other than schedule is identical.
	after, _ := os.ReadFile(path)
	var b, a []map[string]interface{}
	_ = json.Unmarshal(bak, &b)
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatal(err)
	}
	for i := range b {
		if i == 0 || i == 3 {
			if !strings.HasPrefix(a[i]["schedule"].(string), "CRON_TZ=America/Los_Angeles ") {
				t.Errorf("job %d not migrated: %v", i, a[i]["schedule"])
			}
		} else if a[i]["schedule"] != b[i]["schedule"] {
			t.Errorf("job %d schedule changed", i)
		}
		delete(a[i], "schedule")
		delete(b[i], "schedule")
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Errorf("non-schedule fields changed")
	}

	// The scheduler loads the migrated file.
	s := New(dir, nil)
	if err := s.loadJobs(); err != nil {
		t.Fatalf("scheduler cannot load migrated file: %v", err)
	}
	for _, j := range s.jobs {
		if _, err := normalizeSchedule(j.Schedule, j.Type); err != nil && j.ID != "e5" {
			t.Errorf("%s: %v", j.ID, err)
		}
	}

	// Idempotent: second run changes nothing and writes no backup.
	opts.Now = refPDT.Add(time.Hour)
	res2, err := ApplyTZMigration(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed != 0 || res2.BackupPath != "" {
		t.Fatalf("second run changed %d (backup %q)", res2.Changed, res2.BackupPath)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(after) {
		t.Fatal("second run modified the file")
	}
}

// The running scheduler's hot reload picks up migrated CRON_TZ schedules
// and computes next runs in the job's zone, independent of s.location.
func TestReloadPicksUpCronTZ(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	dir := t.TempDir()
	path := filepath.Join(dir, "cron_jobs.json")
	if err := os.WriteFile(path, []byte(sampleJobs), 0644); err != nil {
		t.Fatal(err)
	}
	s := New(dir, nil, WithLocation(time.UTC))
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if _, err := ApplyTZMigration(path, TZMigrateOptions{From: time.UTC, To: la, Now: refPDT}); err != nil {
		t.Fatal(err)
	}
	// External write well after any self-write: the watcher path.
	s.mu.Lock()
	s.lastWriteTime = time.Time{}
	s.mu.Unlock()
	s.checkAndReload()

	j, err := s.GetJob("a1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(j.Schedule, "CRON_TZ=America/Los_Angeles ") {
		t.Fatalf("reload did not pick up migrated schedule: %q", j.Schedule)
	}
	if j.NextRun == nil {
		t.Fatal("no next run")
	}
	if lt := j.NextRun.In(la); lt.Hour() != 6 || lt.Minute() != 5 {
		t.Errorf("next run %v, want 06:05 Pacific", lt)
	}
}
