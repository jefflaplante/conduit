package heartbeat

import (
	"context"
	"testing"
	"time"
)

// conduit-2six: owner notices (scheduler job-health) go through the
// DeliveryRegistry and are deferred during quiet hours unless critical.

func TestNotifyOwner_DeliversAndAudits(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &flakySender{}
	g, _, auditor := newAuditedIntegration(t, sender, fastPolicy(3))
	g.now = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, la) } // not quiet

	deferred, err := g.NotifyOwner(context.Background(), OwnerNotice{
		Target: "telegram:42", Message: "job wildlife failing", Type: "cron_job_failing",
		Source: "scheduler:wildlife", Severity: AlertSeverityWarning,
	})
	if err != nil || deferred {
		t.Fatalf("NotifyOwner: deferred=%v err=%v", deferred, err)
	}
	if _, msgs := sender.snapshot(); len(msgs) != 1 || msgs[0] != "telegram:42|job wildlife failing" {
		t.Fatalf("sent = %v", msgs)
	}
	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 alert_history row, got %d", len(rows))
	}
	r := rows[0]
	if r.AlertType != "cron_job_failing" || r.Severity != "warning" || r.Source != "scheduler:wildlife" || r.ActionResult != "success" {
		t.Errorf("row = type %q sev %q src %q result %q", r.AlertType, r.Severity, r.Source, r.ActionResult)
	}
}

func TestNotifyOwner_DeferredDuringQuietHours(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &flakySender{}
	g, _, auditor := newAuditedIntegration(t, sender, fastPolicy(3))
	g.now = func() time.Time { return time.Date(2026, 9, 25, 23, 0, 0, 0, la) } // quiet (20:00-06:00)

	deferred, err := g.NotifyOwner(context.Background(), OwnerNotice{
		Target: "telegram:42", Message: "job wildlife failing", Type: "cron_job_failing",
		Source: "scheduler:wildlife", Severity: AlertSeverityWarning,
	})
	if err != nil || !deferred {
		t.Fatalf("NotifyOwner: deferred=%v err=%v", deferred, err)
	}
	if calls, _ := sender.snapshot(); calls != 0 {
		t.Fatalf("delivered during quiet hours")
	}
	pending, err := g.deferred.GetPendingAlerts()
	if err != nil || len(pending) != 1 {
		t.Fatalf("deferred queue: %d entries, err %v", len(pending), err)
	}

	// Still quiet: nothing flushed.
	if n, _ := g.FlushDeferred(context.Background()); n != 0 {
		t.Fatalf("flushed %d during quiet hours", n)
	}

	// After quiet hours the regular deferred flush delivers it, audited.
	g.now = func() time.Time { return time.Date(2026, 9, 26, 7, 0, 0, 0, la) }
	if n, err := g.FlushDeferred(context.Background()); err != nil || n != 1 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	if _, msgs := sender.snapshot(); len(msgs) != 1 || msgs[0] != "telegram:42|job wildlife failing" {
		t.Fatalf("sent = %v", msgs)
	}
	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "scheduler:wildlife" || rows[0].Severity != "warning" {
		t.Fatalf("audit rows = %+v", rows)
	}
}

func TestNotifyOwner_CriticalBypassesQuietHours(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	sender := &flakySender{}
	g, _, _ := newAuditedIntegration(t, sender, fastPolicy(0))
	g.now = func() time.Time { return time.Date(2026, 9, 25, 23, 0, 0, 0, la) }

	deferred, err := g.NotifyOwner(context.Background(), OwnerNotice{Target: "telegram:42", Message: "urgent", Severity: AlertSeverityCritical})
	if err != nil || deferred {
		t.Fatalf("deferred=%v err=%v", deferred, err)
	}
	if calls, _ := sender.snapshot(); calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestNotifyOwner_NoTarget(t *testing.T) {
	g, _, _ := newAuditedIntegration(t, &flakySender{}, fastPolicy(0))
	if _, err := g.NotifyOwner(context.Background(), OwnerNotice{Message: "x"}); err == nil {
		t.Fatal("want error without a target")
	}
}
