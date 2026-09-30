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
	neg := -1
	validateMaintenance(&me, MaintenanceConfig{RetentionDays: -1, BatchSize: -5, PrunablePrefixes: []string{"telegram"}, KeepBackups: &neg})
	err := me.toError()
	if err == nil {
		t.Fatal("invalid section accepted")
	}
	for _, want := range []string{"retention_days", "batch_size", "protected", "keep_backups"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// conduit-16f0: keep_backups unset means the default; 0 is "keep all".
func TestMaintenanceKeepBackups(t *testing.T) {
	if got := (MaintenanceConfig{}).EffectiveKeepBackups(); got != DefaultKeepBackups {
		t.Fatalf("unset = %d, want %d", got, DefaultKeepBackups)
	}
	zero, five := 0, 5
	if got := (MaintenanceConfig{KeepBackups: &zero}).EffectiveKeepBackups(); got != 0 {
		t.Fatalf("0 = %d", got)
	}
	if got := (MaintenanceConfig{KeepBackups: &five}).EffectiveKeepBackups(); got != 5 {
		t.Fatalf("5 = %d", got)
	}
	var me multiError
	validateMaintenance(&me, MaintenanceConfig{KeepBackups: &zero})
	if err := me.toError(); err != nil {
		t.Fatalf("keep_backups 0 rejected: %v", err)
	}
}
