package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/scheduler"
)

// conduit-2six: end-to-end wiring of scheduler job-failure notices through
// the heartbeat DeliveryRegistry into alert_history.

type captureSender struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureSender) SendMessage(_ context.Context, channelID, userID, content string, _ map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, channelID+":"+userID+"|"+content)
	return nil
}

func (c *captureSender) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

func TestJobHealthNotifier_EndToEndAlertHistory(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.monitoring.WireDeliveryRegistry(store.DB())

	hbCfg := config.DefaultAgentHeartbeatConfig()
	hbCfg.QuietEnabled = false
	hbCfg.AlertTargets = []config.AlertTarget{{Name: "owner", Type: "telegram", Config: map[string]string{"chat_id": "4242"}}}

	sender := &captureSender{}
	hb := heartbeat.NewGatewayIntegration(t.TempDir(), nil, nil, nil, sender, nil, "", 0)
	t.Cleanup(func() { _ = hb.Close() })
	hb.SetAgentHeartbeatConfig(hbCfg)
	hb.SetDeliveryRegistry(gw.monitoring.DeliveryRegistry)

	workspace := t.TempDir()
	jobs := []*scheduler.Job{{ID: "wildlife", Name: "Wildlife", Schedule: "0 0 0 1 1 *", Type: scheduler.JobTypeGo, Enabled: true}}
	data, _ := json.Marshal(jobs)
	if err := os.WriteFile(filepath.Join(workspace, "cron_jobs.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	var fail atomic.Bool
	fail.Store(true)
	exec := func(ctx context.Context, job *scheduler.Job) error {
		if fail.Load() {
			return errors.New("AI execution failed: provider 529")
		}
		return nil
	}
	notifier := newJobHealthNotifier(hbCfg)
	notifier.hb = hb
	s := scheduler.New(workspace, exec, schedulerHealthOptions(workspace, hbCfg, notifier)...)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	gw.scheduler = s

	runAndWait := func(wantStreak int) {
		t.Helper()
		waitFor(t, 5*time.Second, "job idle", func() bool { return len(s.RunningJobs()) == 0 })
		if err := s.RunNow("wildlife"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 5*time.Second, "run recorded", func() bool {
			return len(s.RunningJobs()) == 0 && s.JobHealth()["wildlife"].ConsecutiveFailures == wantStreak
		})
	}

	runAndWait(1)
	runAndWait(2)
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("alerted below threshold: %v", got)
	}
	runAndWait(3)
	waitFor(t, 5*time.Second, "failing alert", func() bool { return len(sender.sent()) == 1 })
	runAndWait(4)

	// Visibility: Cron tool list data and /status summary.
	if got := gw.failingJobsSummary(); got != "wildlife (4)" {
		t.Errorf("failingJobsSummary = %q", got)
	}
	listed := gw.ListJobs()
	if len(listed) != 1 || listed[0].ConsecutiveFailures != 4 || !strings.Contains(listed[0].LastError, "provider 529") {
		t.Errorf("ListJobs = %+v", listed[0])
	}

	fail.Store(false)
	runAndWait(0)
	waitFor(t, 5*time.Second, "recovered notice", func() bool { return len(sender.sent()) == 2 })
	time.Sleep(50 * time.Millisecond) // let any stray notice land before counting

	msgs := sender.sent()
	if len(msgs) != 2 {
		t.Fatalf("want exactly 2 messages (failing, recovered), got %d: %v", len(msgs), msgs)
	}
	if !strings.HasPrefix(msgs[0], "telegram:4242|") || !strings.Contains(msgs[0], "failed 3 run(s) in a row") {
		t.Errorf("failing msg = %q", msgs[0])
	}
	if !strings.Contains(msgs[1], "recovered") {
		t.Errorf("recovered msg = %q", msgs[1])
	}

	rows, err := heartbeat.NewAlertAuditor(store.DB()).ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, r := range rows {
		types[r.AlertType]++
		if r.Source != "scheduler:wildlife" || r.ActionResult != "success" {
			t.Errorf("row src %q result %q", r.Source, r.ActionResult)
		}
	}
	if len(rows) != 2 || types["cron_job_failing"] != 1 || types["cron_job_recovered"] != 1 {
		t.Fatalf("alert_history types = %v", types)
	}

	// The failure log got one error record per failed run.
	logData, err := os.ReadFile(filepath.Join(workspace, cronFailureLogPath))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(logData), `"status":"error"`); n != 4 {
		t.Errorf("failure log has %d error records, want 4:\n%s", n, logData)
	}
	if gw.failingJobsSummary() != "" {
		t.Errorf("summary after recovery = %q", gw.failingJobsSummary())
	}
}

func TestOwnerAlertTarget(t *testing.T) {
	cases := []struct {
		targets []config.AlertTarget
		want    string
	}{
		{nil, ""},
		{[]config.AlertTarget{{Type: "telegram", Config: map[string]string{"chat_id": "7"}}}, "telegram:7"},
		{[]config.AlertTarget{{Type: "telegram", Config: map[string]string{}}}, ""},
		{[]config.AlertTarget{{Type: "email", Config: map[string]string{"chat_id": "7"}}}, ""},
	}
	for i, c := range cases {
		if got := ownerAlertTarget(config.AgentHeartbeatConfig{AlertTargets: c.targets}); got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

func TestJobHealthNotifier_NoHeartbeat(t *testing.T) {
	n := newJobHealthNotifier(config.DefaultAgentHeartbeatConfig())
	if err := n.NotifyJobHealth(context.Background(), scheduler.JobHealthEvent{JobID: "x"}); err == nil {
		t.Fatal("want error when heartbeat delivery is not wired")
	}
}
