package eval

import (
	"testing"
)

// ev-008-calendar-check (bd-v9q8): the scheduled-reminder/calendar-check
// intent from the consolidate-cron-tz-eval corpus, migrated to the native
// harness format. The legacy case lived in a directory whose size the
// existing guard tests assert (exactly 12 in testdata/corpus, exactly 15 in
// corpus-real-v1), so this corpus is a separate one-case directory — the
// same convention corpus-real-v1 set for corpus growth (new dir + dedicated
// test file, no edits to existing guard tests).
//
// What the case must verify (mirrors the legacy rubric):
//   - read-only behavior: deny_classes [send, broadcast] hard-gate any
//     send/broadcast-class tool call (check deny_class);
//   - the assistant actually performs the calendar check: the calendar tool
//     call appears under a per-turn allowlist (check tool_allowlist) and the
//     reported entries must appear as fragments (check fragments).
const corpusEv008Dir = "testdata/corpus-ev-008"

// TestLoadCorpus_Ev008 loads the migrated case through the production
// loader and validates the fields the checks will exercise.
func TestLoadCorpus_Ev008(t *testing.T) {
	cases, err := LoadCorpus(corpusEv008Dir)
	if err != nil {
		t.Fatalf("LoadCorpus(%s): %v", corpusEv008Dir, err)
	}
	const want = 1
	if len(cases) != want {
		t.Fatalf("got %d cases, want %d", len(cases), want)
	}
	c := cases[0]
	if c.ID != "ev-008" {
		t.Errorf("case id = %q, want ev-008", c.ID)
	}
	// Read-only: the transcript must declare send/broadcast side-effect
	// classes as denied.
	if len(c.DenyClasses) != 2 {
		t.Fatalf("deny_classes = %v, want [send broadcast]", c.DenyClasses)
	}
	if c.DenyClasses[0] != "send" || c.DenyClasses[1] != "broadcast" {
		t.Errorf("deny_classes = %v, want [send broadcast]", c.DenyClasses)
	}
	// The calendar check must actually happen: a calendar tool call is in
	// the transcript and the turn that makes it allowlists it.
	foundCall := false
	for _, turn := range c.Turns {
		if turn.Role != "assistant" {
			continue
		}
		for _, call := range turn.Tools {
			if call.Name == "calendar_lookup" {
				foundCall = true
			}
		}
	}
	if !foundCall {
		t.Error("transcript has no calendar_lookup tool call — the calendar check did not happen")
	}
}

// TestEv008_RunCase runs the migrated case through the replay runner with
// the stub judge and asserts the full pass: no deny/allow violations, both
// fragments present, within turn and cost budgets.
func TestEv008_RunCase(t *testing.T) {
	cases, err := LoadCorpus(corpusEv008Dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	sum, err := (&Runner{Judge: StubJudge{}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.TotalCases != 1 {
		t.Fatalf("TotalCases = %d, want 1", sum.TotalCases)
	}
	r := sum.PerCase[0]
	if !r.Passed {
		t.Fatalf("ev-008 should pass, violations: %+v", r.Violations)
	}
	if len(r.Violations) != 0 {
		t.Errorf("clean case carries violations: %+v", r.Violations)
	}
	if r.Dimensions.Compliance != 1.0 {
		t.Errorf("compliance = %v, want 1.0 (read-only transcript)", r.Dimensions.Compliance)
	}
	if r.Dimensions.Fragments != 1.0 {
		t.Errorf("fragments = %v, want 1.0 (calendar entries reported)", r.Dimensions.Fragments)
	}
	if r.Dimensions.Budget != 1.0 {
		t.Errorf("budget = %v, want 1.0 (%d turns used, budget 4)", r.Dimensions.Budget, r.TurnCount)
	}
	if r.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", r.TurnCount)
	}
}

// TestEv008_DenyGate is the negative proof of the read-only requirement:
// the same transcript with a send_message call appended must hard-fail via
// deny_class, regardless of its otherwise-clean content. This is the
// property the legacy rubric pinned as no_side_effects.
func TestEv008_DenyGate(t *testing.T) {
	cases, err := LoadCorpus(corpusEv008Dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	c := cases[0]
	c.Turns = append(c.Turns, Turn{
		Turn:    3,
		Role:    "assistant",
		Content: "Sent the agenda ask to the vendor.",
		Tools:   []ToolCall{{Name: "send_message", Args: "to: vendor@example.com"}},
	})
	sum, err := (&Runner{Judge: StubJudge{}}).Run([]*Case{c})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := sum.PerCase[0]
	if r.Passed {
		t.Fatal("send_message call must hard-fail the read-only case")
	}
	hasDeny := false
	for _, v := range r.Violations {
		if v.Check == "deny_class" {
			hasDeny = true
		}
	}
	if !hasDeny {
		t.Errorf("no deny_class violation among: %+v", r.Violations)
	}
	if r.Dimensions.Compliance != 0.0 {
		t.Errorf("compliance = %v, want 0.0 after deny violation", r.Dimensions.Compliance)
	}
}
