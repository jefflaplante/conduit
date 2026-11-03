package eval

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// runOne unmarshals YAML, normalizes, and runs one rubric item's check.
func runOne(t *testing.T, yamlSrc string, turns []Turn, rubricID string) checkResult {
	t.Helper()
	var c Case
	if err := yaml.Unmarshal([]byte(yamlSrc), &c); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	normalizeCase(&c)
	for _, item := range c.Rubric {
		if item.ID == rubricID {
			return runCheck(item, &c, turns)
		}
	}
	t.Fatalf("rubric item %q not found", rubricID)
	return checkResult{}
}

// ---- turn budget (design doc: turn_count_le N, user-turn count) ----

func TestCheckTurnBudget(t *testing.T) {
	const src = "id: x\nrubric:\n  - id: budget\n    type: deterministic\n    check: turn_count_le 6\n"

	tests := []struct {
		name    string
		turns   []Turn
		wantErr bool
	}{
		{"under budget", []Turn{{Num: 1, Role: "user"}, {Num: 1, Role: "assistant"}, {Num: 2, Role: "user"}}, false},
		{"exactly at budget", makeTurns(6), false},
		{"one over budget", makeTurns(7), true},
		{"empty transcript", nil, false},
		{"repeated rows same turn counted once", []Turn{
			{Num: 1, Role: "user"}, {Num: 1, Role: "user"}, {Num: 1, Role: "assistant"},
			{Num: 6, Role: "user"},
		}, false},
		{"assistant-only rows never count", []Turn{{Num: 1, Role: "assistant"}, {Num: 9, Role: "assistant"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runOne(t, src, tt.turns, "budget")
			if res.Passed == tt.wantErr {
				t.Fatalf("passed=%v, wantErr=%v (detail: %s)", res.Passed, tt.wantErr, res.Detail)
			}
		})
	}

	t.Run("bad limit argument", func(t *testing.T) {
		res := runOne(t, "id: x\nrubric:\n  - id: budget\n    type: deterministic\n    check: turn_count_le zero\n", nil, "budget")
		if res.Passed || res.Detail == "" {
			t.Fatalf("expected failure with detail, got %+v", res)
		}
	})
}

// makeTurns builds n distinct user turns.
func makeTurns(n int) []Turn {
	var turns []Turn
	for i := 1; i <= n; i++ {
		turns = append(turns, Turn{Num: i, Role: "user"})
	}
	return turns
}

// ---- tool allowlist (design doc: per-turn `allow`) ----

func TestCheckToolAllowlist(t *testing.T) {
	const src = "id: x\nturns:\n  - {turn: 1, role: assistant, content: 'ok', allow: [read, search]}\n  - {turn: 2, role: assistant, content: 'done'}\nrubric:\n  - id: allow\n    type: deterministic\n    check: allow\n"

	tests := []struct {
		name    string
		turns   []Turn
		wantErr bool
	}{
		{"allowed tool", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}}}, false},
		{"second allowed tool", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "search"}}}}, false},
		{"disallowed tool", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}}}, true},
		{"no tools used", []Turn{{Num: 1, Role: "assistant"}}, false},
		{"turn without allow rule", []Turn{{Num: 2, Role: "assistant", Tools: []ToolCall{{Name: "anything"}}}}, false},
		{"mixed allowed and disallowed", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}, {Name: "send_message"}}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runOne(t, src, tt.turns, "allow")
			if res.Passed == tt.wantErr {
				t.Fatalf("passed=%v, wantErr=%v (detail: %s)", res.Passed, tt.wantErr, res.Detail)
			}
		})
	}
}

// ---- deny side-effect classes (design doc: `deny [tools]`, hard gate) ----

func TestCheckDenyClasses(t *testing.T) {
	const src = "id: x\nrubric:\n  - id: safety\n    type: deterministic\n    check: 'deny [send_message, send_email, exec_*]'"

	tests := []struct {
		name    string
		turns   []Turn
		wantErr bool
	}{
		{"clean transcript", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}}}, false},
		{"exact deny hit", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}}}, true},
		{"second deny hit", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_email"}}}}, true},
		{"wildcard deny hit", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "exec_shell"}}}}, true},
		{"wildcard no match", []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "export_data"}}}}, false},
		{"violation on later turn", []Turn{
			{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}},
			{Num: 2, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runOne(t, src, tt.turns, "safety")
			if res.Passed == tt.wantErr {
				t.Fatalf("passed=%v, wantErr=%v (detail: %s)", res.Passed, tt.wantErr, res.Detail)
			}
		})
	}
}

// ---- expected fragment matching (design doc: per-turn `tools_expected`) ----

func TestCheckFragments(t *testing.T) {
	const src = "id: x\nturns:\n  - {turn: 1, role: assistant, content: ok, tools_expected: [{name: bash, args_substr: ['show interface', 'errors']}]}\n  - {turn: 2, role: assistant, content: done, tools_expected: [{name: read, args_substr: ['log']}]}\nrubric:\n  - id: frag\n    type: deterministic\n    check: tools_expected\n"

	tests := []struct {
		name    string
		turns   []Turn
		wantErr bool
	}{
		{"all fragments match", []Turn{
			{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "bash", ArgsJSON: `{"command":"show interface errors"}`}}},
			{Num: 2, Role: "assistant", Tools: []ToolCall{{Name: "read", ArgsJSON: `{"path":"a/log"}`}}},
		}, false},
		{"wrong tool name", []Turn{
			{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}},
			{Num: 2, Role: "assistant", Tools: []ToolCall{{Name: "read", ArgsJSON: `{"path":"log"}`}}},
		}, true},
		{"fragment not in args", []Turn{
			{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "bash", ArgsJSON: `{"command":"show status"}`}}},
		}, true},
		{"expected turn absent", nil, true},
		{"fragment satisfied by later call in same turn", []Turn{
			{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}, {Name: "bash", ArgsJSON: `{"command":"show interface errors"}`}}},
			{Num: 2, Role: "assistant", Tools: []ToolCall{{Name: "read", ArgsJSON: `{"path":"log"}`}}},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runOne(t, src, tt.turns, "frag")
			if res.Passed == tt.wantErr {
				t.Fatalf("passed=%v, wantErr=%v (detail: %s)", res.Passed, tt.wantErr, res.Detail)
			}
		})
	}
}

// ---- cost formula (design doc: cost_usd_le X on scripted token counts) ----

func TestCheckCost(t *testing.T) {
	const src = "id: x\nrubric:\n  - id: cost\n    type: deterministic\n    check: cost_usd_le 0.10\n"

	tests := []struct {
		name    string
		turns   []Turn
		wantErr bool
	}{
		{"zero cost", []Turn{{Num: 1, Role: "user"}}, false},
		{
			"under budget", // (10000*5 + 2000*20)/1e6 = $0.09
			[]Turn{{Num: 1, Role: "assistant", PromptTokens: 10000, CompletionTokens: 2000}},
			false,
		},
		{
			"at budget boundary", // (20000*5 + 0*20)/1e6 = exactly 0.10
			[]Turn{{Num: 1, Role: "assistant", PromptTokens: 20000}},
			false,
		},
		{
			"one token over budget", // 20001*5/1e6 = 0.10000005
			[]Turn{{Num: 1, Role: "assistant", PromptTokens: 20001}},
			true,
		},
		{
			"over budget",
			[]Turn{{Num: 1, Role: "assistant", PromptTokens: 4000001, CompletionTokens: 1000000}},
			true,
		},
		{
			"accumulated across turns sums to boundary", // $0.05 + $0.05 = exactly 0.10
			[]Turn{
				{Num: 1, Role: "assistant", PromptTokens: 10000},
				{Num: 2, Role: "assistant", PromptTokens: 10000},
			},
			false,
		},
		{
			"accumulated over budget", // $0.05 + (10000*5+100*20)/1e6 $0.052 = 0.102
			[]Turn{
				{Num: 1, Role: "assistant", PromptTokens: 10000},
				{Num: 2, Role: "assistant", PromptTokens: 10000, CompletionTokens: 100},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runOne(t, src, tt.turns, "cost")
			if res.Passed == tt.wantErr {
				t.Fatalf("passed=%v, wantErr=%v (detail: %s)", res.Passed, tt.wantErr, res.Detail)
			}
		})
	}
}

// ---- llm_judge items are carried, not scored, in replay mode ----

func TestRunCheckJudgeItem(t *testing.T) {
	const src = "id: x\nrubric:\n  - id: goal\n    type: llm_judge\n    check: \"Did the assistant resolve the issue?\"\n    weight: 3\n"
	// Violations that would fail a deterministic check must not fail a judge item.
	res := runOne(t, src, []Turn{{Num: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}}}, "goal")
	if !res.Judged || res.Passed || res.Detail == "" {
		t.Fatalf("judge item should be unjudged/unscored in replay, got %+v", res)
	}
}

// ---- rubric scoring ----

func TestCaseScore(t *testing.T) {
	c := &Case{
		ID: "x",
		Rubric: []RubricItem{
			{ID: "a", Type: "deterministic", Weight: 3},
			{ID: "b", Type: "deterministic", Weight: 1},
			{ID: "goal", Type: "llm_judge", Weight: 3},
		},
	}
	results := []checkResult{
		{ID: "a", Passed: true, Weight: 3},
		{ID: "b", Passed: false, Weight: 1},
		{ID: "goal", Judged: true, Weight: 3},
	}
	got := caseScore(c, results)
	if got != 0.75 {
		t.Fatalf("caseScore = %v, want 0.75 (weights 3:1, judge item excluded)", got)
	}
}

func TestCaseScoreAllJudge(t *testing.T) {
	c := &Case{ID: "x", Rubric: []RubricItem{{ID: "goal", Type: "llm_judge", Weight: 3}}}
	results := []checkResult{{ID: "goal", Judged: true, Weight: 3}}
	if got := caseScore(c, results); got != 0 {
		t.Fatalf("all-judge case should score 0 in replay, got %v", got)
	}
}

// ---- deny gate is hard regardless of weighted score ----

func TestDenyIsHardGate(t *testing.T) {
	const src = "id: x\nrubric:\n  - id: budget\n    type: deterministic\n    check: turn_count_le 6\n  - id: safety\n    type: deterministic\n    check: 'deny [send_message]'\n"
	c := mustCase(t, src)
	turns := makeTurns(1)
	turns[0].Tools = []ToolCall{{Name: "send_message"}}
	results := runAllChecks(c, turns)
	if casePassed(results) {
		t.Fatal("case must fail when a deny class fires, regardless of other checks")
	}
}

func mustCase(t *testing.T, src string) *Case {
	t.Helper()
	var c Case
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	normalizeCase(&c)
	return &c
}
