package gateway

import (
	"context"
	"testing"

	"conduit/internal/config"
	"conduit/internal/heartbeat"
)

// TestAlertAuditorWiring_AuditsDelivery verifies that a DeliveryRegistry wired
// with an AlertAuditor (matching what Gateway.New does, conduit-1rp3) persists
// delivery attempts to the alert_history table.
//
// NOTE: newTestGatewayWithSessions builds a *Gateway directly (not via New),
// so this test manually replicates the wiring from New to exercise the full
// audit-trail path without spinning up a full gateway.
func TestAlertAuditorWiring_AuditsDelivery(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)

	// Replicate the wiring from Gateway.New (conduit-1rp3).
	gw.deliveryRegistry = heartbeat.NewDeliveryRegistry()
	gw.deliveryRegistry.SetAuditor(heartbeat.NewAlertAuditor(store.DB()))

	// Register a no-op deliverer so the call succeeds.
	gw.deliveryRegistry.Register(&nopDeliverer{})

	alert := heartbeat.Alert{
		ID:       "test-1rp3",
		Type:     "test_alert",
		Source:   "gateway_test",
		Message:  "wiring smoke test",
		Severity: heartbeat.AlertSeverityInfo,
	}
	target := config.AlertTarget{Name: "test-target", Type: "nop"}

	if err := gw.deliveryRegistry.DeliverAlert(context.Background(), alert, target); err != nil {
		t.Fatalf("DeliverAlert: %v", err)
	}

	// Confirm an alert_history row was written via a direct auditor probe
	// (uses the same DB that the registry's auditor writes to).
	auditor := heartbeat.NewAlertAuditor(store.DB())
	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least 1 alert_history row after delivery, got 0")
	}
	if rows[0].AlertType != "test_alert" {
		t.Errorf("alert_type: want test_alert, got %q", rows[0].AlertType)
	}
	if rows[0].ActionResult != "success" {
		t.Errorf("action_result: want success, got %q", rows[0].ActionResult)
	}
}

// nopDeliverer satisfies heartbeat.Deliverer; always succeeds.
type nopDeliverer struct{}

func (n *nopDeliverer) Type() string { return "nop" }
func (n *nopDeliverer) Deliver(_ context.Context, _ heartbeat.Alert, _ config.AlertTarget) error {
	return nil
}
