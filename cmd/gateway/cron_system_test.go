package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conduit-31jg.74: `cron migrate-tz --include-system` converts system jobs
// and rewrites their crontab lines. Crontab I/O is faked: tests must never
// touch the real user crontab.
func TestCronMigrateTZCmd_IncludeSystem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cron_jobs.json")
	orig := `[
  {"id": "aaaa0001", "name": "Job A", "schedule": "10 15 * * 1-5", "type": "system", "command": "python3 cal.py >> log 2>&1", "enabled": true, "created_at": "2026-08-31T14:57:12Z", "run_count": 0},
  {"id": "aaaa0002", "name": "Job B", "schedule": "0 3,15,19 * * *", "type": "system", "command": "clock.sh", "enabled": true, "created_at": "2026-08-31T14:57:12Z", "run_count": 0},
  {"id": "aaaa0003", "name": "Job C", "schedule": "5 */2 * * *", "type": "system", "command": "alert-flush.sh", "enabled": true, "created_at": "2026-08-31T14:57:12Z", "run_count": 0},
  {"id": "aaaa0004", "name": "Job D", "schedule": "*/10 * * * *", "type": "system", "command": "fast.sh", "enabled": true, "created_at": "2026-08-31T14:57:12Z", "run_count": 0}
]
`
	if err := os.WriteFile(path, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	crontab := strings.Join([]string{
		"# m h  dom mon dow   command",
		"0 3,15,19 * * * clock.sh > /dev/null 2>&1",
		"",
		"10 15 * * 1-5 python3 cal.py >> log 2>&1 # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0001",
		"5 */2 * * * alert-flush.sh # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0003",
		"*/10 * * * * fast.sh # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0004",
	}, "\n") + "\n"
	crontabPath := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(crontabPath, []byte(crontab), 0600); err != nil {
		t.Fatal(err)
	}

	var installed []string
	installs := 0
	prevRead, prevInstall := readCrontabCmd, installCrontab
	readCrontabCmd = func() ([]byte, error) { return []byte(crontab), nil }
	installCrontab = func(lines []string) error { installs++; installed = lines; return nil }
	t.Cleanup(func() { readCrontabCmd, installCrontab = prevRead, prevInstall })

	run := func(args ...string) (string, error) {
		cmd := CronRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"migrate-tz", "--from", "UTC", "--to", "America/Los_Angeles", "--file", path, "--now", "2026-09-27T02:30:00Z"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	// Without --include-system, system jobs are skipped as before.
	out, err := run("--dry-run")
	if err != nil || !strings.Contains(out, "0 convert") {
		t.Fatalf("default run converted system jobs: %v\n%s", err, out)
	}

	out, err = run("--dry-run", "--include-system", "--crontab-file", crontabPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`CRON_TZ=America/Los_Angeles 10 8 * * 1-5`",
		"Mon 2026-11-02 08:10 PST", // simulated crontab behaviour after DST ends
		`+ 10 15,16 * * 1-5 case "$(TZ=America/Los_Angeles date +\%H)" in 08) ;; *) exit 0 ;; esac; python3 cal.py >> log 2>&1 # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0001`,
		"aaaa0002 Job B [not-installed]",
		"'*/N' interval",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != orig || installs != 0 {
		t.Fatal("dry-run wrote something")
	}

	// --apply with system conversions requires --update-crontab and writes nothing without it.
	if _, err := run("--apply", "--include-system"); err == nil || !strings.Contains(err.Error(), "--update-crontab") {
		t.Fatalf("expected --update-crontab error, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Fatal("refused apply modified the file")
	}

	out, err = run("--apply", "--include-system", "--update-crontab")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if installs != 1 {
		t.Fatalf("crontab installed %d times", installs)
	}
	got := strings.Join(installed, "\n")
	if !strings.Contains(got, `10 15,16 * * 1-5 case "$(TZ=America/Los_Angeles date +\%H)" in 08)`) {
		t.Fatalf("new crontab missing guarded line:\n%s", got)
	}
	// Unrelated lines (hand-written, blank, interval jobs) are kept verbatim and in order.
	if installed[0] != "# m h  dom mon dow   command" || installed[1] != "0 3,15,19 * * * clock.sh > /dev/null 2>&1" || installed[2] != "" ||
		installed[4] != "5 */2 * * * alert-flush.sh # CONDUIT-MANAGED CONDUIT-JOB-ID:aaaa0003" || len(installed) != 6 {
		t.Fatalf("unrelated crontab lines changed:\n%s", got)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"schedule": "CRON_TZ=America/Los_Angeles 10 8 * * 1-5"`) {
		t.Fatalf("jobs file not migrated:\n%s", b)
	}
	baks, _ := filepath.Glob(path + ".crontab.bak-*")
	if len(baks) != 1 {
		t.Fatalf("crontab backup files: %v", baks)
	}
	if bb, _ := os.ReadFile(baks[0]); string(bb) != crontab {
		t.Fatalf("crontab backup content differs:\n%s", bb)
	}
}
