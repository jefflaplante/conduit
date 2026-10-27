package mqttevents

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"conduit/internal/mqtt"
)

// outcome is the result of evaluating one rule against one event.
type outcome int

const (
	noMatch outcome = iota
	fire
	swallowed // matched inside cooldown
)

// evaluate runs the rule's condition on ev and updates its state. It is pure
// in-memory bookkeeping (no IO) and uses the event timestamp as "now".
func (r *Rule) evaluate(ev mqtt.Event) (outcome, float64) {
	now := ev.Timestamp
	value, numeric := parseValue(ev.Payload, r.cfg.Condition.Field)
	if !numeric {
		value = math.NaN()
	}

	switch r.cfg.Condition.Type {
	case "state_change":
		payload := string(ev.Payload)
		if !r.hasBaseline { // no baseline yet: the first event never fires
			r.hasBaseline, r.lastPayload = true, payload
			return noMatch, value
		}
		changed := payload != r.lastPayload
		r.lastPayload = payload // baseline tracks every event, even in cooldown
		if !changed || (ev.Retained && !r.cfg.FireOnRetained) {
			return noMatch, value
		}
		if r.inCooldown(now) {
			return swallowed, value
		}
		r.markFired(now)
		return fire, value

	case "threshold":
		match := numeric && compare(r.cfg.Condition.Op, value, r.cfg.Condition.Value)
		// Cooldown first: no duration bookkeeping inside it, so the timer
		// starts clean once it ends.
		if r.inCooldown(now) {
			r.since = time.Time{}
			if match {
				return swallowed, value
			}
			return noMatch, value
		}
		if !match { // any out-of-region (or unparseable) reading resets
			r.since = time.Time{}
			return noMatch, value
		}
		if r.since.IsZero() {
			r.since = now
		}
		if now.Sub(r.since) < r.duration {
			return noMatch, value
		}
		if ev.Retained && !r.cfg.FireOnRetained {
			return noMatch, value
		}
		r.markFired(now)
		return fire, value
	}
	return noMatch, value
}

func compare(op string, v, threshold float64) bool {
	switch op {
	case "lt":
		return v < threshold
	case "lte":
		return v <= threshold
	case "gt":
		return v > threshold
	case "gte":
		return v >= threshold
	case "eq":
		return v == threshold
	}
	return false
}

// parseValue extracts a finite number from a scalar payload, or from the
// given key of a JSON object payload (number or numeric string).
func parseValue(payload []byte, field string) (float64, bool) {
	var s string
	if field != "" {
		var obj map[string]any
		if err := json.Unmarshal(payload, &obj); err != nil {
			return 0, false
		}
		switch v := obj[field].(type) {
		case float64:
			return v, !math.IsNaN(v) && !math.IsInf(v, 0)
		case string:
			s = v
		default:
			return 0, false
		}
	} else {
		s = string(payload)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
