package heartbeat

import (
	"path/filepath"
	"testing"
	"time"
)

// conduit-31jg.33: latent bugs in the (currently unwired) alert stack.
//  1. RetryCount++ was applied to a local copy, never persisted.
//  2. ProcessPendingAlerts only looked at "pending", so failed alerts were
//     never retried; and a not-yet-eligible retry was marked suppressed.

func newRetryAlert(id string, maxRetries int) Alert {
	return Alert{
		ID:         id,
		Source:     "test",
		Title:      "retry",
		Message:    "retry me",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: maxRetries,
	}
}

func loadAlert(t *testing.T, p *AlertProcessorImpl, id string) (Alert, bool) {
	t.Helper()
	q, err := p.queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	for _, a := range q.Alerts {
		if a.ID == id {
			return a, true
		}
	}
	return Alert{}, false
}

func TestProcessAlert_FailurePersistsRetryCount(t *testing.T) {
	cfg := createTestConfig()
	md := &mockDeliveryFunc{shouldErr: true, errMsg: "boom"}
	p := NewAlertProcessor(filepath.Join(t.TempDir(), "q.json"), cfg, md.deliver)
	a := newRetryAlert("rc", 3)
	if err := p.AddAlert(a); err != nil {
		t.Fatal(err)
	}
	_ = p.ProcessAlert(a)

	got, ok := loadAlert(t, p, "rc")
	if !ok {
		t.Fatal("alert missing")
	}
	if got.Status != AlertStatusFailed || got.RetryCount != 1 || got.LastError == "" || got.LastAttemptAt == nil {
		t.Fatalf("failure not persisted: status=%s retry=%d err=%q last=%v", got.Status, got.RetryCount, got.LastError, got.LastAttemptAt)
	}
}

func TestProcessPendingAlerts_RetriesFailedAlerts(t *testing.T) {
	cfg := createTestConfig()
	cfg.AlertRetryPolicy.RetryInterval = 0
	md := &mockDeliveryFunc{shouldErr: true, errMsg: "down"}
	p := NewAlertProcessor(filepath.Join(t.TempDir(), "q.json"), cfg, md.deliver)
	if err := p.AddAlert(newRetryAlert("r1", 3)); err != nil {
		t.Fatal(err)
	}

	_ = p.ProcessPendingAlerts()
	firstCalls := len(md.calls)
	if firstCalls == 0 {
		t.Fatal("no delivery attempted")
	}

	md.shouldErr = false
	if err := p.ProcessPendingAlerts(); err != nil {
		t.Fatalf("retry pass: %v", err)
	}
	if len(md.calls) <= firstCalls {
		t.Fatal("failed alert was not retried")
	}
	if a, ok := loadAlert(t, p, "r1"); ok && a.Status != AlertStatusSent {
		t.Fatalf("status after retry = %s", a.Status)
	}
}

func TestProcessPendingAlerts_RetryDelayNotElapsedKeepsFailed(t *testing.T) {
	cfg := createTestConfig() // 5m retry interval
	md := &mockDeliveryFunc{shouldErr: true, errMsg: "down"}
	p := NewAlertProcessor(filepath.Join(t.TempDir(), "q.json"), cfg, md.deliver)
	if err := p.AddAlert(newRetryAlert("r2", 3)); err != nil {
		t.Fatal(err)
	}
	_ = p.ProcessPendingAlerts()
	calls := len(md.calls)

	md.shouldErr = false
	_ = p.ProcessPendingAlerts()
	if len(md.calls) != calls {
		t.Fatal("retried before retry delay elapsed")
	}
	a, _ := loadAlert(t, p, "r2")
	if a.Status != AlertStatusFailed {
		t.Fatalf("not-yet-eligible retry must stay failed, got %s", a.Status)
	}
}

func TestProcessPendingAlerts_StopsAfterMaxRetries(t *testing.T) {
	cfg := createTestConfig()
	cfg.AlertRetryPolicy.RetryInterval = 0
	cfg.AlertRetryPolicy.MaxRetries = 2
	md := &mockDeliveryFunc{shouldErr: true, errMsg: "down"}
	p := NewAlertProcessor(filepath.Join(t.TempDir(), "q.json"), cfg, md.deliver)
	if err := p.AddAlert(newRetryAlert("r3", 2)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = p.ProcessPendingAlerts()
	}
	// 2 targets handle critical in createTestConfig; 2 attempts total.
	if len(md.calls) != 2*2 {
		t.Fatalf("expected 4 delivery calls (2 attempts x 2 targets), got %d", len(md.calls))
	}
	a, _ := loadAlert(t, p, "r3")
	if a.RetryCount != 2 || a.Status != AlertStatusFailed {
		t.Fatalf("retry=%d status=%s", a.RetryCount, a.Status)
	}
}

// Quiet-hours delay must not permanently suppress a warning.
func TestProcessAlert_QuietHoursDelayLeavesPending(t *testing.T) {
	cfg := createTestConfig()
	cfg.QuietHours.StartTime = "00:00"
	cfg.QuietHours.EndTime = "23:59" // quiet almost all day
	md := &mockDeliveryFunc{}
	p := NewAlertProcessor(filepath.Join(t.TempDir(), "q.json"), cfg, md.deliver)
	a := newRetryAlert("w1", 3)
	a.Severity = AlertSeverityWarning
	if err := p.AddAlert(a); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsQuietTime(time.Now()) {
		t.Skip("current minute is 23:59 in LA; window check not applicable")
	}
	_ = p.ProcessAlert(a)
	got, _ := loadAlert(t, p, "w1")
	if got.Status != AlertStatusPending {
		t.Fatalf("delayed warning should stay pending, got %s", got.Status)
	}
}
