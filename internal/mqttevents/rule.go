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

	// state_change baseline
	hasBaseline bool
	lastPayload string

	// threshold: start of the current in-region run (zero = not in region)
	since time.Time

	hasFired  bool
	lastFired time.Time
}

func newRule(rc config.MQTTEventRule) (*Rule, error) {
	tmpl, err := config.ParseMQTTEventTemplate(rc.ID, rc.Action.PromptTemplate)
	if err != nil {
		return nil, err
	}
	return &Rule{
		cfg:      rc,
		tmpl:     tmpl,
		cooldown: time.Duration(rc.CooldownS) * time.Second,
		duration: time.Duration(rc.Condition.DurationS) * time.Second,
	}, nil
}

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
