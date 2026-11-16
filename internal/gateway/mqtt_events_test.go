package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/mqtt"
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
			eng, err := newMQTTEventsEngine(cfg, nil, nil, nil, nil)
			if err != nil || eng != nil {
				t.Fatalf("want (nil, nil), got (%v, %v)", eng, err)
			}
		})
	}

	eng, err := newMQTTEventsEngine(mqttEventsTestConfig(&config.MQTTEventsConfig{Enabled: true, Rules: []config.MQTTEventRule{rule}}), nil, nil, nil, nil)
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

// recordingPublisher records consolidation outputs handed to the publisher.
type recordingPublisher struct {
	mu     sync.Mutex
	topics []string
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, _ []byte, _ byte, _ bool) (*mqtt.PublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	return &mqtt.PublishResult{}, nil
}

func (p *recordingPublisher) published() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.topics...)
}

// conduit-1v8i: the publisher passed to newMQTTEventsEngine must reach the
// engine, or publish/alert/heartbeat outputs are silently disabled.
func TestNewMQTTEventsEngine_PublishReachesPublisher(t *testing.T) {
	rule := config.MQTTEventRule{
		ID:    "dedupe",
		Topic: "conduit/events/pipeline-run/#",
		Condition: config.MQTTEventCondition{
			Type: "window_buffer", Key: "source_entity", WindowS: 60, WindowTimeoutMs: 5000,
			MaxEntitiesFull: 20, AnomalySample: 3, ClockField: "ts",
		},
		Action: config.MQTTEventAction{
			Type: "publish", Topic: "conduit/events", SubtopicTemplate: "pipeline", PayloadTemplate: "pipeline-summary",
		},
	}
	pub := &recordingPublisher{}
	eng, err := newMQTTEventsEngine(mqttEventsTestConfig(&config.MQTTEventsConfig{Enabled: true, Rules: []config.MQTTEventRule{rule}}), nil, nil, nil, pub)
	if err != nil || eng == nil {
		t.Fatalf("want engine, got (%v, %v)", eng, err)
	}
	eng.Start(context.Background())
	defer eng.Stop()

	ts := time.Unix(1_700_000_000, 0)
	payload, _ := json.Marshal(map[string]any{"source_entity": "synth.sensor.one", "attrs": map[string]any{"tool": "notify"}, "ts": ts.UnixMilli()})
	eng.HandleEvent(mqtt.Event{Topic: "conduit/events/pipeline-run/synth.sensor.one", Payload: payload, Timestamp: ts})
	if !eng.FlushConsolidation("dedupe") {
		t.Fatal("flush found no buffered run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := pub.published(); len(got) == 1 && got[0] == "conduit/events/pipeline" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("publications = %v, want [conduit/events/pipeline]", pub.published())
}
