package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"conduit/internal/ai"
)

// mockJudgeProvider is an in-package fake ai.Provider returning canned
// responses; it records the requests it saw. Tests never touch the network.
type mockJudgeProvider struct {
	name     string
	resp     *ai.GenerateResponse
	err      error
	Requests []ai.GenerateRequest
}

func (m *mockJudgeProvider) Name() string {
	if m.name == "" {
		return "mock"
	}
	return m.name
}

func (m *mockJudgeProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	clone := *req
	m.Requests = append(m.Requests, clone)
	return m.resp, nil
}

// judgeTestCase is the minimal case used by the judge tests.
func judgeTestCase() *Case {
	return &Case{
		ID:    "ev-test",
		Title: "judge test",
		Goal:  "Diagnose the interface error counters",
		Turns: []Turn{
			{Turn: 1, Role: "user", Content: "Interface is dropping packets."},
			{Turn: 1, Role: "assistant", Content: "Check error counters first."},
		},
	}
}

func TestLLMJudgeValidJSON(t *testing.T) {
	p := &mockJudgeProvider{resp: &ai.GenerateResponse{
		Content: `{"score": 0.75, "rationale": "mostly complete"}`,
	}}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}

	score, err := j.JudgeGoal(judgeTestCase())
	if err != nil {
		t.Fatalf("JudgeGoal: %v", err)
	}
	if score != 0.75 {
		t.Errorf("score = %v, want 0.75", score)
	}
	if len(p.Requests) != 1 {
		t.Fatalf("provider calls = %d, want 1 (one call per case)", len(p.Requests))
	}

	// Auditability invariant: the system prompt carries the rubric and the
	// user prompt carries the goal and full transcript with roles.
	req := p.Requests[0]
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system user]", req.Messages)
	}
	for _, want := range []string{"0.0", "0.5", "1.0", "JSON"} {
		if !strings.Contains(req.Messages[0].Content, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if !strings.Contains(req.Messages[1].Content, "Diagnose the interface error counters") ||
		!strings.Contains(req.Messages[1].Content, "USER:") ||
		!strings.Contains(req.Messages[1].Content, "ASSISTANT:") {
		t.Errorf("user prompt missing goal or roles: %q", req.Messages[1].Content)
	}
	if req.MaxTokens != judgeMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", req.MaxTokens, judgeMaxTokens)
	}
}

func TestLLMJudgeFencedJSON(t *testing.T) {
	// Models wrap JSON in code fences despite the output contract; the
	// parser must strip them (rubric output contract, design doc §3b).
	p := &mockJudgeProvider{resp: &ai.GenerateResponse{
		Content: "```json\n{\"score\": 0.4, \"rationale\": \"partial\"}\n```",
	}}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}

	score, err := j.JudgeGoal(judgeTestCase())
	if err != nil {
		t.Fatalf("JudgeGoal: %v", err)
	}
	if score != 0.4 {
		t.Errorf("score = %v, want 0.4", score)
	}
}

func TestLLMJudgeScoreClamping(t *testing.T) {
	for _, tt := range []struct {
		raw  float64
		want float64
	}{
		{1.5, 1.0},
		{-0.2, 0.0},
		{2.0, 1.0},
	} {
		p := &mockJudgeProvider{resp: &ai.GenerateResponse{
			Content: fmt.Sprintf(`{"score": %v, "rationale": "overrange"}`, tt.raw),
		}}
		j := &LLMJudge{Provider: p, Model: "example/model-a"}

		score, err := j.JudgeGoal(judgeTestCase())
		if err != nil {
			t.Fatalf("JudgeGoal(raw=%v): %v", tt.raw, err)
		}
		if score != tt.want {
			t.Errorf("score for %v = %v, want %v", tt.raw, score, tt.want)
		}
	}
}

func TestLLMJudgeUnparseable(t *testing.T) {
	for _, content := range []string{
		"I give this conversation a solid 8 out of 10.",
		"score: 0.9",
		"",
	} {
		p := &mockJudgeProvider{resp: &ai.GenerateResponse{Content: content}}
		j := &LLMJudge{Provider: p, Model: "example/model-a"}

		_, err := j.JudgeGoal(judgeTestCase())
		if err == nil {
			t.Errorf("unparseable output %q accepted; must error, never guess", content)
		}
	}
}

func TestLLMJudgeProviderError(t *testing.T) {
	boom := errors.New("provider quota exhausted")
	p := &mockJudgeProvider{err: boom}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}

	_, err := j.JudgeGoal(judgeTestCase())
	if err == nil {
		t.Fatal("provider error swallowed; must propagate")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error chain lost original: %v", err)
	}
}

func TestLLMJudgeID(t *testing.T) {
	p := &mockJudgeProvider{name: "prov-x"}
	j := &LLMJudge{Provider: p, Model: "example/model-a"}
	want := "llm/prov-x/example/model-a@rubric-v1"
	if got := j.ID(); got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}

	// Empty model falls back to a placeholder rather than producing
	// "llm/prov-x/@rubric-v1" (auditability invariant: ID must be present).
	j2 := &LLMJudge{Provider: p}
	if got := j2.ID(); got != "llm/prov-x/default@rubric-v1" {
		t.Errorf("empty-model ID = %q", got)
	}
}

func TestJudgeByNameLLMWiring(t *testing.T) {
	// Injected-provider path: "llm" resolves through LLMJudgeByName with a
	// provider the caller built (from config in production, mock here).
	p := &mockJudgeProvider{name: "prov-x"}
	j, err := LLMJudgeByName("llm", "example/model-a", p)
	if err != nil {
		t.Fatalf("LLMJudgeByName: %v", err)
	}
	llm, ok := j.(*LLMJudge)
	if !ok {
		t.Fatalf("type %T, want *LLMJudge", j)
	}
	if llm.Provider != ai.Provider(p) || llm.Model != "example/model-a" {
		t.Errorf("wiring mismatch: provider=%v model=%q", llm.Provider, llm.Model)
	}

	// "llm" without a model is a config error, surfaced clearly.
	if _, err := LLMJudgeByName("llm", "", p); err == nil {
		t.Error("llm with empty model accepted; must error")
	}
	// A nil provider with a model is equally broken.
	if _, err := LLMJudgeByName("llm", "example/model-a", nil); err == nil {
		t.Error("llm with nil provider accepted; must error")
	}
	// Stub path unchanged: JudgeByName still resolves it without wiring.
	if _, err := JudgeByName("stub"); err != nil {
		t.Errorf("stub rejected: %v", err)
	}
	// And the unknown-name error still surfaces.
	if _, err := JudgeByName("llm/oops"); err == nil {
		t.Error("unknown judge accepted; must error rather than fall back")
	}
}
