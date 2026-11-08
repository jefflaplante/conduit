// Consolidation engine wiring (bd-e9o8 phase 2): window_buffer /
// attrs_any / catch_all conditions and publish / alert / drop_and_count
// actions, per docs/pipeline-consolidation.md and rules/*.yaml. The v1
// engine's routing, worker loop and action verbs are reused unchanged; the
// semantics here converge on the reference actor in
// scripts/test-event-rules.py (the executable contract).
//
// Clocks (docs edge cases 5 and 6): window anchors and flushes run on the
// EVENT clock (the payload field named by condition.clock_field, falling
// back to broker receive time) so a burst bridged late cannot split windows;
// append-stall detection and noise-floor heartbeats run on RECEIVE time, and
// the first heartbeat lands one interval after the rule first sees traffic,
// i.e. drops are reported "since startup".
package mqttevents

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"conduit/internal/config"
	"conduit/internal/mqtt"
)

// Buffer memory caps when a validated config somehow left them unset
// (mirrors the config-side max_buffered_runs/max_buffered_keys defaults).
const (
	fallbackBufferedRuns = 100
	fallbackBufferedKeys = 100
)

// windowEntry is one buffered pipeline-run inside a window.
type windowEntry struct {
	payload []byte         // original payload, re-typed on passthrough
	attrs   map[string]any // payload attrs (or top level) for merging
	entity  string         // resolved key value (e.g. source_entity)
	evTime  time.Time      // event-clock time of the run
	topic   string         // source topic (passthrough type derives from it)
}

// windowState is the per-key tumbling window of a window_buffer rule.
type windowState struct {
	anchor     time.Time     // event-clock time the window opened
	lastAppend time.Time     // receive-clock stamp of the newest run (stall bound)
	entries    []windowEntry // bounded by max_buffered_runs
}

// suppressState is the per-key alert suppression of an alert rule.
type suppressState struct {
	until      time.Time // event-clock suppression horizon
	suppressed int       // fires swallowed inside the window
}

// consState is the in-memory state of one consolidation rule. Touched only
// under the engine's evalMu (like the rest of Rule state).
type consState struct {
	windows      map[string]*windowState   // window_buffer
	keyOrder     []string                  // creation order of window keys (key cap)
	suppress     map[string]*suppressState // alert
	beatAt       time.Time                 // next noise heartbeat due (zero = unarmed)
	dropped      uint64                    // drop_and_count, since previous beat
	droppedTotal uint64                    // drop_and_count, since rule start
}

// windowsKeys returns a snapshot of the window keys in creation order.
func (s *consState) windowsKeys() []string {
	return append([]string(nil), s.keyOrder...)
}

// entriesEmpty reports whether a window holds no buffered entries.
func (w *windowState) entriesEmpty() bool {
	return len(w.entries) == 0
}

// consTwin carries the per-rule knobs resolved once at compile time so
// evaluation does not re-derive them per event.
type consTwin struct {
	// window_buffer
	keyName     string
	keySource   string // top_level | attrs | auto
	window      time.Duration
	stall       time.Duration
	maxEntities int
	sampleCap   int
	clockField  string
	maxRuns     int
	maxKeys     int

	// publish
	passthrough   bool
	idStrategy    string
	pubTopic      string
	pubQoS        byte
	pubRetained   bool
	payloadType   string
	summaryWindow int // window_s reported in summaries

	// alert
	alertTopic    string
	severity      string
	priority      string
	attachSamples bool
	suppressKey   string
	suppressWin   time.Duration

	// drop_and_count heartbeat
	beatTopic  string
	beatType   string
	beatConsts map[string]any
	beatEvery  time.Duration
	beatRetain bool
	beatQoS    byte
}

// newConsTwin compiles the knobs of a consolidation rule from its validated
// config.
func newConsTwin(rc *config.MQTTEventRule) *consTwin {
	c := &rc.Condition
	a := &rc.Action
	t := &consTwin{
		keyName:     c.Key,
		keySource:   c.KeySource,
		window:      time.Duration(c.WindowS) * time.Second,
		stall:       time.Duration(c.WindowTimeoutMs) * time.Millisecond,
		maxEntities: c.MaxEntitiesFull,
		sampleCap:   c.AnomalySample,
		clockField:  c.ClockField,
		maxRuns:     c.MaxBufferedRuns,
		maxKeys:     c.MaxBufferedKeys,
		passthrough: true,
		idStrategy:  a.IDStrategy,
		pubTopic:    strings.TrimSuffix(a.Topic, "/") + "/" + a.SubtopicTemplate,
		pubQoS:      byte(a.QoS),
		pubRetained: a.Retained,
		payloadType: a.PayloadTemplate,
		priority:    a.Priority,
	}
	if t.maxRuns <= 0 {
		t.maxRuns = fallbackBufferedRuns
	}
	if t.maxKeys <= 0 {
		t.maxKeys = fallbackBufferedKeys
	}
	if c.Type == "window_buffer" && a.SinglePassthrough != nil {
		t.passthrough = *a.SinglePassthrough
	}
	t.summaryWindow = int(t.window / time.Second)
	switch a.Type {
	case "alert":
		t.severity = a.Severity
		for _, at := range a.Attach {
			if at == "anomaly_samples" {
				t.attachSamples = true
			}
		}
		t.alertTopic = a.TopicTemplate
		if t.alertTopic == "" {
			t.alertTopic = strings.TrimSuffix(a.Topic, "/") + "/" + a.SubtopicTemplate
		}
		if s := a.Suppression; s != nil {
			t.suppressKey = s.Key
			t.suppressWin = time.Duration(s.WindowS) * time.Second
		}
	case "drop_and_count":
		if h := a.Heartbeat; h != nil {
			t.beatTopic = strings.TrimSuffix(h.Topic, "/") + "/" + h.SubtopicTemplate
			t.beatType = h.PayloadTemplate
			t.beatConsts = h.PayloadConsts
			t.beatEvery = time.Duration(h.IntervalS) * time.Second
			t.beatRetain = h.Retained
			t.beatQoS = byte(h.QoS)
		}
	}
	return t
}

// isConsolidationRule reports whether a rule config uses the bd-e9o8 phase 2
// condition or action types.
func isConsolidationRule(c, a string) bool {
	switch c {
	case "window_buffer", "attrs_any", "catch_all":
		return true
	}
	switch a {
	case "publish", "alert", "drop_and_count":
		return true
	}
	return false
}

// --- payload parsing ---------------------------------------------------------

// parseConsPayload resolves the consolidation view of one event payload in a
// single unmarshal: key (window_buffer), attrs (merging/escalation), event
// clock time — with receive-time fallbacks for unparseable payloads.
func parseConsPayload(payload []byte, t *consTwin, recv time.Time) (key string, attrs map[string]any, evTime time.Time) {
	var top map[string]any
	if err := json.Unmarshal(payload, &top); err != nil || top == nil {
		return "", nil, recv
	}
	a, _ := top["attrs"].(map[string]any)
	fieldVal := func(field, src string) any {
		if src == "top_level" {
			return top[field]
		}
		if src == "attrs" {
			if a == nil {
				return nil
			}
			return a[field]
		}
		// auto: top level wins, attrs as fallback
		if v, ok := top[field]; ok && v != nil {
			return v
		}
		if a == nil {
			return nil
		}
		return a[field]
	}
	if t.keyName != "" {
		switch v := fieldVal(t.keyName, t.keySource).(type) {
		case string:
			key = v
		case float64:
			key = fmt.Sprintf("%v", v)
		}
	}
	if a != nil {
		attrs = a
	} else {
		attrs = top // payloads without an attrs object: top level is the contract
	}
	// Top-level fields stay visible to attr-based logic (error/slow checks,
	// entity extraction under a coarse key) even when an attrs object exists.
	for _, f := range []string{"source_entity", "error", "duration_ms", "tool"} {
		if _, ok := attrs[f]; !ok {
			if v, ok := top[f]; ok {
				attrs[f] = v
			}
		}
	}
	if t.clockField != "" {
		evTime = eventClock(fieldVal(t.clockField, "auto"), recv)
	} else {
		evTime = recv
	}
	return key, attrs, evTime
}

// eventClock interprets a clock field value: epoch milliseconds or seconds
// numerically, RFC3339 as a string. Window math only ever uses differences,
// so the origin does not matter — only consistent units do.
func eventClock(v any, recv time.Time) time.Time {
	switch x := v.(type) {
	case float64:
		if x > 1e12 { // epoch ms
			return time.UnixMilli(int64(x))
		}
		return time.Unix(int64(x), 0)
	case string:
		if ts, err := time.Parse(time.RFC3339, x); err == nil {
			return ts
		}
	}
	return recv
}

// --- condition evaluation (called under evalMu) -------------------------------

// evaluateConsolidation runs a consolidation condition and stages any output
// jobs on the rule; the engine drains rule.pending into the worker channel
// (the same bounded channel v1 actions use).
func (r *Rule) evaluateConsolidation(ev mqtt.Event, t *consTwin) outcome {
	key, attrs, evTime := parseConsPayload(ev.Payload, t, ev.Timestamp)
	switch r.cfg.Condition.Type {
	case "window_buffer":
		r.windowBuffer(key, attrs, evTime, ev, t)
		return noMatch // publications happen at flush, not per event
	case "attrs_any":
		if r.attrsAnyMatches(attrs) {
			r.alertFire(key, attrs, evTime, ev, t)
			return fire
		}
		return noMatch
	case "catch_all":
		r.dropAndCount(evTime, t)
		return fire
	}
	return noMatch
}

// windowBuffer buffers the event into its key's tumbling window. A window
// flushes when a later event crosses anchor+window_s (tumbling, docs edge
// case 1), when the per-key run cap is hit, or on a sweep after appends
// stall past window_timeout_ms.
func (r *Rule) windowBuffer(key string, attrs map[string]any, evTime time.Time, ev mqtt.Event, t *consTwin) {
	if r.cons == nil {
		r.cons = &consState{windows: map[string]*windowState{}}
	}
	if key == "" {
		key = "\x00unkeyed" // unparseable payloads still buffer (bounded like any key)
	}
	w := r.cons.windows[key]
	if w == nil {
		// Key cap: flush and drop the oldest key first — the map never
		// exceeds max_keys live windows.
		if len(r.cons.windows) >= t.maxKeys {
			r.evictOldest(t)
		}
		w = &windowState{anchor: evTime, lastAppend: ev.Timestamp}
		r.cons.windows[key] = w
		r.cons.keyOrder = append(r.cons.keyOrder, key)
	} else if evTime.Sub(w.anchor) >= t.window {
		// Tumbling: past anchor+window_s the old window flushes and the new
		// one anchors at THIS event. An anchor never moves because a later
		// run arrived (docs rule 1).
		r.flushWindow(key, t)
		w = r.cons.windows[key]
		w.anchor = evTime
		w.lastAppend = ev.Timestamp
	}
	w.entries = append(w.entries, windowEntry{
		payload: append([]byte(nil), ev.Payload...),
		attrs:   attrs,
		entity:  key,
		evTime:  evTime,
		topic:   ev.Topic,
	})
	w.lastAppend = ev.Timestamp
	// Per-key run cap: flush the full window rather than grow unbounded
	// (bounded memory: a flood on one key becomes a series of summaries).
	if len(w.entries) >= t.maxRuns {
		r.flushWindow(key, t)
		w = r.cons.windows[key]
		w.anchor = evTime
		w.lastAppend = ev.Timestamp
	}
}

// attrsAnyMatches evaluates the attrs_any checks (error non-empty OR slow).
func (r *Rule) attrsAnyMatches(attrs map[string]any) bool {
	for _, chk := range r.cfg.Condition.Any {
		v, ok := attrs[chk.Field]
		if !ok {
			continue
		}
		switch chk.Type {
		case "attrs_nonempty":
			if s, isS := v.(string); isS && strings.TrimSpace(s) != "" {
				return true
			}
		case "attrs_threshold":
			if f, isF := toFloat(v); isF && compare(chk.Op, f, chk.Value) {
				return true
			}
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// --- flush / summaries ---------------------------------------------------------

// flushWindow publishes one window: 1 run = passthrough (a normal event, no
// envelope), N>1 = one merged pipeline-summary. Called under evalMu from
// boundary crossing, cap flush, eviction, manual flush and sweeps.
func (r *Rule) flushWindow(key string, t *consTwin) bool {
	if r.cons == nil {
		return false
	}
	w := r.cons.windows[key]
	if w == nil || len(w.entries) == 0 {
		return false
	}
	entries := w.entries
	w.entries = nil
	if len(entries) == 1 && t.passthrough {
		// Passthrough: the original event as a normal pipeline-run (no
		// summary envelope). The payload's type reflects its source topic —
		// a run buffered from conduit/events/pipeline-run/<entity> is
		// republished as type "pipeline-run" — so consumers see a normal
		// run regardless of buffering.
		p := entries[0]
		payload := passthroughPayload(p)
		r.stage(&consJob{rule: r, kind: "publish", topic: t.pubTopic, payload: payload, qos: t.pubQoS, retained: t.pubRetained})
		return true
	}
	summary := r.buildSummary(entries, t)
	if t.idStrategy == "uuid_v5" {
		summary["id"] = deterministicID(t.pubTopic, summary)
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		log.Printf("[MQTTEvents] rule=%s: summary marshal failed: %v", r.ID(), err)
		return true
	}
	r.stage(&consJob{rule: r, kind: "publish", topic: t.pubTopic, payload: payload, qos: t.pubQoS, retained: t.pubRetained})
	return true
}

// buildSummary merges buffered runs into one pipeline-summary (docs rule 1):
// run_count, entity_count, tool_histogram, anomaly samples (capped, oldest
// first) and window_s; beyond max_entities_full the entity list switches to
// reduced form {first: [...], more: N}.
func (r *Rule) buildSummary(entries []windowEntry, t *consTwin) map[string]any {
	entities := make([]string, 0, len(entries))
	seen := map[string]bool{}
	hist := map[string]int{}
	type sample struct {
		m map[string]any
		t time.Time
	}
	var anomalies []sample
	entityOf := func(e windowEntry) string {
		if s, ok := e.attrs["source_entity"].(string); ok && s != "" {
			return s // the true entity id, even under a coarse fan-out key
		}
		return e.entity
	}
	for _, e := range entries {
		ent := entityOf(e)
		if ent != "" && !seen[ent] {
			seen[ent] = true
			entities = append(entities, ent)
		}
		tool := "unknown"
		if e.attrs != nil {
			if s, ok := e.attrs["tool"].(string); ok && s != "" {
				tool = s
			}
		}
		hist[tool]++
		if r.attrsAnyMatches(e.attrs) {
			anomalies = append(anomalies, sample{m: sampleOf(e), t: e.evTime})
		}
	}
	sort.Strings(entities)
	// Stable sort keeps equal-timestamp samples in buffer (arrival) order, so
	// the payload — and therefore its deterministic id — is reproducible.
	sort.SliceStable(anomalies, func(i, j int) bool { return anomalies[i].t.Before(anomalies[j].t) })
	if len(anomalies) > t.sampleCap {
		anomalies = anomalies[:t.sampleCap]
	}
	samples := make([]any, len(anomalies))
	for i, a := range anomalies {
		samples[i] = a.m
	}
	var entityField any = entities
	entityCount := len(entities)
	if entityCount > t.maxEntities {
		first := entities[:t.maxEntities]
		entityField = map[string]any{"first": first, "more": entityCount - t.maxEntities}
	}
	return map[string]any{
		"type":            t.payloadType,
		"run_count":       len(entries),
		"entity_count":    entityCount,
		"entities":        entityField,
		"tool_histogram":  hist,
		"anomaly_samples": samples,
		"window_s":        t.summaryWindow,
	}
}

// sampleOf quotes one anomalous run: source_entity, error, duration_ms, tool.
// passthroughPayload returns a single-run window's payload re-typed for
// republish: the type field mirrors the run's source topic segment (a run
// from .../pipeline-run/<entity> stays a "pipeline-run"). Unmarshal failures
// fall back to the original bytes.
func passthroughPayload(e windowEntry) []byte {
	var top map[string]any
	if err := json.Unmarshal(e.payload, &top); err != nil || top == nil {
		return e.payload
	}
	seg := e.topic
	// The run type is the topic segment BEFORE the trailing entity id
	// (conduit/events/pipeline-run/<entity> -> "pipeline-run").
	parts := strings.Split(seg, "/")
	if len(parts) >= 2 {
		seg = parts[len(parts)-2]
	}
	top["type"] = seg
	b, err := json.Marshal(top)
	if err != nil {
		return e.payload
	}
	return b
}

func sampleOf(e windowEntry) map[string]any {
	s := map[string]any{
		"source_entity": e.entity,
		"error":         "",
		"duration_ms":   nil,
		"tool":          "",
	}
	if e.attrs == nil {
		return s
	}
	if v, ok := e.attrs["error"].(string); ok {
		s["error"] = v
	}
	if v, ok := e.attrs["duration_ms"]; ok {
		s["duration_ms"] = v
	}
	if v, ok := e.attrs["tool"].(string); ok {
		s["tool"] = v
	}
	return s
}

// evictOldest flushes and removes the oldest live key's window (memory bound:
// the windows map never exceeds max_keys entries).
func (r *Rule) evictOldest(t *consTwin) {
	for len(r.cons.keyOrder) > 0 {
		oldest := r.cons.keyOrder[0]
		r.cons.keyOrder = r.cons.keyOrder[1:]
		if _, live := r.cons.windows[oldest]; live {
			r.flushWindow(oldest, t)
			r.deleteKey(oldest)
			return
		}
	}
}

// deleteKey removes a key from the window map and the order index (bounded
// by max_keys, so the linear scan is cheap).
func (r *Rule) deleteKey(key string) {
	delete(r.cons.windows, key)
	for i, k := range r.cons.keyOrder {
		if k == key {
			r.cons.keyOrder = append(r.cons.keyOrder[:i], r.cons.keyOrder[i+1:]...)
			return
		}
	}
}

// --- alert path ---------------------------------------------------------------

// alertFire handles an attrs_any match: the first qualifying event for a key
// publishes the alert (severity, samples) and queues an owner notice; repeat
// fires inside suppress_s are counted and logged, never published (docs rule
// 2: the first alert already carries the samples — a flapping automation
// must not page every hop).
func (r *Rule) alertFire(key string, attrs map[string]any, evTime time.Time, ev mqtt.Event, t *consTwin) {
	if r.cons == nil {
		r.cons = &consState{suppress: map[string]*suppressState{}}
	}
	if key == "" && attrs != nil {
		if s, ok := attrs[t.suppressKey].(string); ok {
			key = s
		}
	}
	if key == "" {
		key = ev.Topic // last resort: per-topic suppression
	}
	if t.suppressWin > 0 {
		if s := r.cons.suppress[key]; s != nil && evTime.Before(s.until) {
			s.suppressed++
			if r.eng != nil {
				r.eng.stats.consSuppressed.Add(1)
			}
			log.Printf("[MQTTEvents] rule=%s: alert suppressed for %s (%d in window)", r.ID(), key, s.suppressed)
			return
		}
	}
	r.cons.suppress[key] = &suppressState{until: evTime.Add(t.suppressWin)}

	alert := map[string]any{
		"type":          "pipeline-anomaly",
		"source_entity": key,
		"severity":      t.severity,
		"ts":            evTime.UnixMilli(),
	}
	if t.attachSamples {
		alert["anomaly_samples"] = []any{sampleOf(windowEntry{entity: key, attrs: attrs, evTime: evTime})}
	}
	alert["id"] = deterministicID(t.alertTopic, alert)
	payload, err := json.Marshal(alert)
	if err != nil {
		log.Printf("[MQTTEvents] rule=%s: alert marshal failed: %v", r.ID(), err)
		return
	}
	r.stage(&consJob{rule: r, kind: "alert", topic: t.alertTopic, payload: payload, qos: t.pubQoS})
	// The alert path also reaches the owner through the shared alert queue
	// (the same Queuer v1 uses; severity rides action.priority).
	msg := fmt.Sprintf("%s\n\nPipeline anomaly on %s (severity %s): error %q, duration_ms %v. Repeats for this entity are suppressed for %s.",
		Preamble(r.ID(), ev.Topic), key, t.severity, alertStr(attrs, "error"), attrsVal(attrs, "duration_ms"), t.suppressWin)
	r.stage(&consJob{rule: r, kind: "notice", notice: &Notice{
		RuleID: r.ID(), Topic: t.alertTopic, Priority: t.priority, Message: msg, Timestamp: ev.Timestamp,
	}})
}

func alertStr(attrs map[string]any, field string) string {
	if attrs == nil {
		return ""
	}
	s, _ := attrs[field].(string)
	return s
}

func attrsVal(attrs map[string]any, field string) any {
	if attrs == nil {
		return nil
	}
	return attrs[field]
}

// --- noise floor ---------------------------------------------------------------

// dropAndCount records one unclaimed event (docs rule 3: dropped silently
// but counted) and publishes the hourly heartbeat when the beat went due.
func (r *Rule) dropAndCount(evTime time.Time, t *consTwin) {
	if r.cons == nil {
		r.cons = &consState{}
	}
	r.cons.dropped++
	r.cons.droppedTotal++
	if r.eng != nil {
		r.eng.stats.consDropped.Add(1)
	}
	// First beat lands one interval after the rule first sees traffic (docs
	// edge case 5: the first beat reports drops since startup).
	if r.cons.beatAt.IsZero() {
		r.cons.beatAt = evTime.Add(t.beatEvery)
	}
	if t.beatEvery > 0 && !evTime.Before(r.cons.beatAt) {
		r.publishHeartbeat(evTime, t)
	}
}

// publishHeartbeat emits the noise-floor heartbeat: dropped_count since the
// PREVIOUS beat, cumulative dropped_total, and the rule's payload_consts
// (window_s mirrors the dedupe window). A zero-drop beat distinguishes
// healthy silence from breakage.
func (r *Rule) publishHeartbeat(now time.Time, t *consTwin) {
	st := r.cons
	if st == nil {
		return
	}
	beat := map[string]any{
		"type":          t.beatType,
		"dropped_count": st.dropped,
		"dropped_total": st.droppedTotal,
	}
	for k, v := range t.beatConsts {
		beat[k] = v
	}
	beat["ts"] = now.UnixMilli()
	payload, err := json.Marshal(beat)
	if err != nil {
		log.Printf("[MQTTEvents] rule=%s: heartbeat marshal failed: %v", r.ID(), err)
		return
	}
	st.dropped = 0
	st.beatAt = now.Add(t.beatEvery)
	r.stage(&consJob{rule: r, kind: "publish", topic: t.beatTopic, payload: payload, qos: t.beatQoS, retained: t.beatRetain})
}

// sweepDrops publishes an overdue noise heartbeat even without new traffic
// (docs rule 3: a missing heartbeat = pipeline broken, so beats fire on the
// clock, not only when drops arrive). Called on the receive-clock janitor.
func (r *Rule) sweepDrops(now time.Time, t *consTwin) {
	if r.cfg.Action.Type != "drop_and_count" || t.beatEvery == 0 {
		return
	}
	if r.cons == nil {
		r.cons = &consState{} // zero-traffic rule: create state so the beat clock arms
	}
	// Zero-traffic rules arm due immediately on the first sweep: a beat that
	// has NEVER fired is itself the breakage signal (docs rule 3), so the
	// first sweep publishes a zero-drop heartbeat rather than waiting a full
	// interval.
	if r.cons.beatAt.IsZero() {
		r.cons.beatAt = now
	}
	if !now.Before(r.cons.beatAt) {
		r.publishHeartbeat(now, t)
	}
}

// --- deterministic ids -----------------------------------------------------------

// deterministicID derives a UUIDv5 id (RFC 4122, URL namespace — the same
// scheme as the harness reference) from topic + canonical payload, so
// re-publishing identical window content is idempotent (BB8 convention:
// unsolicited publisher, consumers dedupe by id).
func deterministicID(topic string, payload map[string]any) string {
	name := topic + pythonStyleJSON(payload)
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(name)).String()
}

// pythonStyleJSON renders a payload the way Python's json.dumps(sort_keys)
// does — sorted keys, ", "/": " separators, integral floats without a
// decimal point — so identical content hashes identically across the
// reference actor and the engine. (Non-ASCII escaping differs; ids only
// need internal determinism, which this preserves.)
func pythonStyleJSON(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pythonString(k) + ": " + pythonStyleJSON(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pythonStyleJSON(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case string:
		return pythonString(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func pythonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- jobs -----------------------------------------------------------------------

// consJob is one consolidation output handed to the worker goroutine through
// the same bounded channel as v1 actions.
type consJob struct {
	rule     *Rule
	kind     string // publish | alert | notice
	topic    string
	payload  []byte
	qos      byte
	retained bool
	notice   *Notice
}

// stage appends a job to the rule's pending buffer (evaluation runs under
// evalMu; the engine drains pending into the worker channel).
func (r *Rule) stage(j *consJob) {
	r.pending = append(r.pending, j)
}
