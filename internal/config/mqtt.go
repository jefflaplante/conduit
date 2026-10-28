package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode/utf8"
)

// MQTTConfig holds configuration for the optional MQTT event ingest service.
type MQTTConfig struct {
	Enabled         bool           `json:"enabled"`
	BrokerURL       string         `json:"broker_url" cfg:"env"`             // "tcp://192.168.1.10:1883"
	ClientID        string         `json:"client_id,omitempty"`              // default: unique "conduit-<host>-<rand>" per process
	Username        string         `json:"username,omitempty" cfg:"env"`     // ${MQTT_USERNAME}
	Password        string         `json:"password,omitempty" cfg:"env"`     // ${MQTT_PASSWORD}
	Topics          []string       `json:"topics"`                           // ["zigbee2mqtt/#"]
	QoS             int            `json:"qos,omitempty"`                    // 0-2, default 0
	BufferMaxAge    int            `json:"buffer_max_age_seconds,omitempty"` // default 3600
	BufferMaxEvents int            `json:"buffer_max_events,omitempty"`      // per topic, default 1000
	BufferMaxTopics int            `json:"buffer_max_topics,omitempty"`      // default 500
	PublishAllowed  bool           `json:"publish_allowed,omitempty"`        // default false (safety)
	TLS             *MQTTTLSConfig `json:"tls,omitempty"`

	// Approval: a publish asks the owner for "YES <code>" first unless
	// approval.required is false or an approval.exempt topic filter
	// matches (approval.go).
	Approval ToolApproval `json:"approval,omitempty"`

	// Events is the optional event rule engine (bd-39mx). nil or
	// enabled=false: nothing is constructed.
	Events *MQTTEventsConfig `json:"events,omitempty"`
}

// MQTTTLSConfig holds optional TLS settings for MQTT connections.
type MQTTTLSConfig struct {
	CACert     string `json:"ca_cert,omitempty"`
	ClientCert string `json:"client_cert,omitempty"`
	ClientKey  string `json:"client_key,omitempty"`
	Insecure   bool   `json:"insecure,omitempty"`
}

// DefaultMQTTConfig returns sensible defaults for MQTT configuration.
func DefaultMQTTConfig() MQTTConfig {
	return MQTTConfig{
		Enabled:         false,
		BufferMaxAge:    3600,
		BufferMaxEvents: 1000,
		BufferMaxTopics: 500,
	}
}

// Validate checks the MQTT configuration for errors.
func (m *MQTTConfig) Validate() error {
	if !m.Enabled {
		return nil
	}

	if m.BrokerURL == "" {
		return fmt.Errorf("mqtt: broker_url is required when enabled")
	}

	if len(m.Topics) == 0 {
		return fmt.Errorf("mqtt: at least one topic subscription is required")
	}

	if m.QoS < 0 || m.QoS > 2 {
		return fmt.Errorf("mqtt: qos must be 0, 1, or 2 (got %d)", m.QoS)
	}

	for _, f := range m.Approval.Exempt {
		if err := validateMQTTFilter(f); err != nil {
			return fmt.Errorf("mqtt.approval.exempt: %w", err)
		}
	}

	// Apply defaults for zero-valued fields that weren't set
	// ClientID stays empty here: mqtt.NewClient assigns a unique per-process
	// ID (EffectiveClientID) so two instances never kick each other off the
	// broker (conduit-31jg.40). An explicit client_id is used verbatim.
	if m.BufferMaxAge <= 0 {
		m.BufferMaxAge = 3600
	}
	if m.BufferMaxEvents <= 0 {
		m.BufferMaxEvents = 1000
	}
	if m.BufferMaxTopics <= 0 {
		m.BufferMaxTopics = 500
	}

	if m.Events != nil && m.Events.Enabled {
		if err := m.Events.Validate(); err != nil {
			return fmt.Errorf("mqtt.events: %w", err)
		}
		for _, r := range m.Events.Rules {
			if r.IsEnabled() && !m.TopicCovered(r.Topic) {
				log.Printf("[Config] WARNING: mqtt.events rule %q topic %q is not covered by any mqtt.topics subscription; the rule will never fire", r.ID, r.Topic)
			}
		}
	}

	return nil
}

// GenerateMQTTClientID returns a client ID unique to this process:
// "conduit-<host>-<8 hex>", at most 23 bytes so it fits MQTT 3.1.1's minimum
// broker limit. The old fixed default "conduit" made a second instance (or a
// restart racing the old session) disconnect the first (conduit-31jg.40).
func GenerateMQTTClientID() string {
	host, _ := os.Hostname()
	var b strings.Builder
	for _, r := range strings.ToLower(host) {
		if b.Len() == 6 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("conduit-%s-%d", b.String(), os.Getpid())
	}
	if b.Len() == 0 {
		return "conduit-" + hex.EncodeToString(rnd[:])
	}
	return "conduit-" + b.String() + "-" + hex.EncodeToString(rnd[:])
}

// EffectiveClientID returns the configured client_id, or a freshly generated
// unique one when unset. Call once per connection owner and keep the result.
func (m *MQTTConfig) EffectiveClientID() string {
	if m.ClientID != "" {
		return m.ClientID
	}
	return GenerateMQTTClientID()
}

// MQTTEventsConfig configures the MQTT event rule engine (bd-39mx): code-
// evaluated rules over inbound events that spawn a sub-agent, queue a notice
// for the heartbeat, or log.
type MQTTEventsConfig struct {
	Enabled             bool             `json:"enabled"`
	MaxConcurrentSpawns int              `json:"max_concurrent_spawns,omitempty"` // default 2
	GlobalBudget        *MQTTEventBudget `json:"global_budget,omitempty"`         // default 10 / 3600s
	PerRuleBudget       *MQTTEventBudget `json:"per_rule_budget,omitempty"`       // default 3 / 600s
	PayloadMaxBytes     int              `json:"payload_max_bytes,omitempty"`     // default 2048
	Rules               []MQTTEventRule  `json:"rules"`
}

// MQTTEventBudget is a sliding-window action budget.
type MQTTEventBudget struct {
	WindowS    int `json:"window_s"`
	MaxActions int `json:"max_actions"`
}

// MQTTEventRule is one owner-authored rule on an exact topic.
type MQTTEventRule struct {
	ID             string             `json:"id"`
	Topic          string             `json:"topic"` // exact topic, no wildcards
	Condition      MQTTEventCondition `json:"condition"`
	Action         MQTTEventAction    `json:"action"`
	CooldownS      int                `json:"cooldown_s,omitempty"`
	FireOnRetained bool               `json:"fire_on_retained,omitempty"`
	Enabled        *bool              `json:"enabled,omitempty"` // nil = enabled
}

// IsEnabled reports whether the rule is active (unset means enabled).
func (r MQTTEventRule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// MQTTEventCondition is evaluated in code on every event for the rule topic.
type MQTTEventCondition struct {
	Type      string  `json:"type"`            // state_change | threshold
	Field     string  `json:"field,omitempty"` // JSON key for object payloads
	Op        string  `json:"op,omitempty"`    // lt | lte | gt | gte | eq (threshold)
	Value     float64 `json:"value,omitempty"`
	DurationS int     `json:"duration_s,omitempty"` // threshold: must hold this long
}

// MQTTEventAction is what a fired rule does. Only spawn, queue and log
// exist; the engine's dispatch switch enforces that independently of this
// validation.
type MQTTEventAction struct {
	Type           string   `json:"type"`               // spawn | queue | log
	Priority       string   `json:"priority,omitempty"` // spawn only: high | medium | low
	Skills         []string `json:"skills,omitempty"`
	Model          string   `json:"model,omitempty"`
	TimeoutS       int      `json:"timeout_s,omitempty"` // spawn, default 600
	PromptTemplate string   `json:"prompt_template"`
}

const (
	defaultEventMaxConcurrentSpawns = 2
	defaultEventPerRuleWindowS      = 600
	defaultEventPerRuleMaxActions   = 3
	defaultEventGlobalWindowS       = 3600
	defaultEventGlobalMaxActions    = 10
	defaultEventPayloadMaxBytes     = 2048
	defaultEventSpawnTimeoutS       = 600
	defaultEventSpawnPriority       = "medium"

	// MinEventTemplateSubstance is the minimum length of a prompt template's
	// own text (rendered with blank event data): a template that gives the
	// spawned session nothing to do is rejected at load.
	MinEventTemplateSubstance = 40
)

var eventRuleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ApplyDefaults fills zero-valued engine settings. Validate calls it; the
// engine calls it again so a config built in code behaves the same.
func (e *MQTTEventsConfig) ApplyDefaults() {
	if e.MaxConcurrentSpawns <= 0 {
		e.MaxConcurrentSpawns = defaultEventMaxConcurrentSpawns
	}
	e.PerRuleBudget = budgetWithDefaults(e.PerRuleBudget, defaultEventPerRuleWindowS, defaultEventPerRuleMaxActions)
	e.GlobalBudget = budgetWithDefaults(e.GlobalBudget, defaultEventGlobalWindowS, defaultEventGlobalMaxActions)
	if e.PayloadMaxBytes <= 0 {
		e.PayloadMaxBytes = defaultEventPayloadMaxBytes
	}
	for i := range e.Rules {
		a := &e.Rules[i].Action
		if a.Type == "spawn" {
			if a.TimeoutS <= 0 {
				a.TimeoutS = defaultEventSpawnTimeoutS
			}
			if a.Priority == "" {
				a.Priority = defaultEventSpawnPriority
			}
		}
	}
}

func budgetWithDefaults(b *MQTTEventBudget, windowS, maxActions int) *MQTTEventBudget {
	out := MQTTEventBudget{}
	if b != nil {
		out = *b
	}
	if out.WindowS <= 0 {
		out.WindowS = windowS
	}
	if out.MaxActions <= 0 {
		out.MaxActions = maxActions
	}
	return &out
}

// Validate applies defaults and checks every rule, including compiling and
// test-rendering its prompt template.
func (e *MQTTEventsConfig) Validate() error {
	e.ApplyDefaults()
	seen := make(map[string]bool, len(e.Rules))
	for i := range e.Rules {
		r := &e.Rules[i]
		if !eventRuleIDPattern.MatchString(r.ID) {
			return fmt.Errorf("rules[%d]: id %q must be non-empty and match %s", i, r.ID, eventRuleIDPattern)
		}
		if seen[r.ID] {
			return fmt.Errorf("rules[%d]: duplicate id %q", i, r.ID)
		}
		seen[r.ID] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("rule %q: %w", r.ID, err)
		}
	}
	return nil
}

func (r *MQTTEventRule) validate() error {
	if r.Topic == "" {
		return fmt.Errorf("topic is required")
	}
	if strings.ContainsAny(r.Topic, "+#") {
		return fmt.Errorf("topic %q: wildcards (+, #) are not allowed in rule topics; use an exact topic", r.Topic)
	}
	if r.CooldownS < 0 {
		return fmt.Errorf("cooldown_s cannot be negative")
	}
	c := r.Condition
	switch c.Type {
	case "state_change":
	case "threshold":
		switch c.Op {
		case "lt", "lte", "gt", "gte", "eq":
		default:
			return fmt.Errorf("condition.op %q: must be one of lt, lte, gt, gte, eq", c.Op)
		}
	default:
		return fmt.Errorf("condition.type %q: must be state_change or threshold", c.Type)
	}
	if c.DurationS < 0 {
		return fmt.Errorf("condition.duration_s cannot be negative")
	}
	a := r.Action
	switch a.Type {
	case "spawn":
		switch a.Priority {
		case "high", "medium", "low":
		default:
			return fmt.Errorf("action.priority %q: must be high, medium or low", a.Priority)
		}
	case "queue", "log":
		if a.Priority != "" {
			return fmt.Errorf("action.priority is only valid with action.type spawn")
		}
	default:
		return fmt.Errorf("action.type %q: must be spawn, queue or log", a.Type)
	}
	if a.TimeoutS < 0 {
		return fmt.Errorf("action.timeout_s cannot be negative")
	}
	_, err := ParseMQTTEventTemplate(r.ID, a.PromptTemplate)
	return err
}

// ParseMQTTEventTemplate compiles a rule prompt template (text/template, no
// functions, unknown keys are errors) and rejects one whose own text has
// fewer than MinEventTemplateSubstance characters when rendered with blank
// event data.
func ParseMQTTEventTemplate(ruleID, src string) (*template.Template, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("action.prompt_template is required")
	}
	tmpl, err := template.New("mqtt-event-" + ruleID).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("action.prompt_template: %w", err)
	}
	if err := checkEventTemplateNodes(tmpl); err != nil {
		return nil, fmt.Errorf("action.prompt_template: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, MQTTEventTemplateVars("", "", "", 0, "", nil)); err != nil {
		return nil, fmt.Errorf("action.prompt_template: test render: %w", err)
	}
	substance := strings.Join(strings.Fields(b.String()), " ")
	if n := utf8.RuneCountInString(substance); n < MinEventTemplateSubstance {
		return nil, fmt.Errorf("action.prompt_template: only %d chars of substance (min %d); tell the session what to check", n, MinEventTemplateSubstance)
	}
	return tmpl, nil
}

// checkEventTemplateNodes refuses function calls and nested templates.
func checkEventTemplateNodes(tmpl *template.Template) error {
	if len(tmpl.Templates()) > 1 {
		return fmt.Errorf("define/block/template are not allowed")
	}
	var walk func(n parse.Node) error
	walk = func(n parse.Node) error {
		switch n := n.(type) {
		case nil:
			return nil
		case *parse.ListNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Nodes {
				if err := walk(c); err != nil {
					return err
				}
			}
		case *parse.ActionNode:
			return walk(n.Pipe)
		case *parse.IfNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.RangeNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.WithNode:
			return walkBranch(walk, &n.BranchNode)
		case *parse.PipeNode:
			if n == nil {
				return nil
			}
			for _, c := range n.Cmds {
				if err := walk(c); err != nil {
					return err
				}
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				if err := walk(a); err != nil {
					return err
				}
			}
		case *parse.ChainNode:
			return walk(n.Node)
		case *parse.IdentifierNode:
			return fmt.Errorf("functions are not allowed (%q)", n.Ident)
		case *parse.TemplateNode:
			return fmt.Errorf("nested templates are not allowed")
		}
		return nil
	}
	return walk(tmpl.Tree.Root)
}

func walkBranch(walk func(parse.Node) error, b *parse.BranchNode) error {
	if err := walk(b.Pipe); err != nil {
		return err
	}
	if err := walk(b.List); err != nil {
		return err
	}
	if b.ElseList != nil {
		return walk(b.ElseList)
	}
	return nil
}

// MQTTEventHistoryVar is one prior event exposed to a prompt template.
type MQTTEventHistoryVar struct {
	Timestamp string
	Payload   string
}

// MQTTEventTemplateVars is the prompt template data. Keys are offered in the
// documented snake_case form ({{.rule_id}}, {{.history}}) and as Go-style
// names ({{.RuleID}}, {{.History}}).
func MQTTEventTemplateVars(ruleID, topic, payload string, value float64, timestamp string, history []MQTTEventHistoryVar) map[string]any {
	hist := make([]map[string]any, len(history))
	for i, h := range history {
		hist[i] = map[string]any{
			"timestamp": h.Timestamp, "Timestamp": h.Timestamp,
			"payload": h.Payload, "Payload": h.Payload,
		}
	}
	return map[string]any{
		"rule_id": ruleID, "RuleID": ruleID,
		"topic": topic, "Topic": topic,
		"payload": payload, "Payload": payload,
		"value": value, "Value": value,
		"timestamp": timestamp, "Timestamp": timestamp,
		"history": hist, "History": hist,
	}
}

// TopicCovered reports whether topic is matched by any mqtt.topics filter.
func (m *MQTTConfig) TopicCovered(topic string) bool {
	for _, f := range m.Topics {
		if mqttTopicMatches(f, topic) {
			return true
		}
	}
	return false
}

// mqttTopicMatches implements MQTT filter matching (+ one level, # rest).
func mqttTopicMatches(filter, topic string) bool {
	fl := strings.Split(filter, "/")
	tl := strings.Split(topic, "/")
	for i, f := range fl {
		if f == "#" {
			return true
		}
		if i >= len(tl) {
			return false
		}
		if f != "+" && f != tl[i] {
			return false
		}
	}
	return len(fl) == len(tl)
}
