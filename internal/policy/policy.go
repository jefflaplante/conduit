// Package policy is Conduit's tool action policy (conduit-25lt.2, design
// "Conduit Tool Permission Design"). Tools with outward effects classify
// each call into dotted action classes ("message.group",
// "email.send.owner"); one table the agent cannot edit maps classes to
// allow, ask or deny; and the turn's origin applies on top: a turn nobody
// can answer (cron, heartbeat, sub-agent, MCP, automation token) turns ask
// into deny. Every decision is recorded.
//
// Phase 1 runs in shadow mode only: decisions are recorded, nothing is
// blocked. Enforcement is phase 2 (conduit-25lt.3).
//
// The package depends only on internal/approval (a stdlib-only leaf) so
// tools, skills and the gateway can all import it without cycles.
package policy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"conduit/internal/approval"
)

// Decision is what the policy says about one action.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// ParseDecision validates a decision name (case-insensitive).
func ParseDecision(s string) (Decision, error) {
	switch d := Decision(strings.ToLower(strings.TrimSpace(s))); d {
	case Allow, Ask, Deny:
		return d, nil
	default:
		return "", fmt.Errorf("invalid decision %q (want allow, ask or deny)", s)
	}
}

// Modes. Only off and shadow exist until phase 2 adds enforcement.
const (
	ModeOff    = "off"
	ModeShadow = "shadow"
)

// Classes with special handling. Tools report message targets as
// MessageDM or MessageGroup; the engine turns a DM to one of the owner's
// own targets into MessageSelf.
const (
	MessageSelf    = "message.self"
	MessageDM      = "message.dm"
	MessageGroup   = "message.group"
	EmailSendAgent = "email.send.agent"
	EmailSendOwner = "email.send.owner"
)

// Action is one outward effect of a tool call.
type Action struct {
	// Class is the dotted action class, e.g. "message.group".
	Class string
	// Target is who or what the action reaches, normalised by the tool:
	// "telegram:123" for a chat, a lower-cased address for email.
	Target string
}

// Classifier is implemented by tools whose calls have outward effects. It
// returns nil for calls with none (reads, status checks).
type Classifier interface {
	ClassifyActions(ctx context.Context, args map[string]interface{}) []Action
}

// Config is the resolved policy table.
type Config struct {
	Mode    string
	Default Decision
	// Classes maps a class, or a class prefix such as "email.send", to a
	// decision. The most specific entry wins.
	Classes map[string]Decision
	// Recipients lists targets an ask-class action may reach without
	// asking, per class or class prefix. Matched case-insensitively.
	Recipients map[string][]string
	// OwnerTargets are the owner's own chat targets ("telegram:123"); a
	// message to one of them is message.self.
	OwnerTargets []string
}

// Result is the evaluation of one action.
type Result struct {
	Action
	// Policy is the table's answer before the origin rule.
	Policy Decision
	// Decision is the effective answer after the origin rule.
	Decision Decision
	// Rule names what decided it: "class:<key>", "default",
	// "recipient_listed", plus "+non_interactive" when the origin rule
	// turned ask into deny.
	Rule string
}

// Record is one audit entry: a tool call's action and its decision.
type Record struct {
	Time        time.Time `json:"time"`
	Mode        string    `json:"mode"`
	Tool        string    `json:"tool"`
	Class       string    `json:"class"`
	Target      string    `json:"target,omitempty"`
	Policy      Decision  `json:"policy"`
	Decision    Decision  `json:"decision"`
	Rule        string    `json:"rule"`
	Origin      string    `json:"origin"`
	Interactive bool      `json:"interactive"`
	SessionKey  string    `json:"session_key,omitempty"`
	ChannelID   string    `json:"channel_id,omitempty"`
	Purpose     string    `json:"purpose,omitempty"`
}

// Recorder persists decision records.
type Recorder interface {
	Record(Record)
}

// Engine evaluates and records actions. A nil *Engine is valid and does
// nothing.
type Engine struct {
	cfg      Config
	owner    map[string]bool
	rec      Recorder
	keysDesc []string // class keys, longest first
	now      func() time.Time
}

// New builds an engine for cfg, recording to rec (may be nil).
func New(cfg Config, rec Recorder) *Engine {
	e := &Engine{cfg: cfg, owner: map[string]bool{}, rec: rec, now: time.Now}
	if e.cfg.Default == "" {
		e.cfg.Default = Allow
	}
	for _, t := range cfg.OwnerTargets {
		e.owner[strings.ToLower(strings.TrimSpace(t))] = true
	}
	for k := range cfg.Classes {
		e.keysDesc = append(e.keysDesc, k)
	}
	sort.Slice(e.keysDesc, func(i, j int) bool { return len(e.keysDesc[i]) > len(e.keysDesc[j]) })
	return e
}

// Mode reports the engine's mode ("off" for a nil engine).
func (e *Engine) Mode() string {
	if e == nil || e.cfg.Mode == "" {
		return ModeOff
	}
	return e.cfg.Mode
}

// Evaluate decides each action for the turn in ctx without recording.
func (e *Engine) Evaluate(ctx context.Context, actions []Action) []Result {
	if e == nil || len(actions) == 0 {
		return nil
	}
	o, _ := approval.OriginFrom(ctx)
	out := make([]Result, 0, len(actions))
	for _, a := range actions {
		a = e.normalize(a)
		r := Result{Action: a}
		r.Policy, r.Rule = e.lookup(a)
		r.Decision = r.Policy
		if r.Decision == Ask && !o.Interactive {
			r.Decision = Deny
			r.Rule += "+non_interactive"
		}
		out = append(out, r)
	}
	return out
}

// Observe evaluates the actions of one tool call and records each
// decision. It is the shadow-mode entry point: it never blocks. purpose is
// the agent's stated reason, if it gave one.
func (e *Engine) Observe(ctx context.Context, tool, purpose string, actions []Action) []Result {
	if e.Mode() == ModeOff {
		return nil
	}
	results := e.Evaluate(ctx, actions)
	if e.rec == nil {
		return results
	}
	o, _ := approval.OriginFrom(ctx)
	source := o.Source
	if source == "" {
		source = "unknown"
	}
	for _, r := range results {
		e.rec.Record(Record{
			Time: e.now().UTC(), Mode: e.Mode(), Tool: tool,
			Class: r.Class, Target: r.Target,
			Policy: r.Policy, Decision: r.Decision, Rule: r.Rule,
			Origin: source, Interactive: o.Interactive,
			SessionKey: o.SessionKey, ChannelID: o.ChannelID,
			Purpose: clip(purpose, 300),
		})
	}
	return results
}

// normalize lower-cases the target and turns a DM to the owner into
// message.self.
func (e *Engine) normalize(a Action) Action {
	a.Target = strings.ToLower(strings.TrimSpace(a.Target))
	if a.Class == MessageDM && e.owner[a.Target] {
		a.Class = MessageSelf
	}
	return a
}

// lookup returns the table's decision for a and the rule that gave it.
func (e *Engine) lookup(a Action) (Decision, string) {
	d, rule := e.cfg.Default, "default"
	for _, k := range e.keysDesc {
		if a.Class == k || strings.HasPrefix(a.Class, k+".") {
			d, rule = e.cfg.Classes[k], "class:"+k
			break
		}
	}
	if d == Ask && a.Target != "" && e.listed(a) {
		return Allow, "recipient_listed"
	}
	return d, rule
}

// listed reports whether a's target is on the recipient list for its class
// or any prefix of it.
func (e *Engine) listed(a Action) bool {
	for key, targets := range e.cfg.Recipients {
		if a.Class != key && !strings.HasPrefix(a.Class, key+".") {
			continue
		}
		for _, t := range targets {
			if strings.EqualFold(strings.TrimSpace(t), a.Target) {
				return true
			}
		}
	}
	return false
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
