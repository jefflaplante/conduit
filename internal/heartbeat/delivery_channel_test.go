package heartbeat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/scheduler"
)

// conduit-31jg.59: heartbeat delivery goes through DeliveryRegistry
// (ChannelSenderDeliverer) for circuit breaker, audit and retries.

// flakySender fails the first failN sends (or all of them when failN < 0).
type flakySender struct {
	mu    sync.Mutex
	calls int
	failN int
	msgs  []string
}

func (f *flakySender) SendMessage(ctx context.Context, channelID, userID, content string, md map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failN < 0 || f.calls <= f.failN {
		return errors.New("channel down")
	}
	f.msgs = append(f.msgs, channelID+":"+userID+"|"+content)
	return nil
}

func (f *flakySender) snapshot() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]string(nil), f.msgs...)
}

// newAuditedIntegration wires an integration to an audited registry, the
// way Gateway.New does.
func newAuditedIntegration(t *testing.T, sender ChannelSender, policy config.AlertRetryPolicy) (*GatewayIntegration, *DeliveryRegistry, *AlertAuditor) {
	t.Helper()
	old := minRetryInterval
	minRetryInterval = time.Millisecond
	t.Cleanup(func() { minRetryInterval = old })

	db := setupAuditDB(t)
	auditor := NewAlertAuditor(db)
	reg := NewDeliveryRegistry()
	reg.SetAuditor(auditor)

	g := NewGatewayIntegration(t.TempDir(), nil, nil, sender, nil, "", 0)
	cfg := liveLikeCfg()
	cfg.AlertRetryPolicy = policy
	g.SetAgentHeartbeatConfig(cfg)
	g.SetDeliveryRegistry(reg)
	t.Cleanup(func() { _ = g.Close() })
	return g, reg, auditor
}

func fastPolicy(max int) config.AlertRetryPolicy {
	return config.AlertRetryPolicy{MaxRetries: max, RetryInterval: config.Duration(time.Millisecond), BackoffFactor: 2}
}

func TestDelivery_SuccessWritesAlertHistory(t *testing.T) {
	sender := &flakySender{}
	g, _, auditor := newAuditedIntegration(t, sender, fastPolicy(3))
	job := &scheduler.Job{ID: "agent_heartbeat_main", Target: "telegram:42"}

	action := HeartbeatAction{Type: ActionTypeAlert, Content: "disk 95%", Priority: TaskPriorityCritical}
	if err := g.executeAction(context.Background(), action, job, deliverWithRetry); err != nil {
		t.Fatalf("executeAction: %v", err)
	}

	calls, msgs := sender.snapshot()
	if calls != 1 || len(msgs) != 1 || msgs[0] != "telegram:42|🚨 CRITICAL ALERT: disk 95%" {
		t.Fatalf("unexpected sends: calls=%d msgs=%v", calls, msgs)
	}

	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 alert_history row, got %d", len(rows))
	}
	r := rows[0]
	if r.ActionResult != "success" || r.ActionTaken != "delivered:telegram:42(channel)" {
		t.Errorf("row action = %q / %q", r.ActionTaken, r.ActionResult)
	}
	if r.AlertType != "heartbeat_alert" || r.Severity != "critical" || r.Source != "agent_heartbeat_main" {
		t.Errorf("row classification = type %q sev %q src %q", r.AlertType, r.Severity, r.Source)
	}
}

func TestDelivery_FailingSenderRetriesThenTripsBreaker(t *testing.T) {
	sender := &flakySender{failN: -1}
	g, reg, auditor := newAuditedIntegration(t, sender, fastPolicy(5))

	if err := g.sendToTarget(context.Background(), "telegram:42", "heartbeat error"); err == nil {
		t.Fatal("expected first attempt to fail")
	}
	g.retries.wg.Wait()

	// Breaker threshold is 3: first attempt + 2 retries fail, the third
	// retry is short-circuited by the open breaker and retries stop there,
	// before the policy's 5 retries are used up.
	calls, _ := sender.snapshot()
	if calls != 3 {
		t.Fatalf("sender calls = %d, want 3", calls)
	}
	if open, failures, _ := reg.CircuitBreakerState("telegram:42"); !open || failures != 3 {
		t.Fatalf("breaker open=%v failures=%d, want open with 3", open, failures)
	}

	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 audit rows (3 failures + 1 breaker skip), got %d", len(rows))
	}
	var breakerRows int
	for _, r := range rows {
		if strings.HasPrefix(r.ActionTaken, "circuit_breaker_open:") {
			breakerRows++
		} else if !strings.HasPrefix(r.ActionResult, "error:") {
			t.Errorf("failed attempt audited as %q", r.ActionResult)
		}
	}
	if breakerRows != 1 {
		t.Errorf("breaker rows = %d, want 1", breakerRows)
	}

	// While open, new sends are skipped without touching the sender and
	// without scheduling more retries.
	if err := g.sendToTarget(context.Background(), "telegram:42", "again"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("send with open breaker: err=%v, want ErrCircuitOpen", err)
	}
	g.retries.wg.Wait()
	if calls, _ := sender.snapshot(); calls != 3 {
		t.Fatalf("sender called with open breaker: calls=%d", calls)
	}
}

func TestDelivery_RetryPolicyBoundsAttempts(t *testing.T) {
	// One retry allowed: exactly two attempts, breaker stays closed.
	sender := &flakySender{failN: -1}
	g, reg, _ := newAuditedIntegration(t, sender, fastPolicy(1))
	_ = g.sendToTarget(context.Background(), "telegram:42", "x")
	g.retries.wg.Wait()
	if calls, _ := sender.snapshot(); calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if open, _, _ := reg.CircuitBreakerState("telegram:42"); open {
		t.Fatal("breaker opened after 2 failures (threshold 3)")
	}

	// A transient failure recovers on retry and resets the breaker.
	sender2 := &flakySender{failN: 1}
	g2, reg2, _ := newAuditedIntegration(t, sender2, fastPolicy(3))
	_ = g2.sendToTarget(context.Background(), "telegram:7", "hello")
	g2.retries.wg.Wait()
	calls, msgs := sender2.snapshot()
	if calls != 2 || len(msgs) != 1 {
		t.Fatalf("calls=%d msgs=%v, want delivery on first retry", calls, msgs)
	}
	if _, failures, _ := reg2.CircuitBreakerState("telegram:7"); failures != 0 {
		t.Fatalf("failures=%d after success, want 0", failures)
	}
}

func TestDelivery_CloseStopsPendingRetries(t *testing.T) {
	sender := &flakySender{failN: -1}
	g, _, _ := newAuditedIntegration(t, sender, config.AlertRetryPolicy{MaxRetries: 3, RetryInterval: config.Duration(time.Hour), BackoffFactor: 2})
	_ = g.sendToTarget(context.Background(), "telegram:42", "x")

	done := make(chan struct{})
	go func() { _ = g.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop a retry waiting on a 1h backoff")
	}
	if calls, _ := sender.snapshot(); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	// After Close no new retries are scheduled.
	_ = g.sendToTarget(context.Background(), "telegram:42", "y")
	g.retries.wg.Wait()
}

func TestDeferredFlush_GoesThroughRegistry(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &flakySender{failN: 1}
	g, _, auditor := newAuditedIntegration(t, sender, fastPolicy(3))
	job := &scheduler.Job{ID: "hb", Target: "telegram:42"}

	g.now = func() time.Time { return time.Date(2026, 9, 25, 23, 0, 0, 0, la) }
	if err := g.executeActions(context.Background(), []HeartbeatAction{quietAwareAction("digest")}, job); err != nil {
		t.Fatal(err)
	}
	if calls, _ := sender.snapshot(); calls != 0 {
		t.Fatalf("delivered during quiet hours")
	}

	// First flush fails: audited, stays queued, and no background retry
	// (the durable queue retries on the next cycle instead).
	g.now = func() time.Time { return time.Date(2026, 9, 26, 7, 0, 0, 0, la) }
	if n, _ := g.FlushDeferred(context.Background()); n != 0 {
		t.Fatalf("n=%d on failing sender", n)
	}
	g.retries.wg.Wait()
	if calls, _ := sender.snapshot(); calls != 1 {
		t.Fatalf("flush failure spawned background retries: calls=%d", calls)
	}

	if n, err := g.FlushDeferred(context.Background()); err != nil || n != 1 {
		t.Fatalf("second flush: n=%d err=%v", n, err)
	}
	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 audit rows (fail + success), got %d", len(rows))
	}
	for _, r := range rows {
		if r.AlertType != "heartbeat_notification" || r.ActionTaken != "delivered:telegram:42(channel)" {
			t.Errorf("unexpected row: type %q action %q", r.AlertType, r.ActionTaken)
		}
	}
}

func TestDelivery_OutputSanitized(t *testing.T) {
	sender := &flakySender{}
	g, reg, _ := newAuditedIntegration(t, sender, fastPolicy(0))

	raw := "[[reply_to_current]] Disk warning\nMEDIA: /tmp/x.ogg\n\n\n\nDetails"
	if err := g.sendToTarget(context.Background(), "telegram:42", raw); err != nil {
		t.Fatal(err)
	}
	// Registry path used directly (e.g. by a future caller) is sanitized too.
	alert := Alert{ID: "a", Type: "t", Message: "[[reply_to: 12]] hi\nMEDIA: /tmp/y.png"}
	if err := reg.DeliverAlert(context.Background(), alert, channelTarget("telegram:42")); err != nil {
		t.Fatal(err)
	}
	// Silent tokens are still suppressed entirely.
	if err := g.sendToTarget(context.Background(), "telegram:42", "HEARTBEAT_OK"); err != nil {
		t.Fatal(err)
	}

	_, msgs := sender.snapshot()
	want := []string{"telegram:42|Disk warning\n\nDetails", "telegram:42|hi"}
	if len(msgs) != len(want) {
		t.Fatalf("msgs = %q, want %q", msgs, want)
	}
	for i := range want {
		if msgs[i] != want[i] {
			t.Errorf("msg %d = %q, want %q", i, msgs[i], want[i])
		}
		if strings.Contains(msgs[i], "reply_to") || strings.Contains(msgs[i], "MEDIA:") {
			t.Errorf("unsanitized output: %q", msgs[i])
		}
	}
}

func TestChannelTarget_Parse(t *testing.T) {
	for in, want := range map[string][2]string{
		"telegram:42": {"telegram", "42"},
		"42":          {"telegram", "42"},
		"tui:alice":   {"tui", "alice"},
	} {
		got := channelTarget(in)
		if got.Config[targetConfigChannelID] != want[0] || got.Config[targetConfigUserID] != want[1] || got.Type != ChannelDelivererType {
			t.Errorf("channelTarget(%q) = %+v", in, got)
		}
	}
}
