package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/monitoring"
	toolstypes "conduit/internal/tools/types"
)

// assertConfigUpdateCounts checks every outcome counter: those in want
// have exactly that value, all others are zero.
func assertConfigUpdateCounts(t *testing.T, gw *Gateway, want map[monitoring.ConfigUpdateOutcome]int64) {
	t.Helper()
	s := gw.ConfigUpdateMetrics()
	for _, o := range monitoring.ConfigUpdateOutcomes {
		if got := s.Count(o); got != want[o] {
			t.Errorf("%s = %d, want %d", o, got, want[o])
		}
	}
}

func TestConfigUpdateMetrics_AppliedLive(t *testing.T) {
	f := newReloadFixture(t)
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.providers.z-ai.timeout_seconds": 600.0,
	}); err != nil {
		t.Fatal(err)
	}
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateAppliedLive: 1})
	s := f.gw.ConfigUpdateMetrics()
	if s.ProvidersRebuilt != 1 {
		t.Errorf("providers rebuilt = %d, want 1", s.ProvidersRebuilt)
	}
	if s.LastApplied == nil || s.LastApplied.IsZero() {
		t.Error("last_applied not set")
	}
}

func TestConfigUpdateMetrics_AppliedRestartRequired(t *testing.T) {
	f := newReloadFixture(t)
	// One live and one restart-only key: counts as restart-required.
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"port":                      18800.0,
		"ai.subagent_default_model": "glm-5.3-flash",
	}); err != nil {
		t.Fatal(err)
	}
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateAppliedRestartRequired: 1})
	s := f.gw.ConfigUpdateMetrics()
	if s.ProvidersRebuilt != 0 || s.LastApplied == nil {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestConfigUpdateMetrics_Unchanged(t *testing.T) {
	f := newReloadFixture(t)
	patch := map[string]interface{}{"ai.providers.z-ai.timeout_seconds": 300.0} // current value
	if _, err := f.gw.PlanConfigUpdate(context.Background(), patch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), patch); err != nil {
		t.Fatal(err)
	}
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateUnchanged: 2})
	if f.gw.ConfigUpdateMetrics().LastApplied != nil {
		t.Error("last_applied set by a no-op update")
	}
}

func TestConfigUpdateMetrics_Planned(t *testing.T) {
	f := newReloadFixture(t)
	if _, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0}); err != nil {
		t.Fatal(err)
	}
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdatePlanned: 1})
}

func TestConfigUpdateMetrics_RejectedInvalid(t *testing.T) {
	for name, patch := range map[string]map[string]interface{}{
		"negative max_concurrent": {"ai.providers.z-ai.max_concurrent": -1.0},
		"type mismatch":           {"port": "not-a-number"},
		"port out of range":       {"port": 80.0},
		"unknown provider":        {"ai.providers.ghost.model": "x"},
		"literal secret":          {"ai.providers.z-ai.api_key": "sk-new-literal"},
		"redaction marker":        {"ai.providers.z-ai.model": config.RedactedValue},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReloadFixture(t)
			if _, err := f.gw.PlanConfigUpdate(context.Background(), patch); err == nil {
				t.Fatal("plan: expected rejection")
			}
			assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateRejectedInvalid: 1})
			if _, err := f.gw.ApplyConfigUpdate(context.Background(), patch); err == nil {
				t.Fatal("apply: expected rejection")
			}
			assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateRejectedInvalid: 2})
		})
	}
}

func TestConfigUpdateMetrics_RejectedConflict(t *testing.T) {
	f := newReloadFixture(t)
	f.gw.reload.beforePersist = func() {
		// A hand edit lands between the read and the write.
		if err := os.WriteFile(f.path, []byte(f.orig+"\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	_, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0})
	if err == nil {
		t.Fatal("expected conflict")
	}
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateRejectedConflict: 1})
}

func TestConfigUpdateMetrics_Failed(t *testing.T) {
	t.Run("persist fails", func(t *testing.T) {
		f := newReloadFixture(t)
		f.gw.reload.beforePersist = func() {
			// The config file becomes unreadable (a directory) before the write.
			if err := os.Remove(f.path); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(f.path, 0o700); err != nil {
				t.Error(err)
			}
		}
		_, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0})
		if err == nil {
			t.Fatal("expected failure")
		}
		assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateFailed: 1})
		if f.gw.ConfigUpdateMetrics().LastApplied != nil {
			t.Error("last_applied set by a failed update")
		}
	})
	t.Run("no config path", func(t *testing.T) {
		gw := &Gateway{config: config.Default()}
		if _, err := gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0}); err == nil {
			t.Fatal("expected error")
		}
		if _, err := gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"port": 18800.0}); err == nil {
			t.Fatal("expected error")
		}
		assertConfigUpdateCounts(t, gw, map[monitoring.ConfigUpdateOutcome]int64{monitoring.ConfigUpdateFailed: 2})
	})
}

func TestConfigUpdateMetrics_Approvals(t *testing.T) {
	gw := &Gateway{config: config.Default()}
	res := func(kind string, d approval.Decision) approval.Resolution {
		return approval.Resolution{Action: approval.Action{Kind: kind}, Decision: d}
	}
	gw.recordApprovalOutcome(res(toolstypes.ConfigUpdateApprovalKind, approval.DecisionDenied))
	gw.recordApprovalOutcome(res(toolstypes.ConfigUpdateApprovalKind, approval.DecisionExpired))
	// Approved is counted by the apply it triggers, not here.
	gw.recordApprovalOutcome(res(toolstypes.ConfigUpdateApprovalKind, approval.DecisionApproved))
	// Other kinds are not config updates.
	gw.recordApprovalOutcome(res("ssh.exec", approval.DecisionDenied))
	gw.recordApprovalOutcome(res("ssh.exec", approval.DecisionExpired))
	assertConfigUpdateCounts(t, gw, map[monitoring.ConfigUpdateOutcome]int64{
		monitoring.ConfigUpdateApprovalDenied:  1,
		monitoring.ConfigUpdateApprovalExpired: 1,
	})
}

func TestConfigUpdateMetrics_ConcurrentApplies(t *testing.T) {
	f := newReloadFixture(t)
	const workers, perWorker = 4, 5
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = f.gw.ConfigUpdateMetrics().ToMap()
			var b bytes.Buffer
			writePrometheusConfigUpdates(&b, f.gw.ConfigUpdateMetrics())
		}
	}()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				v := float64(1000 + w*perWorker + i) // distinct: never a no-op
				if _, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.z-ai.timeout_seconds": v}); err != nil {
					t.Error(err)
				}
				if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.z-ai.timeout_seconds": v}); err != nil {
					t.Error(err)
				}
				_, _ = f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"port": 1.0})
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	n := int64(workers * perWorker)
	assertConfigUpdateCounts(t, f.gw, map[monitoring.ConfigUpdateOutcome]int64{
		monitoring.ConfigUpdatePlanned:         n,
		monitoring.ConfigUpdateAppliedLive:     n,
		monitoring.ConfigUpdateRejectedInvalid: n,
	})
	if got := f.gw.ConfigUpdateMetrics().ProvidersRebuilt; got != n {
		t.Errorf("providers rebuilt = %d, want %d", got, n)
	}
}

func TestConfigUpdateMetrics_Surfaced(t *testing.T) {
	f := newReloadFixture(t)
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{
		"ai.providers.z-ai.timeout_seconds": 600.0,
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"ai.providers.z-ai.api_key": "sk-literal-SHOULD-NOT-LEAK"})

	status, err := f.gw.GetGatewayStatus()
	if err != nil {
		t.Fatal(err)
	}
	cu, ok := status["config_updates"].(map[string]interface{})
	if !ok {
		t.Fatalf("status config_updates = %#v", status["config_updates"])
	}
	if cu["applied_live_total"] != int64(1) || cu["rejected_invalid_total"] != int64(1) || cu["providers_rebuilt_total"] != int64(1) {
		t.Errorf("config_updates = %v", cu)
	}
	if _, ok := cu["last_applied"].(string); !ok {
		t.Errorf("last_applied missing: %v", cu)
	}

	var b bytes.Buffer
	writePrometheusConfigUpdates(&b, f.gw.ConfigUpdateMetrics())
	out := b.String()
	for _, want := range []string{
		`conduit_config_updates_total{outcome="applied_live"} 1`,
		`conduit_config_updates_total{outcome="rejected_invalid"} 1`,
		`conduit_config_updates_total{outcome="failed"} 0`,
		`conduit_config_update_providers_rebuilt_total 1`,
		`conduit_config_update_last_applied_timestamp_seconds `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prometheus output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "conduit_config_update_last_applied_timestamp_seconds 0\n") {
		t.Error("last applied timestamp not exported")
	}

	js, err := json.Marshal(MetricsResponse{ConfigUpdates: f.gw.ConfigUpdateMetrics()})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SHOULD-NOT-LEAK", "timeout_seconds", "z-ai", reloadSecret} {
		if strings.Contains(string(js), leak) || strings.Contains(out, leak) {
			t.Errorf("metrics expose %q", leak)
		}
	}
	if !strings.Contains(string(js), `"config_updates":{"planned_total":0,"applied_live_total":1`) {
		t.Errorf("/metrics JSON = %s", js)
	}
}
