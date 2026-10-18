package eval

import (
	"testing"
)

func TestIsDenied(t *testing.T) {
	tests := []struct {
		name string
		tool string
		deny []string
		want bool
	}{
		{"send class matches send_message", "send_message", []string{"send"}, true},
		{"send class matches email_send", "email_send", []string{"send"}, true},
		{"send class matches notify_push", "notify_push", []string{"send"}, true},
		{"read not denied by send", "read", []string{"send"}, false},
		{"literal name denied", "bash", []string{"bash"}, true},
		{"exec class matches bash", "bash", []string{"exec"}, true},
		{"exec class matches shell", "shell_tool", []string{"exec"}, true},
		{"exec class matches exec_cmd", "exec_cmd", []string{"exec"}, true},
		{"query not denied by exec", "query", []string{"exec"}, false},
		{"write class matches delete_file", "delete_file", []string{"write"}, true},
		{"network class matches web_search", "web_search", []string{"network"}, true},
		{"empty deny denies nothing", "send_message", nil, false},
		{"empty entry skipped", "send_message", []string{"", "send"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDenied(tt.tool, tt.deny); got != tt.want {
				t.Errorf("IsDenied(%q, %v) = %v, want %v", tt.tool, tt.deny, got, tt.want)
			}
		})
	}
}

func TestCheckTurnBudget(t *testing.T) {
	mk := func(budget int, maxTurn int) *Case {
		return &Case{
			TurnBudget: budget,
			Turns: []Turn{
				{Turn: 1, Role: "user"},
				{Turn: maxTurn, Role: "assistant"},
			},
		}
	}
	tests := []struct {
		name    string
		budget  int
		maxTurn int
		wantVio bool
	}{
		{"within budget", 8, 6, false},
		{"exactly at budget", 6, 6, false},
		{"over budget", 4, 6, true},
		{"zero disables check", 0, 99, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := CheckTurnBudget(mk(tt.budget, tt.maxTurn))
			if (v != nil) != tt.wantVio {
				t.Fatalf("violation = %v, want %v", v, tt.wantVio)
			}
			if v != nil && v.Check != "turn_budget" {
				t.Errorf("check = %q", v.Check)
			}
		})
	}
}

func TestCheckToolAllowlist(t *testing.T) {
	tests := []struct {
		name  string
		c     *Case
		wantN int
	}{
		{
			name: "clean",
			c: &Case{Turns: []Turn{
				{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}, Allow: []string{"read", "query"}},
			}},
			wantN: 0,
		},
		{
			name: "one violation",
			c: &Case{Turns: []Turn{
				{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "bash"}}, Allow: []string{"read"}},
			}},
			wantN: 1,
		},
		{
			name: "lone unallowlisted turn without any policy skips check",
			c: &Case{Turns: []Turn{
				{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "bash"}}},
			}},
			wantN: 0,
		},
		{
			name: "unallowlisted tool turn inside policy-bearing transcript flags",
			c: &Case{Turns: []Turn{
				{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}, Allow: []string{"read"}},
				{Turn: 2, Role: "assistant", Tools: []ToolCall{{Name: "bash"}}},
			}},
			wantN: 1,
		},
		{
			name: "user turns with tools ignored",
			c: &Case{Turns: []Turn{
				{Turn: 1, Role: "user", Tools: []ToolCall{{Name: "bash"}}},
			}},
			wantN: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckToolAllowlist(tt.c)
			if len(got) != tt.wantN {
				t.Fatalf("got %d violations (%v), want %d", len(got), got, tt.wantN)
			}
		})
	}
}

func TestCheckDenyClasses(t *testing.T) {
	tests := []struct {
		name  string
		c     *Case
		wantN int
	}{
		{
			name: "clean",
			c: &Case{
				DenyClasses: []string{"send"},
				Turns:       []Turn{{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "read"}}}},
			},
			wantN: 0,
		},
		{
			name: "one deny hit",
			c: &Case{
				DenyClasses: []string{"send"},
				Turns:       []Turn{{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}}},
			},
			wantN: 1,
		},
		{
			name: "no deny classes declared",
			c: &Case{
				Turns: []Turn{{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}}},
			},
			wantN: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckDenyClasses(tt.c)
			if len(got) != tt.wantN {
				t.Fatalf("got %d violations (%v), want %d", len(got), got, tt.wantN)
			}
			if tt.wantN > 0 && got[0].Check != "deny_class" {
				t.Errorf("check = %q", got[0].Check)
			}
		})
	}
}

func TestCheckFragments(t *testing.T) {
	tests := []struct {
		name      string
		fragments []string
		assistant string
		wantScore float64
		wantN     int
	}{
		{"all present", []string{"crc", "counters"}, "check the CRC counters", 1.0, 0},
		{"case insensitive", []string{"CRC ERRORS"}, "see the crc errors log", 1.0, 0},
		{"one missing", []string{"crc", "logs"}, "check the crc counters", 0.5, 1},
		{"none declared", nil, "anything", 1.0, 0},
		{"only assistant turns searched", []string{"secret"}, "a perfectly innocent reply", 0.0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Case{
				ExpectedFragments: tt.fragments,
				Turns: []Turn{
					{Turn: 1, Role: "user", Content: "user said secret"},
					{Turn: 1, Role: "assistant", Content: tt.assistant},
				},
			}
			score, vios := CheckFragments(c)
			if score != tt.wantScore {
				t.Errorf("score = %v, want %v", score, tt.wantScore)
			}
			if len(vios) != tt.wantN {
				t.Errorf("violations = %d, want %d (%v)", len(vios), tt.wantN, vios)
			}
		})
	}
}

func TestCheckCostBudget(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		prompt     int
		completion int
		budget     float64
		wantOver   bool
		wantPriced bool
	}{
		{
			name:  "real model within budget",
			model: "gpt-4o", prompt: 2000, completion: 400, budget: 0.01,
			wantOver: false, wantPriced: true,
		},
		{
			name:  "real model over budget",
			model: "gpt-4o", prompt: 20000, completion: 4000, budget: 0.01,
			wantOver: true, wantPriced: true,
		},
		{
			name:  "synthetic model falls back",
			model: "synthetic-model-a", prompt: 1000, completion: 100, budget: 0.05,
			wantOver: false, wantPriced: false,
		},
		{
			name:  "no budget never over",
			model: "gpt-4o", prompt: 20000, completion: 4000, budget: 0,
			wantOver: false, wantPriced: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Case{
				CostBudgetUSD: tt.budget,
				Cost:          CostInputs{Model: tt.model, PromptTokens: tt.prompt, CompletionTokens: tt.completion},
			}
			cost, priced, vio := CheckCostBudget(c, nil)
			if priced != tt.wantPriced {
				t.Errorf("priced = %v, want %v", priced, tt.wantPriced)
			}
			if (vio != nil) != tt.wantOver {
				t.Fatalf("violation = %v, want over=%v", vio, tt.wantOver)
			}
			if vio != nil && vio.Check != "cost_budget" {
				t.Errorf("check = %q", vio.Check)
			}
			if tt.model == "gpt-4o" && tt.prompt == 2000 {
				// gpt-4o: $2.50/M input, $10/M output → 2000×2.5 + 400×10 = $0.009.
				if cost < 0.0089 || cost > 0.0091 {
					t.Errorf("gpt-4o 2000/400 cost = %v, want ~0.009", cost)
				}
			}
		})
	}
}

func TestCostDimension(t *testing.T) {
	c := &Case{CostBudgetUSD: 0.05}
	if got := CostDimension(c, 0.05); got != 1.0 {
		t.Errorf("at boundary = %v, want 1.0", got)
	}
	if got := CostDimension(c, 0.06); got != 0.0 {
		t.Errorf("over = %v, want 0.0", got)
	}
	noBudget := &Case{}
	if got := CostDimension(noBudget, 100.0); got != 1.0 {
		t.Errorf("no budget = %v, want 1.0", got)
	}
}
