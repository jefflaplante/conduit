package heartbeat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/scheduler"
)

// conduit-31jg.33: quiet-aware heartbeat actions are deferred to a durable
// queue and delivered once quiet hours end, even across a restart.

type recordingSender struct {
	mu   sync.Mutex
	msgs []string
	err  error
}

func (r *recordingSender) SendMessage(ctx context.Context, channelID, userID, content string, md map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, channelID+":"+userID+"|"+content)
	return nil
}

func (r *recordingSender) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

func liveLikeCfg() config.AgentHeartbeatConfig {
	cfg := config.DefaultAgentHeartbeatConfig()
	cfg.Timezone = "America/Los_Angeles"
	cfg.QuietEnabled = true
	cfg.QuietHours = config.QuietHoursConfig{StartTime: "20:00", EndTime: "06:00"}
	return cfg
}

func newTestIntegration(t *testing.T, dir string, sender ChannelSender, now time.Time) *GatewayIntegration {
	t.Helper()
	g := NewGatewayIntegration(dir, nil, nil, sender, nil, "", 0)
	g.SetAgentHeartbeatConfig(liveLikeCfg())
	g.now = func() time.Time { return now }
	return g
}

func quietAwareAction(content string) HeartbeatAction {
	return HeartbeatAction{
		Type:     ActionTypeNotification,
		Content:  content,
		Priority: TaskPriorityNormal,
		Metadata: map[string]interface{}{"quiet_aware": true},
	}
}

func TestDeferredAction_DeliveredAfterQuietEnds_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	la, _ := time.LoadLocation("America/Los_Angeles")
	job := &scheduler.Job{ID: "agent_heartbeat_main", Target: "telegram:42"}

	// 21:00 PDT: inside the configured 20:00-06:00 window.
	sender := &recordingSender{}
	g := newTestIntegration(t, dir, sender, time.Date(2026, 9, 25, 21, 0, 0, 0, la))
	if err := g.executeActions(context.Background(), []HeartbeatAction{quietAwareAction("weekly digest ready")}, job); err != nil {
		t.Fatalf("executeActions: %v", err)
	}
	if got := sender.sent(); len(got) != 0 {
		t.Fatalf("quiet-aware action delivered during quiet hours: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "memory", "alerts", "deferred.json")); err != nil {
		t.Fatalf("deferred queue not persisted: %v", err)
	}

	// Simulated restart: a fresh integration over the same workspace.
	sender2 := &recordingSender{}
	g2 := newTestIntegration(t, dir, sender2, time.Date(2026, 9, 26, 5, 59, 0, 0, la))
	if n, err := g2.FlushDeferred(context.Background()); err != nil || n != 0 {
		t.Fatalf("flush during quiet: n=%d err=%v", n, err)
	}
	if got := sender2.sent(); len(got) != 0 {
		t.Fatalf("delivered before quiet end: %v", got)
	}

	g2.now = func() time.Time { return time.Date(2026, 9, 26, 6, 0, 0, 0, la) }
	n, err := g2.FlushDeferred(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("flush after quiet: n=%d err=%v", n, err)
	}
	got := sender2.sent()
	if len(got) != 1 || got[0] != "telegram:42|💡 weekly digest ready" {
		t.Fatalf("unexpected delivery: %v", got)
	}

	// Idempotent: a second flush delivers nothing.
	if n, _ := g2.FlushDeferred(context.Background()); n != 0 {
		t.Fatalf("second flush redelivered %d", n)
	}
	if len(sender2.sent()) != 1 {
		t.Fatalf("duplicate delivery: %v", sender2.sent())
	}
}

func TestDeferredAction_OutsideQuietDeliversImmediately(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &recordingSender{}
	// 16:00 PDT is 23:00 UTC: the old hardcoded hour>=22 server-local (UTC) check called this quiet.
	g := newTestIntegration(t, t.TempDir(), sender, time.Date(2026, 9, 26, 16, 0, 0, 0, la))
	job := &scheduler.Job{ID: "hb", Target: "telegram:42"}
	if err := g.executeActions(context.Background(), []HeartbeatAction{quietAwareAction("hello")}, job); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("expected immediate delivery, got %v", sender.sent())
	}
}

func TestDeferredAction_FailedDeliveryStaysQueued(t *testing.T) {
	dir := t.TempDir()
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &recordingSender{}
	g := newTestIntegration(t, dir, sender, time.Date(2026, 9, 25, 23, 0, 0, 0, la))
	job := &scheduler.Job{ID: "hb", Target: "telegram:42"}
	if err := g.executeActions(context.Background(), []HeartbeatAction{quietAwareAction("retry me")}, job); err != nil {
		t.Fatal(err)
	}

	g.now = func() time.Time { return time.Date(2026, 9, 26, 7, 0, 0, 0, la) }
	sender.err = errors.New("telegram down")
	if n, _ := g.FlushDeferred(context.Background()); n != 0 {
		t.Fatalf("n=%d on failing sender", n)
	}
	sender.err = nil
	if n, err := g.FlushDeferred(context.Background()); err != nil || n != 1 {
		t.Fatalf("retry flush: n=%d err=%v", n, err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("got %v", sender.sent())
	}
}
