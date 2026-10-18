package eval

import (
	"strings"
	"testing"

	"conduit/internal/ai"
)

// Feature 3 (bd-18u v0.3): cache-friendly rubric placement. The judge
// prompt's cacheable prefix must be byte-stable across cases and sampled
// runs, and the rubric must be declared so cache-aware providers (the
// Anthropic path) pin their system breakpoint on it by construction.

func TestJudgeSystemMessageByteStable(t *testing.T) {
	// The system message is the shared prefix across every judge call; it
	// must be byte-identical on every call, with no per-call bytes.
	a := judgeSystemMessage()
	b := judgeSystemMessage()
	if a.Content != b.Content {
		t.Error("system Content varies between calls; shared prefix would be invalidated")
	}
	if len(a.SystemBlocks) != 1 || a.SystemBlocks[0].Text != b.SystemBlocks[0].Text {
		t.Error("system blocks vary between calls")
	}
	if a.SystemBlocks[0].Dynamic {
		t.Error("rubric block marked Dynamic; the system breakpoint would exclude it")
	}

	// The rubric anchors and output contract are intact (rubric-v1 text).
	for _, want := range []string{"0.0", "0.5", "1.0", "JSON", "score"} {
		if !strings.Contains(a.Content, want) {
			t.Errorf("rubric missing %q", want)
		}
	}
}

func TestJudgeUserPromptPerCase(t *testing.T) {
	// Only the user message varies; it must contain the case's goal and
	// transcript, and nothing of the rubric (the rubric lives solely in
	// the system position — duplicating it per call would both dilute the
	// cacheable prefix and risk divergence between the two copies).
	c1 := judgeUserPrompt(judgeTestCase())
	c2 := judgeUserPrompt(&Case{
		ID:    "ev-other",
		Title: "other",
		Goal:  "Something entirely different",
		Turns: []Turn{{Turn: 1, Role: "user", Content: "hi"}},
	})
	if c1 == c2 {
		t.Error("user prompts identical across cases")
	}
	if !strings.Contains(c1, "Diagnose the interface error counters") {
		t.Error("user prompt missing the case goal")
	}
	for _, rubricOnly := range []string{"Scoring anchors", "0.5 = Partially accomplished"} {
		if strings.Contains(c1, rubricOnly) || strings.Contains(c2, rubricOnly) {
			t.Errorf("user prompt contains rubric text %q", rubricOnly)
		}
	}
}

func TestJudgeRequestsShareSystemPrefix(t *testing.T) {
	// End-to-end through the judge: every provider request carries the
	// identical system message and a per-case user message.
	p := &mockJudgeProvider{resp: &ai.GenerateResponse{
		Content: `{"score": 0.5, "rationale": "ok"}`,
	}}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}
	cases := []*Case{judgeTestCase(), {ID: "ev-b", Title: "b", Goal: "other goal", Turns: judgeTestCase().Turns}}
	if _, err := j.JudgeGoal(cases[0]); err != nil {
		t.Fatalf("JudgeGoal: %v", err)
	}
	if _, err := j.JudgeGoal(cases[1]); err != nil {
		t.Fatalf("JudgeGoal: %v", err)
	}
	if len(p.Requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(p.Requests))
	}
	s0 := p.Requests[0].Messages[0]
	s1 := p.Requests[1].Messages[0]
	if s0.Content != s1.Content || len(s0.SystemBlocks) != len(s1.SystemBlocks) {
		t.Fatal("system messages differ across requests; prompt cache would miss")
	}
	for i := range s0.SystemBlocks {
		if s0.SystemBlocks[i].Text != s1.SystemBlocks[i].Text || s0.SystemBlocks[i].Dynamic != s1.SystemBlocks[i].Dynamic {
			t.Errorf("system block %d differs across requests", i)
		}
	}
	u0 := p.Requests[0].Messages[1].Content
	u1 := p.Requests[1].Messages[1].Content
	if u0 == u1 {
		t.Error("user prompts should differ per case")
	}
}

func TestJudgeSystemBlockWireInvariant(t *testing.T) {
	// The Anthropic path keeps the block-array system form only when the
	// blocks' joined texts equal Content (systemMessageBlocks, ai pkg);
	// otherwise it degrades to a plain string and the cache breakpoint
	// rides the non-block path. Assert that invariant on the judge's
	// system message: it is what guarantees the rubric stays a distinct,
	// byte-stable block for addCacheBreakpoints' staticEnd placement.
	// (The wire conversion itself is covered by internal/ai's own cache
	// tests; this pins the judge side of the contract.)
	msg := judgeSystemMessage()
	if len(msg.SystemBlocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(msg.SystemBlocks))
	}
	var joined []string
	for _, b := range msg.SystemBlocks {
		joined = append(joined, b.Text)
	}
	if strings.Join(joined, "\n\n") != msg.Content {
		t.Error("Content != joined block texts; wire conversion would drop the block form")
	}
	if msg.SystemBlocks[0].Type != "text" || msg.SystemBlocks[0].Text == "" {
		t.Errorf("rubric block shape wrong: %+v", msg.SystemBlocks[0])
	}
}

func TestJudgePromptShapeContract(t *testing.T) {
	// The full request shape: system rubric first, user transcript second,
	// nothing else. Guards against a future edit sneaking per-call bytes
	// into the shared prefix or reordering the messages.
	p := &mockJudgeProvider{resp: &ai.GenerateResponse{
		Content: `{"score": 1.0, "rationale": "done"}`,
	}}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}
	if _, err := j.JudgeGoal(judgeTestCase()); err != nil {
		t.Fatalf("JudgeGoal: %v", err)
	}
	req := p.Requests[0]
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want [system, user]", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || len(req.Messages[0].SystemBlocks) != 1 {
		t.Fatal("first message must be the system rubric with one static block")
	}
	if req.Messages[1].Role != "user" {
		t.Fatal("second message must be the user transcript")
	}
	// MaxTokens must not vary per call either (part of the request
	// fingerprint some providers fold into cache keys).
	if req.MaxTokens != judgeMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, judgeMaxTokens)
	}
}
