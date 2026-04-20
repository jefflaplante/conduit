package heartbeat

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/database"

	_ "modernc.org/sqlite"
)

// setupAuditDB creates a migrated database for auditor tests.
func setupAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("ConfigureDatabase: %v", err)
	}
	return db
}

// TestAlertAuditor_RecordAndList_RoundTrip verifies that an AlertHistoryEntry
// written via RecordAlert can be read back with matching fields via
// ListRecent. Covers the core conduit-2uvp API.
func TestAlertAuditor_RecordAndList_RoundTrip(t *testing.T) {
	db := setupAuditDB(t)
	a := NewAlertAuditor(db)

	entry := AlertHistoryEntry{
		AlertType:    "disk_space",
		Severity:     "critical",
		Source:       "heartbeat:disk_check",
		Message:      "root partition at 95%",
		ActionTaken:  "delivered:telegram-ops(telegram)",
		ActionResult: "success",
		Details: map[string]any{
			"partition":   "/",
			"used_pct":    95,
			"target_name": "telegram-ops",
		},
	}

	if err := a.RecordAlert(context.Background(), entry); err != nil {
		t.Fatalf("RecordAlert: %v", err)
	}

	rows, err := a.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	got := rows[0]
	if got.AlertType != entry.AlertType {
		t.Errorf("alert_type: want %q, got %q", entry.AlertType, got.AlertType)
	}
	if got.Severity != entry.Severity {
		t.Errorf("severity: want %q, got %q", entry.Severity, got.Severity)
	}
	if got.Source != entry.Source {
		t.Errorf("source: want %q, got %q", entry.Source, got.Source)
	}
	if got.Message != entry.Message {
		t.Errorf("message: want %q, got %q", entry.Message, got.Message)
	}
	if got.ActionTaken != entry.ActionTaken {
		t.Errorf("action_taken: want %q, got %q", entry.ActionTaken, got.ActionTaken)
	}
	if got.ActionResult != entry.ActionResult {
		t.Errorf("action_result: want %q, got %q", entry.ActionResult, got.ActionResult)
	}
	if got.ID <= 0 {
		t.Errorf("expected positive id, got %d", got.ID)
	}
	if got.FiredAt.IsZero() {
		t.Error("fired_at should be populated")
	}

	// Details should round-trip as a JSON object.
	detailsMap, ok := got.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type: want map[string]any, got %T", got.Details)
	}
	if detailsMap["partition"] != "/" {
		t.Errorf("details.partition: want %q, got %v", "/", detailsMap["partition"])
	}
}

// TestAlertAuditor_RecordAlert_ValidatesRequiredFields ensures an auditor
// rejects rows that would violate the NOT NULL columns.
func TestAlertAuditor_RecordAlert_ValidatesRequiredFields(t *testing.T) {
	db := setupAuditDB(t)
	a := NewAlertAuditor(db)

	cases := []struct {
		name  string
		entry AlertHistoryEntry
	}{
		{"missing alert_type", AlertHistoryEntry{Severity: "info", Message: "m"}},
		{"missing severity", AlertHistoryEntry{AlertType: "t", Message: "m"}},
		{"missing message", AlertHistoryEntry{AlertType: "t", Severity: "info"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.RecordAlert(context.Background(), tc.entry); err == nil {
				t.Errorf("expected error for entry %+v, got nil", tc.entry)
			}
		})
	}
}

// TestAlertAuditor_NilSafe verifies that a nil auditor or an auditor with a
// nil DB no-ops cleanly — the delivery path must not panic when auditing is
// disabled.
func TestAlertAuditor_NilSafe(t *testing.T) {
	var a *AlertAuditor
	if err := a.RecordAlert(context.Background(), AlertHistoryEntry{AlertType: "t", Severity: "info", Message: "m"}); err != nil {
		t.Errorf("nil auditor RecordAlert should no-op, got %v", err)
	}
	if _, err := a.ListRecent(context.Background(), 10); err != nil {
		t.Errorf("nil auditor ListRecent should no-op, got %v", err)
	}

	empty := &AlertAuditor{}
	if err := empty.RecordAlert(context.Background(), AlertHistoryEntry{AlertType: "t", Severity: "info", Message: "m"}); err != nil {
		t.Errorf("zero auditor RecordAlert should no-op, got %v", err)
	}
}

// TestAlertAuditor_ListRecent_OrderedNewestFirst confirms the fired_at DESC
// ordering promised by ListRecent.
func TestAlertAuditor_ListRecent_OrderedNewestFirst(t *testing.T) {
	db := setupAuditDB(t)
	a := NewAlertAuditor(db)

	now := time.Now().UTC().Truncate(time.Second)
	for i, ts := range []time.Time{
		now.Add(-3 * time.Hour),
		now.Add(-1 * time.Hour),
		now.Add(-2 * time.Hour),
	} {
		if err := a.RecordAlert(context.Background(), AlertHistoryEntry{
			AlertType: "t",
			Severity:  "info",
			Message:   "msg",
			FiredAt:   ts,
			Details:   map[string]any{"i": i},
		}); err != nil {
			t.Fatalf("RecordAlert %d: %v", i, err)
		}
	}

	rows, err := a.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Newest first: -1h, -2h, -3h.
	expected := []time.Time{
		now.Add(-1 * time.Hour),
		now.Add(-2 * time.Hour),
		now.Add(-3 * time.Hour),
	}
	for i, want := range expected {
		if !rows[i].FiredAt.Equal(want) {
			t.Errorf("row %d fired_at: want %s, got %s", i, want, rows[i].FiredAt)
		}
	}
}

// deliveryAuditMock is a Deliverer whose Deliver result is controllable from
// tests so we can cover both success and failure audit paths.
type deliveryAuditMock struct {
	typ        string
	deliverErr error
}

func (m *deliveryAuditMock) Type() string { return m.typ }
func (m *deliveryAuditMock) Deliver(_ context.Context, _ Alert, _ config.AlertTarget) error {
	return m.deliverErr
}

// TestDeliveryRegistry_AuditsSuccessfulDelivery verifies conduit-2uvp
// end-to-end: calling DeliverAlert on a registry with an attached auditor
// writes an alert_history row.
func TestDeliveryRegistry_AuditsSuccessfulDelivery(t *testing.T) {
	db := setupAuditDB(t)
	auditor := NewAlertAuditor(db)

	reg := NewDeliveryRegistry()
	reg.Register(&deliveryAuditMock{typ: "telegram"})
	reg.SetAuditor(auditor)

	alert := Alert{
		ID:       "a1",
		Source:   "heartbeat:cpu_check",
		Type:     "cpu_high",
		Title:    "CPU 95%",
		Message:  "sustained > 90% for 5m",
		Severity: AlertSeverityWarning,
	}
	target := config.AlertTarget{Name: "telegram-ops", Type: "telegram"}

	if err := reg.DeliverAlert(context.Background(), alert, target); err != nil {
		t.Fatalf("DeliverAlert: %v", err)
	}

	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	row := rows[0]
	if row.AlertType != "cpu_high" {
		t.Errorf("alert_type: want cpu_high, got %q", row.AlertType)
	}
	if row.Severity != "warning" {
		t.Errorf("severity: want warning, got %q", row.Severity)
	}
	if row.Source != "heartbeat:cpu_check" {
		t.Errorf("source: want heartbeat:cpu_check, got %q", row.Source)
	}
	if row.ActionResult != "success" {
		t.Errorf("action_result: want success, got %q", row.ActionResult)
	}
	if row.ActionTaken == "" {
		t.Error("action_taken should include target info")
	}
}

// TestDeliveryRegistry_AuditsFailedDelivery verifies a delivery failure
// produces an audit row whose action_result captures the error.
func TestDeliveryRegistry_AuditsFailedDelivery(t *testing.T) {
	db := setupAuditDB(t)
	auditor := NewAlertAuditor(db)

	reg := NewDeliveryRegistry()
	reg.Register(&deliveryAuditMock{typ: "telegram", deliverErr: errBoom})
	reg.SetAuditor(auditor)

	alert := Alert{
		ID:       "a2",
		Source:   "heartbeat:mem_check",
		Type:     "mem_pressure",
		Title:    "mem",
		Message:  "swap thrashing",
		Severity: AlertSeverityCritical,
	}
	target := config.AlertTarget{Name: "telegram-ops", Type: "telegram"}

	if err := reg.DeliverAlert(context.Background(), alert, target); err == nil {
		t.Fatal("expected DeliverAlert to fail, got nil")
	}

	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	if rows[0].ActionResult == "success" {
		t.Error("action_result should not be success for failed delivery")
	}
	if rows[0].Severity != "critical" {
		t.Errorf("severity: want critical, got %q", rows[0].Severity)
	}
}

// TestDeliveryRegistry_NoAuditorSkipsGracefully verifies a registry with no
// auditor behaves identically to the pre-audit code path (no-op on audit).
func TestDeliveryRegistry_NoAuditorSkipsGracefully(t *testing.T) {
	reg := NewDeliveryRegistry()
	reg.Register(&deliveryAuditMock{typ: "telegram"})

	err := reg.DeliverAlert(context.Background(), Alert{
		ID: "a3", Source: "s", Type: "t", Title: "title",
		Message: "m", Severity: AlertSeverityInfo,
	}, config.AlertTarget{Name: "tele", Type: "telegram"})
	if err != nil {
		t.Fatalf("DeliverAlert without auditor: %v", err)
	}
}

// errBoom is a sentinel used by failure tests.
var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
