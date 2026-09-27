package scheduler

import (
	"strings"
	"testing"
	"time"
)

// conduit-31jg.74: migrate-tz for system (crontab) jobs.

func TestPlanTZMigration_IncludeSystem(t *testing.T) {
	withDaemonZone(t, time.UTC)
	la, _ := time.LoadLocation("America/Los_Angeles")
	data := []byte(`[
 {"id":"cal","type":"system","schedule":"10 15 * * 1-5","command":"cal","enabled":true},
 {"id":"mon8pm","type":"system","schedule":"0 3 * * 2","command":"x","enabled":true},
 {"id":"flush","type":"system","schedule":"5 */2 * * *","command":"f","enabled":true},
 {"id":"tick","type":"system","schedule":"*/5 * * * *","command":"t","enabled":true}
]`)
	opts := TZMigrateOptions{From: time.UTC, To: la, Now: tz74Ref}

	res, err := PlanTZMigration(data, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Action != TZSkip {
			t.Fatalf("without IncludeSystem %s: %s", r.ID, r.Action)
		}
	}

	opts.IncludeSystem = true
	res, err = PlanTZMigration(data, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TZAction{"cal": TZConvert, "mon8pm": TZRefuse, "flush": TZSkip, "tick": TZSkip}
	for _, r := range res {
		if r.Action != want[r.ID] {
			t.Errorf("%s: action %s (%s), want %s", r.ID, r.Action, r.Reason, want[r.ID])
		}
		if r.Action == TZRefuse && r.NewSchedule != r.OldSchedule {
			t.Errorf("%s: refused job schedule changed to %q", r.ID, r.NewSchedule)
		}
	}
	if res[0].NewSchedule != "CRON_TZ=America/Los_Angeles 10 8 * * 1-5" || res[0].Command != "cal" {
		t.Fatalf("cal: %+v", res[0])
	}
}

func TestPlanCrontabTZUpdate(t *testing.T) {
	jobs := []TZJobResult{
		{ID: "ab", Type: JobTypeSystem, Enabled: true, Action: TZConvert, OldSchedule: "0 13 * * *", NewSchedule: "CRON_TZ=America/Los_Angeles 0 6 * * *", Command: "ledger"},
		{ID: "gone", Type: JobTypeSystem, Enabled: true, Action: TZConvert, OldSchedule: "0 6 * * *", NewSchedule: "CRON_TZ=America/Los_Angeles 0 23 * * *", Command: "solar"},
		{ID: "off", Type: JobTypeSystem, Enabled: false, Action: TZConvert, OldSchedule: "0 6 * * *", NewSchedule: "CRON_TZ=America/Los_Angeles 0 23 * * *", Command: "x"},
		{ID: "go1", Type: JobTypeGo, Action: TZConvert},
	}
	cur := []string{
		"# header",
		"0 13 * * * ledger # CONDUIT-MANAGED CONDUIT-JOB-ID:ab",
		"0 13 * * * other # CONDUIT-MANAGED CONDUIT-JOB-ID:abc", // longer ID sharing the prefix
		"0 13 * * * ledger # CONDUIT-MANAGED CONDUIT-JOB-ID:ab", // duplicate
	}
	out, changes, err := PlanCrontabTZUpdate(cur, jobs, time.UTC, tz74Ref, false)
	if err != nil {
		t.Fatal(err)
	}
	wantOut := []string{
		"# header",
		`0 13,14 * * * case "$(TZ=America/Los_Angeles date +\%H)" in 06) ;; *) exit 0 ;; esac; ledger # CONDUIT-MANAGED CONDUIT-JOB-ID:ab`,
		"0 13 * * * other # CONDUIT-MANAGED CONDUIT-JOB-ID:abc",
	}
	if strings.Join(out, "\n") != strings.Join(wantOut, "\n") {
		t.Fatalf("crontab:\n%s\nwant:\n%s", strings.Join(out, "\n"), strings.Join(wantOut, "\n"))
	}
	acts := map[string]string{}
	for _, c := range changes {
		acts[c.ID] = c.Action
	}
	if acts["ab"] != CrontabRewrite || acts["gone"] != CrontabNotInstalled || acts["off"] != CrontabDisabled || len(changes) != 3 {
		t.Fatalf("changes: %+v", changes)
	}
	if !strings.Contains(changes[0].Note, "duplicate") {
		t.Errorf("duplicate not reported: %q", changes[0].Note)
	}

	out, changes, _ = PlanCrontabTZUpdate(cur, jobs, time.UTC, tz74Ref, true)
	if changes[1].Action != CrontabInstall || !strings.HasSuffix(out[len(out)-1], "CONDUIT-JOB-ID:gone") {
		t.Fatalf("install-missing: %+v\n%s", changes[1], strings.Join(out, "\n"))
	}
}
