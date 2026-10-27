package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/heartbeat"
	"conduit/internal/mqttevents"
	"conduit/internal/sessions"
)

// The gateway spawner runs an event sub-agent on the shared spawn path,
// persists its label and skill scope, and calls onDone once the turn ends.
func TestMQTTEventSpawner_SpawnsLabeledSubAgentAndCallsOnDone(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.setLifecycleCtx(context.Background())
	gw.ai = subAgentFallbackRouter(t, []ai.MockResponse{{Content: "Battery looks fine. Report follows."}})

	done := make(chan struct{})
	sp := &mqttEventSpawner{g: gw}
	key, err := sp.Spawn(context.Background(), mqttevents.SpawnRequest{
		RuleID: "r", Label: "mqtt-r", Task: "AUTOMATED EVENT (rule r). check it",
		Priority: "medium", Skills: []string{"solar"}, TimeoutS: 10,
	}, func() { close(done) })
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !strings.HasPrefix(key, "subagent_") {
		t.Fatalf("session key = %q, want subagent_*", key)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onDone not called after the sub-agent finished")
	}

	sess, err := store.GetSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Context["label"] != "mqtt-r" {
		t.Errorf("label = %q, want mqtt-r", sess.Context["label"])
	}
	if sess.Context[sessions.ContextSkillFilter] != "solar" {
		t.Errorf("skill filter = %q, want solar", sess.Context[sessions.ContextSkillFilter])
	}
}

// A refused spawn returns the error and never calls onDone (the engine frees
// its slot itself and downgrades to queue).
func TestMQTTEventSpawner_DrainingRefusesWithoutOnDone(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	sp := &mqttEventSpawner{g: gw, ownerTarget: "telegram:123456789"}
	if _, err := sp.Spawn(ctx, mqttevents.SpawnRequest{RuleID: "r", Label: "mqtt-r", Task: "t", Priority: "high"}, func() { called = true }); err == nil {
		t.Fatal("want error from a canceled spawn context")
	}
	if called {
		t.Error("onDone must not be called when the spawn fails")
	}
}

func TestMQTTEventQueuer_DowngradeDetailAndUniqueIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	q := &mqttEventQueuer{q: heartbeat.NewSharedAlertQueue(path)}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ { // same timestamp twice: IDs must still differ
		if err := q.Enqueue(mqttevents.Notice{RuleID: "r", Topic: "sensors/a", Priority: "high", Message: "m", Timestamp: ts, Downgraded: "max_concurrent_spawns (2) reached"}); err != nil {
			t.Fatal(err)
		}
	}
	alerts, err := heartbeat.NewSharedAlertQueue(path).GetPendingAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 || alerts[0].ID == alerts[1].ID {
		t.Fatalf("want 2 alerts with distinct IDs, got %+v", alerts)
	}
	if !strings.Contains(alerts[0].Details, "max_concurrent_spawns") {
		t.Errorf("details = %q, want the downgrade reason", alerts[0].Details)
	}
}
