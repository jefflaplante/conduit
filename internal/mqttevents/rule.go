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

	// Armed-hours window (bd-q0ez), parsed once from ArmedFrom/ArmedTo.
	// hasArmed is false when the fields are unset and armed always returns
	// true; times are server-local by design (see armed).
	armedFrom, armedTo time.Time
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
		x.armedFrom, x.armedTo, x.hasArmed = from, to, true
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
// A window that spans midnight (from > to) matches two ranges on any given
// day: [day+from, next-day+to) and [prev-day+from, day+to).
func (r *Rule) armed(t time.Time) bool {
	if !r.hasArmed {
		return true
	}
	t = t.In(time.Local)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	// r.armedFrom/armedTo carry only a wall clock on Go's zero date; derive
	// their offset from the zero date's own midnight (small duration, no
	// overflow) and project onto today.
	zero := time.Date(0, 1, 1, 0, 0, 0, 0, time.Local)
	from := day.Add(r.armedFrom.Sub(zero))
	to := day.Add(r.armedTo.Sub(zero))
	if !r.armedTo.Before(r.armedFrom) { // plain same-day window: [from, to)
		return !t.Before(from) && t.Before(to)
	}
	// Midnight-spanning: inside iff now >= today's from OR now < today's to.
	nextTo := to.AddDate(0, 0, 1)
	prevFrom := from.AddDate(0, 0, -1)
	return (!t.Before(from) && t.Before(nextTo)) || (!t.Before(prevFrom) && t.Before(to))
}

func (r *Rule) markFired(now time.Time) {
	r.hasFired = true
	r.lastFired = now
	r.since = time.Time{}
}
