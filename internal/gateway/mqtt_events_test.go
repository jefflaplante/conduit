package gateway

import (
	"path/filepath"
	"testing"

	"conduit/internal/config"
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
