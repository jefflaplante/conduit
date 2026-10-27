package gateway

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/mqttevents"
)

func mqttEventsTestConfig(ev *config.MQTTEventsConfig) config.MQTTConfig {
	return config.MQTTConfig{
		Enabled:   true,
		BrokerURL: "tcp://broker.example:1883",
		Topics:    []string{"sensors/#"},
		Events:    ev,
	}
}

func TestNewMQTTEventsEngine_NilOrDisabledBuildsNothing(t *testing.T) {
	rule := config.MQTTEventRule{
		ID:        "r",
		Topic:     "sensors/total/battery/state",
		Condition: config.MQTTEventCondition{Type: "state_change"},
		Action:    config.MQTTEventAction{Type: "log", PromptTemplate: "Sensor state changed; summarise what changed and whether it matters."},
	}
	cases := map[string]config.MQTTConfig{
		"nil events":    mqttEventsTestConfig(nil),
		"disabled":      mqttEventsTestConfig(&config.MQTTEventsConfig{Enabled: false, Rules: []config.MQTTEventRule{rule}}),
		"mqtt disabled": {Enabled: false, Events: &config.MQTTEventsConfig{Enabled: true, Rules: []config.MQTTEventRule{rule}}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			eng, err := newMQTTEventsEngine(cfg, nil, nil, nil)
			if err != nil || eng != nil {
				t.Fatalf("want (nil, nil), got (%v, %v)", eng, err)
			}
		})
	}

	eng, err := newMQTTEventsEngine(mqttEventsTestConfig(&config.MQTTEventsConfig{Enabled: true, Rules: []config.MQTTEventRule{rule}}), nil, nil, nil)
	if err != nil || eng == nil {
		t.Fatalf("enabled: want engine, got (%v, %v)", eng, err)
	}
	eng.Stop()
}

func TestMQTTEventQueuer_WritesPendingAlert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "alerts", "pending.json")
	q := &mqttEventQueuer{q: heartbeat.NewSharedAlertQueue(path)}
	for i, prio := range []string{"high", "medium", "low", ""} {
		err := q.Enqueue(mqttevents.Notice{
			RuleID:    "r",
			Topic:     "sensors/total/battery/state",
			Priority:  prio,
			Message:   "AUTOMATED EVENT (rule r, topic sensors/total/battery/state). body",
			Value:     math.NaN(),
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("Enqueue(%q): %v", prio, err)
		}
	}
	alerts, err := heartbeat.NewSharedAlertQueue(path).GetPendingAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 4 {
		t.Fatalf("want 4 pending alerts, got %d", len(alerts))
	}
	want := []heartbeat.AlertSeverity{heartbeat.AlertSeverityCritical, heartbeat.AlertSeverityWarning, heartbeat.AlertSeverityInfo, heartbeat.AlertSeverityInfo}
	for i, a := range alerts {
		if a.Severity != want[i] {
			t.Errorf("alert %d severity = %s, want %s", i, a.Severity, want[i])
		}
		if a.Source != "mqtt-events" || a.Component != "r" {
			t.Errorf("alert %d source/component = %s/%s", i, a.Source, a.Component)
		}
	}
}

func TestMQTTEventAlertQueuePath(t *testing.T) {
	if got, want := mqttEventAlertQueuePath("/ws", config.AgentHeartbeatConfig{}), filepath.Join("/ws", "memory", "alerts", "pending.json"); got != want {
		t.Errorf("default path = %q, want %q", got, want)
	}
	if got := mqttEventAlertQueuePath("/ws", config.AgentHeartbeatConfig{AlertQueuePath: "/abs/q.json"}); got != "/abs/q.json" {
		t.Errorf("absolute override = %q", got)
	}
	if got, want := mqttEventAlertQueuePath("/ws", config.AgentHeartbeatConfig{AlertQueuePath: "rel/q.json"}), filepath.Join("/ws", "rel", "q.json"); got != want {
		t.Errorf("relative override = %q, want %q", got, want)
	}
}

func TestMQTTEventSpawnRouting(t *testing.T) {
	ch, user, announce := mqttEventSpawnRouting("high", "telegram:123456789")
	if ch != "telegram" || user != "123456789" || !announce {
		t.Errorf("high with owner: got %q %q %v", ch, user, announce)
	}
	if _, _, announce := mqttEventSpawnRouting("high", ""); announce {
		t.Error("high without owner target cannot announce")
	}
	for _, p := range []string{"medium", "low"} {
		if _, _, announce := mqttEventSpawnRouting(p, "telegram:123456789"); announce {
			t.Errorf("%s must not announce", p)
		}
	}
}
