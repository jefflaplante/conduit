package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// conduit-31jg.74: CRON_TZ system jobs rendered for vixie cron (no CRON_TZ).

func withDaemonZone(t *testing.T, loc *time.Location) {
	t.Helper()
	prev := crontabDaemonLocation
	crontabDaemonLocation = func() *time.Location { return loc }
	t.Cleanup(func() { crontabDaemonLocation = prev })
}

var tz74Ref = time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)

func TestRenderCrontabSchedule(t *testing.T) {
	cases := []struct {
		in, fields string
		guard      []int
		wantErr    bool
	}{
		{in: "0 13 * * *", fields: "0 13 * * *"}, // no prefix: unchanged
		{in: "CRON_TZ=America/Los_Angeles 10 8 * * 1-5", fields: "10 15,16 * * 1-5", guard: []int{8}},
		{in: "CRON_TZ=America/Los_Angeles 0 8,12,20 * * *", fields: "0 3,4,15,16,19,20 * * *", guard: []int{8, 12, 20}},
		{in: "CRON_TZ=America/Los_Angeles 0 0 * * 1", fields: "0 7,8 * * 1", guard: []int{0}},
		{in: "CRON_TZ=America/Los_Angeles 0 13 5 * *", fields: "0 20,21 5 * *", guard: []int{13}},
		{in: "CRON_TZ=America/Los_Angeles 20 23 * * *", fields: "20 6,7 * * *", guard: []int{23}},
		{in: "CRON_TZ=America/Los_Angeles */5 * * * *", fields: "*/5 * * * *"}, // zone-independent
		{in: "CRON_TZ=UTC 0 8 * * *", fields: "0 8 * * *"},                     // same zone as daemon
		{in: "CRON_TZ=America/Los_Angeles 0 20 * * 1", wantErr: true},          // Tue in UTC
		{in: "CRON_TZ=Asia/Kolkata 0 8 * * *", wantErr: true},                  // half-hour offset
		{in: "CRON_TZ=Not/AZone 0 8 * * *", wantErr: true},
	}
	for _, c := range cases {
		r, err := renderCrontabSchedule(c.in, time.UTC, tz74Ref)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", c.in, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if r.Fields != c.fields || !reflect.DeepEqual(r.GuardHours, c.guard) {
			t.Errorf("%q: got fields %q guard %v; want %q %v", c.in, r.Fields, r.GuardHours, c.fields, c.guard)
		}
	}
}

func TestCrontabLine_GuardAndMarker(t *testing.T) {
	withDaemonZone(t, time.UTC)
	line, err := CrontabLine(&Job{ID: "aaaa0001", Schedule: "CRON_TZ=America/Los_Angeles 10 8 * * 1-5",
		Command: "python3 x.py >> log 2>&1"}, CrontabMarker)
	if err != nil {
		t.Fatal(err)
	}
	want := `10 15,16 * * 1-5 case "$(TZ=America/Los_Angeles date +\%H)" in 08) ;; *) exit 0 ;; esac; python3 x.py >> log 2>&1 # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0001`
	if line != want {
		t.Fatalf("line:\n got %s\nwant %s", line, want)
	}
	// Unzoned system jobs render exactly as before.
	line, _ = CrontabLine(&Job{ID: "a", Schedule: "*/5 * * * *", Command: "run"}, CrontabMarker)
	if line != "*/5 * * * * run # CONDUIT-MANAGED CONDUIT-JOB-ID:a" {
		t.Fatalf("unzoned line changed: %s", line)
	}
}

// The simulated crontab behaviour (daemon-zone fields + guard) must fire at
// exactly the instants robfig's CRON_TZ semantics define, across both DST
// transitions in the next year.
func TestCrontabRendering_MatchesCronTZAcrossDST(t *testing.T) {
	schedules := []string{
		"CRON_TZ=America/Los_Angeles 10 8 * * 1-5",
		"CRON_TZ=America/Los_Angeles 0 8,12,20 * * *",
		"CRON_TZ=America/Los_Angeles 0 6 * * *",
		"CRON_TZ=America/Los_Angeles 15 6 * * *",
		"CRON_TZ=America/Los_Angeles 0 20 * * *",
		"CRON_TZ=America/Los_Angeles 0 23 * * *",
		"CRON_TZ=America/Los_Angeles 20 23 * * *",
		"CRON_TZ=America/Los_Angeles 0 0 * * 1",
		"CRON_TZ=America/Los_Angeles 0 13 5 * *",
		"CRON_TZ=America/Los_Angeles 0 4 1 * *",
		"CRON_TZ=America/Los_Angeles 5 */2 * * *",
		"CRON_TZ=America/Los_Angeles 30 7-9 * * *",
	}
	for _, sched := range schedules {
		want, err := cron.ParseStandard(sched)
		if err != nil {
			t.Fatal(err)
		}
		// Window spans both DST changes (2026-11-01 and 2027-03-14).
		end := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
		var exp []time.Time
		for t0 := want.Next(tz74Ref); t0.Before(end); t0 = want.Next(t0) {
			exp = append(exp, t0)
		}
		got, err := NextCrontabFires(sched, time.UTC, tz74Ref, len(exp))
		if err != nil {
			t.Fatalf("%s: %v", sched, err)
		}
		if len(got) != len(exp) {
			t.Fatalf("%s: %d fires, want %d", sched, len(got), len(exp))
		}
		for i := range exp {
			if !got[i].Equal(exp[i]) {
				t.Fatalf("%s: fire %d = %s, want %s", sched, i, got[i].UTC(), exp[i].UTC())
			}
		}
	}
}
