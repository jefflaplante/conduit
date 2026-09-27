package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conduit-31jg.60: CLI smoke test for `cron migrate-tz`.
func TestCronMigrateTZCmd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cron_jobs.json")
	orig := `[
  {
    "id": "a1",
    "name": "Morning Briefing",
    "schedule": "0 5 13 * * 1,2,4,5",
    "type": "go",
    "command": "brief",
    "enabled": true,
    "created_at": "2026-08-31T14:57:12Z",
    "run_count": 0
  }
]
`
	if err := os.WriteFile(path, []byte(orig), 0644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := CronRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"migrate-tz", "--from", "UTC", "--to", "America/Los_Angeles", "--file", path, "--now", "2026-09-27T01:00:00Z"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`CRON_TZ=America/Los_Angeles 0 5 6 * * 1,2,4,5`") || !strings.Contains(out, "Mon 2026-11-02 06:05 PST") {
		t.Fatalf("dry-run output missing expected rows:\n%s", out)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Fatal("dry-run modified the file")
	}

	if _, err := run("--dry-run", "--apply"); err == nil {
		t.Fatal("expected error for --dry-run with --apply")
	}

	out, err = run("--apply")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Migrated 1 job(s)") {
		t.Fatalf("apply output: %s", out)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"schedule": "CRON_TZ=America/Los_Angeles 0 5 6 * * 1,2,4,5"`) {
		t.Fatalf("file not migrated:\n%s", b)
	}
	out, err = run("--apply")
	if err != nil || !strings.Contains(out, "Nothing to migrate") {
		t.Fatalf("second apply not idempotent: %v\n%s", err, out)
	}
}
