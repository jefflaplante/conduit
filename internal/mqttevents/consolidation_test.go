package mqttevents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/mqtt"
)

// Consolidation rule tests (bd-e9o8 phase 2). Each case mirrors one harness
// case in scripts/test-event-rules.py: the reference actor is the executable
// contract, these tests are the Go engine converging on it. Constants are
// taken from rules/*.yaml consts blocks (window_s 60, entity_limit 20,
// anomaly_sample 3, slow_ms 30000, suppress_s 600, heartbeat_s 3600) — the
// Python harness re-reads the YAML on every run, so a Go/YAML knob drift
// fails there.

// --- fakes -----------------------------------------------------------------

type consolidationPublisher struct {
	mu   sync.Mutex
	pubs []pubRec
}

type pubRec struct {
	topic    string
	payload  []byte
	qos      byte
	retained bool
}

func (m *consolidationPublisher) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*mqtt.PublishResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pubs = append(m.pubs, pubRec{topic: topic, payload: append([]byte(nil), payload...), qos: qos, retained: retained})
	return &mqtt.PublishResult{}, nil
}

func (m *consolidationPublisher) pubsOn(topic string) []pubRec {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []pubRec
	for _, p := range m.pubs {
		if p.topic == topic {
			out = append(out, p)
		}
	}
	return out
}

func (m *consolidationPublisher) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pubs = nil
}

// all returns a copy of every recorded publication across topics.
func (m *consolidationPublisher) all() []pubRec {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]pubRec, len(m.pubs))
	copy(out, m.pubs)
	return out
}

// --- rule builders (mirror rules/*.yaml) ------------------------------------

const (
	consRootTopic   = "conduit/events"
	consAlertTopic  = "alerts/pipeline"
	consSummaryPath = "conduit/events/pipeline"
	consHealthPath  = "conduit/health/pipeline-noise"
)

func windowRule(id string) config.MQTTEventRule {
	// rules/dedupe-buffer.yaml.
	return config.MQTTEventRule{
		ID:    id,
		Topic: "conduit/events/pipeline-run/#",
		Condition: config.MQTTEventCondition{
			Type:            "window_buffer",
			Key:             "source_entity",
			WindowS:         60,
			WindowTimeoutMs: 5000,
			MaxEntitiesFull: 20,
			AnomalySample:   3,
			ClockField:      "ts", // docs edge case 6: event clock, not receive time
			Any: []config.MQTTEventAttrCheck{ // docs: error/slow runs get quoted
				{Type: "attrs_nonempty", Field: "error"},
				{Type: "attrs_threshold", Field: "duration_ms", Op: "gt", Value: 30000},
			},
		},
		Action: config.MQTTEventAction{
			Type:             "publish",
			Topic:            consRootTopic,
			SubtopicTemplate: "pipeline",
			PayloadTemplate:  "pipeline-summary",
			// single_passthrough defaults true; id_strategy defaults uuid_v5
		},
	}
}

func anomalyRule(id string) config.MQTTEventRule {
	// rules/anomaly-escalate.yaml.
	return config.MQTTEventRule{
		ID:    id,
		Topic: consRootTopic + "/#", // subtree: runs AND summaries are alert-worthy
		Condition: config.MQTTEventCondition{
			Type: "attrs_any",
			Any: []config.MQTTEventAttrCheck{
				{Type: "attrs_nonempty", Field: "error"},
				{Type: "attrs_threshold", Field: "duration_ms", Op: "gt", Value: 30000},
			},
			AnomalySample: 3,
		},
		Action: config.MQTTEventAction{
			Type:             "alert",
			Topic:            "alerts",
			SubtopicTemplate: "pipeline",
			Severity:         "warn",
			Attach:           []string{"anomaly_samples"},
			Priority:         "medium",
			PromptTemplate:   testTmpl,
			Suppression: &config.MQTTEventSuppression{
				Key:     "source_entity",
				WindowS: 600,
			},
		},
	}
}

func noiseRule(id string) config.MQTTEventRule {
	// rules/noise-floor.yaml: drop_and_count + hourly heartbeat.
	return config.MQTTEventRule{
		ID:    id,
		Topic: consSummaryPath,
		Condition: config.MQTTEventCondition{
			Type: "catch_all",
		},
		Action: config.MQTTEventAction{
			Type: "drop_and_count",
			Heartbeat: &config.MQTTEventHeartbeat{
				Topic:            "conduit/health",
				SubtopicTemplate: "pipeline-noise",
				PayloadTemplate:  "pipeline-noise-heartbeat",
				PayloadConsts:    map[string]any{"window_s": 60},
				IntervalS:        3600,
				Retained:         true,
				QoS:              1,
			},
		},
	}
}

func configValidateRule(r config.MQTTEventRule) error {
	cfg := config.MQTTEventsConfig{Enabled: true, Rules: []config.MQTTEventRule{r}}
	return cfg.Validate()
}

// --- helpers ---------------------------------------------------------------

// runEvent builds an HA bridge pipeline-run event with an event clock (the
// payload ts field the engine's ClockField points at).
func runEvent(entity string, m map[string]any, ts time.Time) mqtt.Event {
	payload, _ := json.Marshal(map[string]any{
		"source_entity": entity,
		"attrs":         m,
		"ts":            ts.UnixMilli(),
	})
	return mqtt.Event{
		Topic:     "conduit/events/pipeline-run/" + entity,
		Payload:   payload,
		Timestamp: ts,
	}
}

// pipelineEvent builds an already-consolidated event on conduit/events/pipeline.
func pipelineEvent(m map[string]any, ts time.Time) mqtt.Event {
	payload, _ := json.Marshal(m)
	return mqtt.Event{Topic: consSummaryPath, Payload: payload, Timestamp: ts}
}

type consHarness struct {
	e   *Engine
	pub *consolidationPublisher
	sp  *mockSpawner
	q   *mockQueuer
}

func newConsHarness(t *testing.T, rules ...config.MQTTEventRule) *consHarness {
	t.Helper()
	cfg := config.MQTTEventsConfig{Enabled: true, Rules: rules}
	h := &consHarness{pub: &consolidationPublisher{}, sp: &mockSpawner{}, q: &mockQueuer{}}
	e, err := NewEngine(cfg, Deps{Spawner: h.sp, Queuer: h.q, Publisher: h.pub})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(e.Stop)
	h.e = e
	return h
}

// drain dispatches any queued consolidation jobs so publication assertions
// are deterministic (the worker is asynchronous).
func (h *consHarness) drain() { h.e.drain() }

// at is the fixed epoch the event clocks run on.
func at(sec int) time.Time { return time.Unix(1_700_000_000+int64(sec), 0) }

func mustJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("payload not a JSON object: %v\n%s", err, b)
	}
	return m
}

// --- tests -----------------------------------------------------------------

// Harness case "single-run-passthrough": one run in a window flushes as a
// normal pipeline-run, no summary envelope.
func TestConsolidationSingleRunPassthrough(t *testing.T) {
	h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"))
	h.e.HandleEvent(runEvent("synth.sensor.one", map[string]any{"tool": "doorbell_cam", "duration_ms": 900}, at(0)))
	// Window must still hold the run (no early publish).
	if got := h.pub.pubsOn(consSummaryPath); len(got) != 0 {
		t.Fatalf("run published before flush: %d pubs", len(got))
	}
	// Flush with 1 run = passthrough.
	if !h.e.FlushConsolidation("pipeline-dedupe-buffer") {
		t.Fatal("flush returned false for rule with buffered runs")
	}
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 {
		t.Fatalf("expected 1 publication, got %d", len(pubs))
	}
	p := mustJSON(t, pubs[0].payload)
	if p["type"] != "pipeline-run" {
		t.Fatalf("passthrough must stay type=pipeline-run, got %v", p["type"])
	}
	if p["source_entity"] != "synth.sensor.one" {
		t.Fatalf("passthrough source_entity = %v", p["source_entity"])
	}
	if _, has := p["run_count"]; has {
		t.Fatal("single run must NOT get summary envelope")
	}
}

// Harness case "burst-7-runs-3-entities-summary": 7 runs / 3 entities, one
// merged summary per entity window.
func TestConsolidationBurstSummary(t *testing.T) {
	h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"))
	tools := []string{"doorbell_cam", "doorbell_cam", "notify", "notify", "lock_sync", "doorbell_cam", "notify"}
	ents := []string{"synth.sensor.one", "synth.sensor.two", "synth.sensor.three"}
	for i, tool := range tools {
		h.e.HandleEvent(runEvent(ents[i%3], map[string]any{"tool": tool, "duration_ms": 400 + 10*i}, at(0)))
	}
	// FlushConsolidation drains ALL of a rule's buffered windows in one
	// call: first call must flush (3 entity windows -> 3 publications), and
	// subsequent calls must find nothing.
	if !h.e.FlushConsolidation("pipeline-dedupe-buffer") {
		t.Fatal("flush found no buffer for rule with buffered runs")
	}
	if h.e.FlushConsolidation("pipeline-dedupe-buffer") {
		t.Fatal("second flush found work after full drain")
	}
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 3 {
		t.Fatalf("3 entities -> 3 publications, got %d", len(pubs))
	}
	total, summaries := 0, 0
	for _, p := range pubs {
		m := mustJSON(t, p.payload)
		if m["type"] != "pipeline-summary" {
			t.Fatalf("all bursts are multi-run, got type=%v", m["type"])
		}
		rc, _ := m["run_count"].(float64)
		total += int(rc)
		summaries++
		if m["entity_count"].(float64) != 1 {
			t.Fatalf("per-entity summary counts its own entity, got %v", m["entity_count"])
		}
		hist, _ := m["tool_histogram"].(map[string]any)
		hs := 0
		for _, v := range hist {
			hs += int(v.(float64))
		}
		if hs != int(rc) {
			t.Fatalf("tool histogram must sum to run_count: %d vs %v", hs, m["run_count"])
		}
	}
	if total != 7 || summaries != 3 {
		t.Fatalf("expected 7 runs in 3 summaries, got %d runs in %d", total, summaries)
	}
}

// Harness case "window-boundary-straddle": tumbling, not sliding — a burst
// straddling the boundary yields two publications of sizes 2 and 1.
func TestConsolidationWindowBoundary(t *testing.T) {
	h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"))
	e := "synth.sensor.one"
	h.e.HandleEvent(runEvent(e, map[string]any{"tool": "doorbell_cam", "duration_ms": 200}, at(0)))
	h.e.HandleEvent(runEvent(e, map[string]any{"tool": "notify", "duration_ms": 200}, at(45)))    // inside 60s
	h.e.HandleEvent(runEvent(e, map[string]any{"tool": "lock_sync", "duration_ms": 200}, at(65))) // 65s > anchor+60s: flushes window 1, opens window 2
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 {
		t.Fatalf("boundary crossing must flush exactly window 1, got %d pubs", len(pubs))
	}
	if got := mustJSON(t, pubs[0].payload)["run_count"].(float64); got != 2 {
		t.Fatalf("window 1 holds the first 2 runs, got %v", got)
	}
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	pubs = h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 2 {
		t.Fatalf("straddling burst must yield exactly 2 publications, got %d", len(pubs))
	}
	// Window 2 holds a single run -> passthrough (docs: flush with 1 run is
	// a normal pipeline-run event, no summary envelope) — same on manual flush.
	m := mustJSON(t, pubs[1].payload)
	if m["type"] != "pipeline-run" {
		t.Fatalf("window 2 passthrough must stay type=pipeline-run, got %v", m["type"])
	}
	if m["source_entity"] != e {
		t.Fatalf("passthrough must carry the original entity, got %v", m["source_entity"])
	}
}

// Docs edge case 6: clock = event timestamps. A burst replayed with late
// broker receive times must not split windows.
func TestConsolidationUsesEventClockNotReceiveTime(t *testing.T) {
	h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"))
	e := "synth.sensor.one"
	rcv := at(3600) // broker receive time way past window_s
	for i := 0; i < 3; i++ {
		ev := runEvent(e, map[string]any{"tool": "notify", "duration_ms": 100}, at(i*5))
		ev.Timestamp = rcv
		h.e.HandleEvent(ev)
	}
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 {
		t.Fatalf("event clock must keep the window whole, got %d pubs", len(pubs))
	}
	if got := mustJSON(t, pubs[0].payload)["run_count"].(float64); got != 3 {
		t.Fatalf("all 3 runs must land in one window, got run_count=%v", got)
	}
}

// Reduced form: a multi-entity summary spanning more than entity_limit
// entities lists the first entity_limit plus a "more" count (docs rule 1).
// Reached via a coarse fan-out key, exactly as docs suggest for wide bursts.
func TestConsolidationReducedEntityForm(t *testing.T) {
	rule := windowRule("pipeline-dedupe-buffer")
	rule.Condition.Key = "automation" // coarse fan-out key carried in attrs
	rule.Condition.KeySource = "attrs"
	h := newConsHarness(t, rule)
	for i := 0; i < 25; i++ {
		ent := fmt.Sprintf("synth.sensor.entity_%02d", i)
		h.e.HandleEvent(runEvent(ent, map[string]any{"tool": "notify", "duration_ms": 100, "automation": "bridge.doorbell"}, at(0)))
	}
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 {
		t.Fatalf("one coarse-key window -> one summary, got %d", len(pubs))
	}
	m := mustJSON(t, pubs[0].payload)
	if m["run_count"].(float64) != 25 {
		t.Fatalf("run_count=25 expected, got %v", m["run_count"])
	}
	if m["entity_count"].(float64) != 25 {
		t.Fatalf("entity_count must count all entities, got %v", m["entity_count"])
	}
	entities, ok := m["entities"].(map[string]any)
	if !ok {
		t.Fatalf("beyond entity_limit the entity field must be the reduced form, got %T", m["entities"])
	}
	first, _ := entities["first"].([]any)
	if len(first) != 20 {
		t.Fatalf("reduced form lists first 20 entities, got %d", len(first))
	}
	if entities["more"].(float64) != 5 {
		t.Fatalf("reduced form must count the rest (5), got %v", entities["more"])
	}
}

// Harness case "anomaly-sample-cap": many anomalous runs in one window yield
// at most anomaly_sample samples in the summary.
func TestConsolidationAnomalySampleCap(t *testing.T) {
	h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"), anomalyRule("pipeline-anomaly-escalate"))
	e := "synth.sensor.one"
	for i := 0; i < 6; i++ {
		h.e.HandleEvent(runEvent(e, map[string]any{"tool": "notify", "duration_ms": 31000 + i}, at(0)))
	}
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 || mustJSON(t, pubs[0].payload)["type"] != "pipeline-summary" {
		t.Fatalf("6 runs in one window -> one summary, got %d pubs", len(pubs))
	}
	s := mustJSON(t, pubs[0].payload)
	if got := len(s["anomaly_samples"].([]any)); got != 3 {
		t.Fatalf("anomaly samples capped at 3, got %d", got)
	}
}

// Harness cases "error-attrs-escalates" + "slow-run-45000ms-escalates": error
// or slow runs alert immediately (pre-flush); a fast run does not.
func TestConsolidationAlertImmediate(t *testing.T) {
	h := newConsHarness(t, anomalyRule("pipeline-anomaly-escalate"))
	h.e.HandleEvent(runEvent("synth.sensor.two", map[string]any{"tool": "notify", "duration_ms": 500, "error": "smtp 554 refused"}, at(0)))
	h.drain()
	alerts := h.pub.pubsOn(consAlertTopic)
	if len(alerts) != 1 {
		t.Fatalf("error run must fire alert immediately, got %d", len(alerts))
	}
	m := mustJSON(t, alerts[0].payload)
	if m["severity"] != "warn" {
		t.Fatalf("severity=warn expected, got %v", m["severity"])
	}
	samples := m["anomaly_samples"].([]any)
	if len(samples) != 1 || samples[0].(map[string]any)["error"] != "smtp 554 refused" {
		t.Fatalf("alert must carry the sample, got %v", samples)
	}
	if alerts[0].retained {
		t.Fatal("alerts must not be retained")
	}
	if len(h.q.notices) == 0 {
		t.Fatal("alert must also enqueue an owner notice via the Queuer")
	}

	// Slow run escalates too.
	h.pub.clear()
	h.e.HandleEvent(runEvent("synth.sensor.three", map[string]any{"tool": "vision_tag", "duration_ms": 45000}, at(5)))
	h.drain()
	if len(h.pub.pubsOn(consAlertTopic)) != 1 {
		t.Fatal("duration_ms=45000 > 30000 must fire alert")
	}
	// A fast run does not.
	h.e.HandleEvent(runEvent("synth.sensor.three", map[string]any{"tool": "vision_tag", "duration_ms": 900}, at(10)))
	h.drain()
	if n := len(h.pub.pubsOn(consAlertTopic)); n != 1 {
		t.Fatalf("fast run after slow one must not add alerts, got %d", n)
	}
}

// Harness case "suppression-10min": same-entity repeat inside suppress_s does
// not re-alert; after the window a new anomaly alerts again. Suppressed fires
// are counted (Stats) and logged, never published (docs rule 2).
func TestConsolidationAlertSuppression(t *testing.T) {
	h := newConsHarness(t, anomalyRule("pipeline-anomaly-escalate"))
	e := "synth.sensor.two"
	m := map[string]any{"tool": "notify", "duration_ms": 500, "error": "acme broker timeout"}
	h.e.HandleEvent(runEvent(e, m, at(0)))
	h.drain()
	if n := len(h.pub.pubsOn(consAlertTopic)); n != 1 {
		t.Fatalf("first anomaly alerts, got %d", n)
	}
	h.e.HandleEvent(runEvent(e, m, at(30))) // inside 600s suppression
	h.e.HandleEvent(runEvent(e, m, at(60)))
	h.drain()
	if n := len(h.pub.pubsOn(consAlertTopic)); n != 1 {
		t.Fatalf("repeats within suppression must not re-alert, got %d", n)
	}
	if got := h.e.Stats().ConsolidationSuppressed; got != 2 {
		t.Fatalf("suppressed fires must be counted, got %d", got)
	}
	h.e.HandleEvent(runEvent(e, m, at(630))) // suppression window (600s) over
	h.drain()
	if n := len(h.pub.pubsOn(consAlertTopic)); n != 2 {
		t.Fatalf("after suppression window a new anomaly must alert again, got %d", n)
	}
}

// Noise floor: unclaimed events on the summary topic are dropped+counted, no
// publication; the hourly heartbeat reports drops since previous beat.
func TestConsolidationNoiseFloor(t *testing.T) {
	h := newConsHarness(t, noiseRule("pipeline-noise-floor"))
	for i := 0; i < 3; i++ {
		h.e.HandleEvent(pipelineEvent(map[string]any{"type": "foreign.echo", "i": i}, at(i)))
	}
	h.drain()
	if pubs := h.pub.all(); len(pubs) != 0 {
		t.Fatalf("noise floor must drop silently, got %d pubs", len(pubs))
	}
	// Before the hour: no heartbeat (beat clock anchored at first event t=0).
	h.e.SweepConsolidation(at(3599))
	h.drain()
	if pubs := h.pub.pubsOn(consHealthPath); len(pubs) != 0 {
		t.Fatalf("heartbeat must not fire before the hour is up, got %d", len(pubs))
	}
	// Hour elapsed (event clock): heartbeat fires with drops so far.
	h.e.SweepConsolidation(at(3600))
	h.drain()
	hb := h.pub.pubsOn(consHealthPath)
	if len(hb) != 1 {
		t.Fatalf("hour elapsed -> heartbeat must fire, got %d", len(hb))
	}
	m := mustJSON(t, hb[0].payload)
	if m["dropped_count"].(float64) < 3 {
		t.Fatalf("dropped_count must reflect drops, got %v", m["dropped_count"])
	}
	if m["dropped_total"].(float64) < 3 {
		t.Fatalf("dropped_total must be cumulative, got %v", m["dropped_total"])
	}
	if m["window_s"].(float64) != 60 {
		t.Fatalf("heartbeat reports the consolidation window, got %v", m["window_s"])
	}
	if !hb[0].retained {
		t.Fatal("heartbeat is retained so late subscribers see the last beat")
	}
	// Counter resets per beat: next beat reports only the new drops.
	h.e.HandleEvent(pipelineEvent(map[string]any{"type": "foreign.echo"}, at(3700)))
	h.e.SweepConsolidation(at(7200))
	h.drain()
	hb = h.pub.pubsOn(consHealthPath)
	if len(hb) != 2 {
		t.Fatalf("second hourly beat missing, got %d", len(hb))
	}
	if got := mustJSON(t, hb[1].payload)["dropped_count"].(float64); got != 1 {
		t.Fatalf("dropped_count counts since previous beat, got %v", got)
	}
}

// Silence vs breakage: even with zero drops the hourly heartbeat fires.
func TestConsolidationSilenceHeartbeat(t *testing.T) {
	h := newConsHarness(t, noiseRule("pipeline-noise-floor"))
	// No events ever: the rule is overdue immediately (breakage detection).
	h.e.SweepConsolidation(at(3600))
	h.drain()
	hb := h.pub.pubsOn(consHealthPath)
	if len(hb) != 1 {
		t.Fatalf("zero-drop heartbeat must fire, got %d", len(hb))
	}
	if got := mustJSON(t, hb[0].payload)["dropped_count"].(float64); got != 0 {
		t.Fatalf("dropped_count must be 0, got %v", got)
	}
}

// UUIDv5 ids: re-flushing the same window content yields byte-identical
// payloads (idempotent republish, BB8 style).
func TestConsolidationUUIDv5Idempotent(t *testing.T) {
	build := func() *consHarness {
		h := newConsHarness(t, windowRule("pipeline-dedupe-buffer"))
		e := "synth.sensor.one"
		h.e.HandleEvent(runEvent(e, map[string]any{"tool": "notify", "duration_ms": 100}, at(0)))
		h.e.HandleEvent(runEvent(e, map[string]any{"tool": "notify", "duration_ms": 200}, at(1)))
		h.e.FlushConsolidation("pipeline-dedupe-buffer")
		h.drain()
		return h
	}
	first := build().pub.pubsOn(consSummaryPath)
	second := build().pub.pubsOn(consSummaryPath)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected 1 pub per flush, got %d and %d", len(first), len(second))
	}
	if string(first[0].payload) != string(second[0].payload) {
		t.Fatalf("same window content must produce identical payloads:\n%s\n%s", first[0].payload, second[0].payload)
	}
	var idField struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first[0].payload, &idField); err != nil || idField.ID == "" {
		t.Fatalf("summary must carry a deterministic id, got %q (%v)", idField.ID, err)
	}
}

// window_timeout_ms: an automation may take long to append its runs; the
// janitor flushes the window once appends stall past the timeout (real-time
// liveness so an idle passthrough still publishes). Same semantics with a
// test-sized stall bound.
func TestConsolidationSweepFlushesStalledWindow(t *testing.T) {
	rule := windowRule("pipeline-dedupe-buffer")
	rule.Condition.WindowTimeoutMs = 5 // test-sized stall bound
	h := newConsHarness(t, rule)
	h.e.HandleEvent(runEvent("synth.sensor.one", map[string]any{"tool": "notify", "duration_ms": 100}, at(0)))
	time.Sleep(15 * time.Millisecond) // append idle now exceeds the 5ms stall bound
	h.e.SweepConsolidation(at(10))
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 1 {
		t.Fatalf("sweep must flush the stalled window, got %d pubs", len(pubs))
	}
	if mustJSON(t, pubs[0].payload)["type"] != "pipeline-run" {
		t.Fatal("stalled single run passes through")
	}
}

// Memory bound: per-key buffer cap flushes (not unbounded growth) when one
// key exceeds max_buffered_runs.
func TestConsolidationBufferCapFlushes(t *testing.T) {
	rule := windowRule("pipeline-dedupe-buffer")
	rule.Condition.MaxBufferedRuns = 4
	h := newConsHarness(t, rule)
	e := "synth.sensor.one"
	for i := 0; i < 10; i++ { // 10 runs, cap 4: cap-flush at 4 and 8; 2 remain
		h.e.HandleEvent(runEvent(e, map[string]any{"tool": "notify", "duration_ms": 100}, at(0)))
	}
	h.drain()
	caps := 0
	for _, p := range h.pub.pubsOn(consSummaryPath) {
		if mustJSON(t, p.payload)["run_count"].(float64) == 4 {
			caps++
		}
	}
	if caps != 2 {
		t.Fatalf("cap of 4 over 10 runs must flush 2 full windows, got %d", caps)
	}
	// Remaining 2 runs still in the buffer.
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	pubs := h.pub.pubsOn(consSummaryPath)
	last := pubs[len(pubs)-1]
	if got := mustJSON(t, last.payload)["run_count"].(float64); got != 2 {
		t.Fatalf("tail window must hold the remaining 2 runs, got %v", got)
	}
}

// Key-count cap: keys beyond max_buffered_keys evict the oldest key
// (flush-then-evict), so the map cannot grow without bound.
func TestConsolidationKeyCapEvictsOldest(t *testing.T) {
	rule := windowRule("pipeline-dedupe-buffer")
	rule.Condition.MaxBufferedKeys = 3
	h := newConsHarness(t, rule)
	for i := 0; i < 5; i++ {
		h.e.HandleEvent(runEvent(fmt.Sprintf("synth.sensor.e%02d", i), map[string]any{"tool": "notify", "duration_ms": 100}, at(0)))
	}
	h.e.FlushConsolidation("pipeline-dedupe-buffer")
	h.drain()
	// 5 keys through a 3-key map: the 2 oldest keys were flushed on eviction,
	// the 3 tail keys flush here — every run is published exactly once.
	pubs := h.pub.pubsOn(consSummaryPath)
	if len(pubs) != 5 {
		t.Fatalf("all 5 runs must be published exactly once, got %d pubs", len(pubs))
	}
}

// drop_and_count: dropped events are counted per rule and observable via
// Stats (docs rule 3) — no publish, no alert.
func TestConsolidationDropAndCountStats(t *testing.T) {
	h := newConsHarness(t, noiseRule("pipeline-noise-floor"))
	for i := 0; i < 5; i++ {
		h.e.HandleEvent(pipelineEvent(map[string]any{"type": "foreign.echo", "i": i}, at(i)))
	}
	st := h.e.Stats()
	if st.ConsolidationDropped != 5 {
		t.Fatalf("ConsolidationDropped must count drops, got %d", st.ConsolidationDropped)
	}
	if st.ConsolidationPublished != 0 || st.ConsolidationAlerts != 0 {
		t.Fatalf("drop_and_count must not publish or alert, got pub=%d alert=%d", st.ConsolidationPublished, st.ConsolidationAlerts)
	}
}

// v1 invariants: v1 behavior keeps passing next to the new condition/action
// types.
func TestConsolidationDoesNotBreakV1(t *testing.T) {
	h := newConsHarness(t, thresholdRule("v1-threshold", "gt", 10, 0, "queue"))
	h.e.HandleEvent(mqtt.Event{Topic: testTopic, Payload: []byte(`42`), Timestamp: at(0)})
	h.e.Stop() // drain
	if h.q.count() != 1 {
		t.Fatalf("v1 queue action must keep working, got %d notices", h.q.count())
	}
}

// Validation: malformed consolidation rules are rejected at load; v1 rule
// validation is unchanged.
func TestConsolidationValidation(t *testing.T) {
	bad := windowRule("bad-window")
	bad.Condition.WindowS = 0
	err := configValidateRule(bad)
	if err == nil || !strings.Contains(err.Error(), "window_s") {
		t.Fatalf("window_s=0 must be rejected, got %v", err)
	}
	badAlert := anomalyRule("bad-alert")
	badAlert.Action.Severity = ""
	err = configValidateRule(badAlert)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("alert without severity must be rejected, got %v", err)
	}
	badNoise := noiseRule("bad-noise")
	badNoise.Action.Heartbeat.IntervalS = 0
	err = configValidateRule(badNoise)
	if err == nil || !strings.Contains(err.Error(), "interval") {
		t.Fatalf("heartbeat without interval_s must be rejected, got %v", err)
	}
	if err := configValidateRule(windowRule("ok-window")); err != nil {
		t.Fatalf("well-formed window rule must validate: %v", err)
	}
}
