package eval

import (
	"strings"
	"testing"
)

func TestParseCorpus_Valid(t *testing.T) {
	in := `---
id: ev-0001
title: "Router triage"
tags: [tools, tone]
models_under_test: [providerA/model-x]
turn_budget: 8
deny_classes: [send]
expected_fragments: ["crc", "counters"]
cost_budget_usd: 0.10
cost:
  model: synthetic-model-a
  prompt_tokens: 1200
  completion_tokens: 300
goal: "Finds interface errors first"
---
` + "```json" + `
{"turn":1,"role":"user","content":"Router drops."}
{"turn":1,"role":"assistant","content":"Check CRC counters.","tools":[{"name":"read","args":"counters"}],"allow":["read","query"]}
{"turn":2,"role":"user","content":"CRC climbing."}
` + "```" + `
`
	c, err := ParseCorpus(in)
	if err != nil {
		t.Fatalf("ParseCorpus: %v", err)
	}
	if c.ID != "ev-0001" {
		t.Errorf("ID = %q, want ev-0001", c.ID)
	}
	if c.Title != "Router triage" {
		t.Errorf("Title = %q", c.Title)
	}
	if c.TurnBudget != 8 {
		t.Errorf("TurnBudget = %d, want 8", c.TurnBudget)
	}
	if len(c.DenyClasses) != 1 || c.DenyClasses[0] != "send" {
		t.Errorf("DenyClasses = %v", c.DenyClasses)
	}
	if len(c.ExpectedFragments) != 2 {
		t.Errorf("ExpectedFragments = %v", c.ExpectedFragments)
	}
	if c.CostBudgetUSD != 0.10 {
		t.Errorf("CostBudgetUSD = %v", c.CostBudgetUSD)
	}
	if c.Cost.Model != "synthetic-model-a" || c.Cost.PromptTokens != 1200 || c.Cost.CompletionTokens != 300 {
		t.Errorf("Cost = %+v", c.Cost)
	}
	if len(c.Turns) != 3 {
		t.Fatalf("len(Turns) = %d, want 3", len(c.Turns))
	}
	at := c.Turns[1]
	if len(at.Tools) != 1 || at.Tools[0].Name != "read" {
		t.Errorf("assistant turn tools = %+v", at.Tools)
	}
	if len(at.Allow) != 2 {
		t.Errorf("assistant turn allow = %v", at.Allow)
	}
}

func TestParseCorpus_Invalid(t *testing.T) {
	validTurns := "```json" + "\n" +
		`{"turn":1,"role":"user","content":"hi"}` + "\n" +
		"```"

	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{
			name:    "missing frontmatter start",
			in:      "no frontmatter here",
			wantErr: "frontmatter",
		},
		{
			name: "unclosed frontmatter",
			in: "---\nid: x\ntitle: y\n" +
				"```json\n{}\n```",
			wantErr: "not closed",
		},
		{
			name:    "missing id",
			in:      "---\ntitle: y\n---\n" + validTurns,
			wantErr: "id is required",
		},
		{
			name:    "missing title",
			in:      "---\nid: x\n---\n" + validTurns,
			wantErr: "title is required",
		},
		{
			name: "no turns fence",
			in: "---\nid: x\ntitle: y\n---\n" +
				"no transcript here",
			wantErr: "fenced transcript",
		},
		{
			name: "bad turn json",
			in: "---\nid: x\ntitle: y\n---\n" +
				"```json\n{not json}\n```",
			wantErr: "transcript line 1",
		},
		{
			name: "zero turns",
			in: "---\nid: x\ntitle: y\n---\n" +
				"```json\n\n```",
			wantErr: "at least one turn",
		},
		{
			name: "bad role",
			in: "---\nid: x\ntitle: y\n---\n" +
				"```json\n{\"turn\":1,\"role\":\"system\",\"content\":\"x\"}\n```",
			wantErr: "role must be user or assistant",
		},
		{
			name: "duplicate turn",
			in: "---\nid: x\ntitle: y\n---\n" +
				"```json\n{\"turn\":1,\"role\":\"user\",\"content\":\"a\"}\n{\"turn\":1,\"role\":\"user\",\"content\":\"b\"}\n```",
			wantErr: "duplicate turn",
		},
		{
			name: "turn number zero",
			in: "---\nid: x\ntitle: y\n---\n" +
				"```json\n{\"turn\":0,\"role\":\"user\",\"content\":\"a\"}\n```",
			wantErr: "turn number must be >= 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCorpus(tt.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadCorpus_SortsAndLoads(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(cases) != 12 {
		t.Fatalf("got %d cases, want 12", len(cases))
	}
	// Sorted by filename: ev-0001 first, ev-0012 last.
	if cases[0].ID != "ev-0001" || cases[len(cases)-1].ID != "ev-0012" {
		t.Errorf("order wrong: first=%s last=%s", cases[0].ID, cases[len(cases)-1].ID)
	}
}

func TestLoadCorpus_MissingDir(t *testing.T) {
	if _, err := LoadCorpus("testdata/nope"); err == nil {
		t.Fatal("expected error for missing dir")
	}
}
