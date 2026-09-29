package config

import (
	"reflect"
	"strings"
	"testing"
)

// conduit-2cxu: prunable session prefixes are exact key segments and may
// never reach Telegram or TUI sessions.
func TestNormalizePrunablePrefixes(t *testing.T) {
	got, err := NormalizePrunablePrefixes(nil)
	if err != nil || !reflect.DeepEqual(got, DefaultPrunableSessionPrefixes) {
		t.Fatalf("default = %v, %v", got, err)
	}
	got, err = NormalizePrunablePrefixes([]string{" cron", "heartbeat_", "cron_", "sub-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"cron_", "heartbeat_", "sub-agent_"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"telegram", "Telegram_", "tui", "TUI_", "telegram_123", "tui_jeff", "", " ", "_", "cron*", "cr%n", "a b"} {
		if _, err := NormalizePrunablePrefixes([]string{"cron", bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateMaintenance(t *testing.T) {
	var me multiError
	validateMaintenance(&me, MaintenanceConfig{})
	if err := me.toError(); err != nil {
		t.Fatalf("empty section: %v", err)
	}
	me = multiError{}
	validateMaintenance(&me, MaintenanceConfig{RetentionDays: -1, BatchSize: -5, PrunablePrefixes: []string{"telegram"}})
	err := me.toError()
	if err == nil {
		t.Fatal("invalid section accepted")
	}
	for _, want := range []string{"retention_days", "batch_size", "protected"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
