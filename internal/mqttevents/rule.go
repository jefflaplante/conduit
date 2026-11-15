package mqttevents

import (
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

func (r *Rule) markFired(now time.Time) {
	r.hasFired = true
	r.lastFired = now
	r.since = time.Time{}
}
