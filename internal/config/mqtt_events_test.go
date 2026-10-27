package config

import (
	"strings"
	"testing"
)

const testEventTemplate = "Battery state of charge is {{.value}}% on {{.topic}} at {{.timestamp}}. Check the discharge trend and recommend action."

func baseMQTTWithEvents(ev *MQTTEventsConfig) MQTTConfig {
	return MQTTConfig{
		Enabled:   true,
		BrokerURL: "tcp://broker.example:1883",
		Topics:    []string{"sensors/#"},
		Events:    ev,
	}
}

func validEventRule() MQTTEventRule {
	return MQTTEventRule{
		ID:    "battery-low",
		Topic: "sensors/total/battery/state",
		Condition: MQTTEventCondition{
			Type: "threshold", Op: "lt", Value: 20, DurationS: 600,
		},
		Action: MQTTEventAction{
			Type: "spawn", Priority: "high", PromptTemplate: testEventTemplate,
		},
		CooldownS: 3600,
	}
}

func TestMQTTEvents_NilEventsIsNoop(t *testing.T) {
	m := baseMQTTWithEvents(nil)
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.Events != nil {
		t.Fatalf("Validate must not materialize an events block, got %+v", m.Events)
	}
}

func TestMQTTEvents_DisabledSkipsValidation(t *testing.T) {
	bad := validEventRule()
	bad.Topic = "sensors/#"
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: false, Rules: []MQTTEventRule{bad}})
	if err := m.Validate(); err != nil {
		t.Fatalf("disabled events block must not be validated: %v", err)
	}
}

func TestMQTTEvents_DefaultsApplied(t *testing.T) {
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{validEventRule()}})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	ev := m.Events
	if ev.MaxConcurrentSpawns != 2 {
		t.Errorf("max_concurrent_spawns = %d, want 2", ev.MaxConcurrentSpawns)
	}
	if ev.PerRuleBudget == nil || ev.PerRuleBudget.MaxActions != 3 || ev.PerRuleBudget.WindowS != 600 {
		t.Errorf("per_rule_budget = %+v, want 3/600s", ev.PerRuleBudget)
	}
	if ev.GlobalBudget == nil || ev.GlobalBudget.MaxActions != 10 || ev.GlobalBudget.WindowS != 3600 {
		t.Errorf("global_budget = %+v, want 10/3600s", ev.GlobalBudget)
	}
	if ev.PayloadMaxBytes != 2048 {
		t.Errorf("payload_max_bytes = %d, want 2048", ev.PayloadMaxBytes)
	}
	if got := ev.Rules[0].Action.TimeoutS; got <= 0 {
		t.Errorf("spawn timeout_s default not applied, got %d", got)
	}
}

func TestMQTTEvents_PartialBudgetDefaults(t *testing.T) {
	m := baseMQTTWithEvents(&MQTTEventsConfig{
		Enabled:       true,
		PerRuleBudget: &MQTTEventBudget{MaxActions: 5},
		GlobalBudget:  &MQTTEventBudget{WindowS: 60},
		Rules:         []MQTTEventRule{validEventRule()},
	})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if b := m.Events.PerRuleBudget; b.MaxActions != 5 || b.WindowS != 600 {
		t.Errorf("per_rule_budget = %+v, want 5/600s", b)
	}
	if b := m.Events.GlobalBudget; b.MaxActions != 10 || b.WindowS != 60 {
		t.Errorf("global_budget = %+v, want 10/60s", b)
	}
}

func TestMQTTEvents_RejectsWildcardTopics(t *testing.T) {
	for _, topic := range []string{"sensors/#", "sensors/+/battery/state", "#", "+"} {
		r := validEventRule()
		r.Topic = topic
		m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
		err := m.Validate()
		if err == nil || !strings.Contains(err.Error(), "wildcard") {
			t.Errorf("topic %q: want wildcard rejection, got %v", topic, err)
		}
	}
}

func TestMQTTEvents_TemplateValidation(t *testing.T) {
	cases := []struct {
		name, tmpl, wantErr string
	}{
		{"too short", "bell rang {{.payload}}", "substance"},
		{"only fields", "{{.rule_id}} {{.topic}} {{.payload}} {{.value}} {{.timestamp}}", "substance"},
		{"empty", "", "prompt_template"},
		{"parse error", "Battery state of charge {{.value is broken and so is this long sentence", "template"},
		{"unknown field", "Battery state of charge {{.nosuch}} — check the discharge trend and recommend.", "template"},
		{"function call refused", "Battery state of charge {{printf \"%v\" .value}} — check the discharge trend and act.", "template"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validEventRule()
			r.Action.PromptTemplate = tc.tmpl
			m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestMQTTEvents_TemplateAcceptsBothKeyStyles(t *testing.T) {
	r := validEventRule()
	r.Action.PromptTemplate = "Rule {{.RuleID}} saw {{.Value}} on {{.Topic}}; history:{{range .history}} {{.payload}}@{{.timestamp}}{{end}}. Diagnose the trend."
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestMQTTEvents_ConditionEnums(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*MQTTEventRule)
		want string
	}{
		{"bad type", func(r *MQTTEventRule) { r.Condition.Type = "regex" }, "condition.type"},
		{"bad op", func(r *MQTTEventRule) { r.Condition.Op = "ne" }, "condition.op"},
		{"missing op", func(r *MQTTEventRule) { r.Condition.Op = "" }, "condition.op"},
		{"negative duration", func(r *MQTTEventRule) { r.Condition.DurationS = -1 }, "duration_s"},
		{"negative cooldown", func(r *MQTTEventRule) { r.CooldownS = -1 }, "cooldown_s"},
		{"bad action", func(r *MQTTEventRule) { r.Action.Type = "publish" }, "action.type"},
		{"bad priority", func(r *MQTTEventRule) { r.Action.Priority = "urgent" }, "priority"},
		{"priority without spawn", func(r *MQTTEventRule) { r.Action.Type = "queue" }, "priority"},
		{"empty id", func(r *MQTTEventRule) { r.ID = "" }, "id"},
		{"bad id chars", func(r *MQTTEventRule) { r.ID = "has space" }, "id"},
		{"empty topic", func(r *MQTTEventRule) { r.Topic = "" }, "topic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validEventRule()
			tc.mut(&r)
			m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
			err := m.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestMQTTEvents_StateChangeNeedsNoOp(t *testing.T) {
	r := validEventRule()
	r.Condition = MQTTEventCondition{Type: "state_change"}
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestMQTTEvents_DuplicateRuleIDs(t *testing.T) {
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{validEventRule(), validEventRule()}})
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate id error, got %v", err)
	}
}

func TestMQTTEvents_SpawnDefaultPriority(t *testing.T) {
	r := validEventRule()
	r.Action.Priority = ""
	m := baseMQTTWithEvents(&MQTTEventsConfig{Enabled: true, Rules: []MQTTEventRule{r}})
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := m.Events.Rules[0].Action.Priority; got != "medium" {
		t.Fatalf("default spawn priority = %q, want medium", got)
	}
}

func TestMQTTTopicCovered(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"sensors/#", "sensors/total/battery/state", true},
		{"sensors/+/battery/state", "sensors/total/battery/state", true},
		{"sensors/+/battery", "sensors/total/battery/state", false},
		{"#", "anything/at/all", true},
		{"sensors/total/battery/state", "sensors/total/battery/state", true},
		{"other/#", "sensors/total/battery/state", false},
		{"sensors/#", "sensors", true},
	}
	for _, tc := range cases {
		if got := mqttTopicMatches(tc.filter, tc.topic); got != tc.want {
			t.Errorf("mqttTopicMatches(%q, %q) = %v, want %v", tc.filter, tc.topic, got, tc.want)
		}
	}
	m := MQTTConfig{Topics: []string{"other/#"}}
	if m.TopicCovered("sensors/x") {
		t.Errorf("TopicCovered: want false")
	}
}

func TestMQTTEventRule_IsEnabledDefaultsTrue(t *testing.T) {
	r := validEventRule()
	if !r.IsEnabled() {
		t.Fatal("nil enabled must mean enabled")
	}
	f := false
	r.Enabled = &f
	if r.IsEnabled() {
		t.Fatal("explicit false must disable")
	}
}
