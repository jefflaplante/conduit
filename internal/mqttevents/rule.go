package mqttevents

import (
	"fmt"
	"text/template"
	"time"

	"conduit/internal/config"
)

// Rule is a compiled config.MQTTEventRule plus its in-memory evaluation
// state. State is only touched by Engine.HandleEvent (under evalMu).
type Rule struct {
	cfg      config.MQTTEventRule
	tmpl     *template.Template
	cooldown time.Duration
	duration time.Duration

	// Armed-hours window (bd-q0ez), parsed once from ArmedFrom/ArmedTo as
	// minutes since local midnight. hasArmed is false when the fields are
	// unset and armed always returns true; times are server-local by design
	// (see armed).
	armedFrom, armedTo int
	hasArmed           bool

	// bd-e9o8 phase 2: consolidation knobs compiled once, state, and output
	// jobs staged during evaluation (drained by the engine into the worker
	// channel right after evaluation).
	twin    *consTwin
	cons    *consState
	pending []*consJob
	eng     *Engine // stats back-pointer, set by NewEngine

	// state_change baseline
	hasBaseline bool
	lastPayload string

	// threshold: start of the current in-region run (zero = not in region)
	since time.Time

	hasFired  bool
	lastFired time.Time
}

func newRule(rc config.MQTTEventRule) (*Rule, error) {
	var tmpl *template.Template
	var err error
	if rc.Action.Type == "spawn" || rc.Action.Type == "queue" || rEqLog(rc.Action.Type) {
		tmpl, err = config.ParseMQTTEventTemplate(rc.ID, rc.Action.PromptTemplate)
		if err != nil {
			return nil, err
		}
	}
	x := &Rule{
		cfg:      rc,
		tmpl:     tmpl,
		cooldown: time.Duration(rc.CooldownS) * time.Second,
		duration: time.Duration(rc.Condition.DurationS) * time.Second,
	}
	// Armed window (bd-q0ez): parse once at compile time; validation has
	// already confirmed both fields parse, so an error here is defensive.
	if rc.ArmedFrom != "" {
		from, err := time.Parse(config.ArmedHoursLayout, rc.ArmedFrom)
		if err != nil {
			return nil, fmt.Errorf("rule %q: armed_from: %w", rc.ID, err)
		}
		to, err := time.Parse(config.ArmedHoursLayout, rc.ArmedTo)
		if err != nil {
			return nil, fmt.Errorf("rule %q: armed_to: %w", rc.ID, err)
		}
		x.armedFrom, x.armedTo, x.hasArmed = minuteOfDay(from), minuteOfDay(to), true
	}
	// Only a consolidation CONDITION gets a twin; a v1 rule with one would
	// never reach the v1 evaluation path (HandleEvent branches on twin),
	// and a phase-2 action under a v1 condition stays a dispatch-time
	// refusal (the in-code verb allowlist — test TestUnknownActionRefusedInCode).
	switch rc.Condition.Type {
	case "window_buffer", "attrs_any", "catch_all":
		x.twin = newConsTwin(&rc)
	}
	return x, nil
}

func rEqLog(t string) bool { return t == "log" }

// ID returns the rule id.
func (r *Rule) ID() string { return r.cfg.ID }

// Label is the session label of sub-agents spawned by this rule.
func (r *Rule) Label() string { return "mqtt-" + r.cfg.ID }

func (r *Rule) inCooldown(now time.Time) bool {
	return r.hasFired && r.cooldown > 0 && now.Before(r.lastFired.Add(r.cooldown))
}

// armed reports whether the rule is armed at local wall-clock time t. The
// window is interpreted in SERVER-LOCAL time on purpose (bd-q0ez): the
// owner's rules target local-day patterns (overnight EV charging) and the
// deployment host never leaves one timezone. Unarmed rules return true.
//
// The check compares wall-clock minutes of day, so it is independent of the
// zone's offset and of DST transitions: [from, to) for a same-day window,
// and "at or after from, or before to" for one that spans midnight
// (from > to).
func (r *Rule) armed(t time.Time) bool {
	if !r.hasArmed {
		return true
	}
	m := minuteOfDay(t.In(time.Local))
	if r.armedFrom < r.armedTo {
		return m >= r.armedFrom && m < r.armedTo
	}
	return m >= r.armedFrom || m < r.armedTo
}

// disarmed is called for every event skipped by the armed-hours gate. It
// clears the threshold run so a run started before the window closed cannot
// complete its duration on the first event of the next window (bd-q0ez:
// no duration accumulation while disarmed).
func (r *Rule) disarmed() {
	r.since = time.Time{}
}

func minuteOfDay(t time.Time) int { return t.Hour()*60 + t.Minute() }

func (r *Rule) markFired(now time.Time) {
	r.hasFired = true
	r.lastFired = now
	r.since = time.Time{}
}
