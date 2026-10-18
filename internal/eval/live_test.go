package eval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"conduit/internal/ai"
)

// queueProvider is an in-package fake ai.Provider for live-mode tests: it
// hands out queued responses in order (one per user turn) and records every
// request it saw. Tests never touch the network. Mirrors the mocking style
// of judge_llm_test.go's mockJudgeProvider.
type queueProvider struct {
	name string

	mu        sync.Mutex
	responses []ai.GenerateResponse
	requests  []ai.GenerateRequest
	callCount int
}

var _ ai.Provider = (*queueProvider)(nil)

func (q *queueProvider) Name() string {
	if q.name == "" {
		return "queue-mock"
	}
	return q.name
}

func (q *queueProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.callCount++
	clone := *req
	q.requests = append(q.requests, clone)
	if len(q.responses) == 0 {
		return nil, errors.New("queueProvider: no queued response for call")
	}
	resp := q.responses[0]
	q.responses = q.responses[1:]
	return &resp, nil
}

func (q *queueProvider) enqueue(resps ...ai.GenerateResponse) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.responses = append(q.responses, resps...)
}

func (q *queueProvider) calls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.callCount
}

func (q *queueProvider) lastRequest() (ai.GenerateRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.requests) == 0 {
		return ai.GenerateRequest{}, false
	}
	return q.requests[len(q.requests)-1], true
}

// liveModeCase builds a two-turn corpus case for live-mode tests. The
// scripted assistant turn is replay-only material: live mode must NOT copy
// it into the captured transcript (a live capture that echoes scripted
// assistant content proves the swap didn't happen).
func liveModeCase() *Case {
	return &Case{
		ID:                "ev-live-1",
		Title:             "live mode fixture",
		Tags:              []string{"tool-triage"},
		TurnBudget:        4,
		DenyClasses:       []string{"send"},
		ExpectedFragments: []string{"crc"},
		Goal:              "Directs the user to check interface error counters",
		Turns: []Turn{
			{Turn: 1, Role: "user", Content: "Lab switch sw-fake-01 drops connections. Where do I start?"},
			{Turn: 1, Role: "assistant", Content: "SCRIPTED REPLAY ONLY", Tools: []ToolCall{{Name: "bash", Args: "show version"}}},
			{Turn: 2, Role: "user", Content: "Counters show CRC errors climbing."},
			{Turn: 2, Role: "assistant", Content: "MORE SCRIPTED REPLAY"},
		},
	}
}

// liveResponses returns canned provider answers matching the fixture: turn 1
// makes a read tool call, turn 2 answers with the fragment the case expects.
func liveResponses() []ai.GenerateResponse {
	return []ai.GenerateResponse{
		{
			Content: "Checking interface error counters now.",
			ToolCalls: []ai.ToolCall{
				{ID: "call-1", Name: "read", Args: map[string]interface{}{"cmd": "show interface counters"}},
			},
			Usage: ai.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
		},
		{
			Content: "CRC errors climbing on one port usually means a bad cable. Check the error counters after the swap.",
			Usage:   ai.Usage{PromptTokens: 150, CompletionTokens: 30, TotalTokens: 180},
		},
	}
}

func TestLiveRunnerCapturesTranscript(t *testing.T) {
	p := &queueProvider{}
	p.enqueue(liveResponses()...)
	lr := &LiveRunner{Provider: p, Model: "example/model-a", Judge: fixedJudge{score: 1.0}}

	summary, err := lr.Run([]*Case{liveModeCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One provider call per corpus user turn.
	if got := p.calls(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (one per user turn)", got)
	}
	if len(summary.PerCase) != 1 {
		t.Fatalf("per-case results = %d, want 1", len(summary.PerCase))
	}

	// Transcript shape: the captured assistant turns replace the scripted
	// ones; the user turns are preserved verbatim.
	res := summary.PerCase[0]
	if res.CaseID != "ev-live-1" {
		t.Fatalf("case ID = %q, want ev-live-1", res.CaseID)
	}

	// The captured transcript, reconstructed from the result, is not directly
	// observable — but the checks prove what it contained: the scripted
	// "SCRIPTED REPLAY ONLY" content and its bash tool call must not have
	// leaked in, and the live turn-2 content (containing "crc") must have.
	// Turn budget is 4 and the captured transcript has 2 turns, so the budget
	// check passing proves live turn numbering.
	for _, v := range res.Violations {
		if v.Check == "deny_class" && strings.Contains(v.Detail, "bash") {
			t.Errorf("scripted deny-class violation leaked into live scoring: %+v", v)
		}
	}
	if !res.Passed {
		t.Errorf("case failed: violations=%+v", res.Violations)
	}
	// The stub judge scores on fragments; with fixedJudge the goal dimension
	// is fixed. What we can assert on the deterministic side: the captured
	// turn-2 content contains "crc", so fragments must be 1.0.
	if res.Dimensions.Fragments != 1.0 {
		t.Errorf("fragments = %v, want 1.0 (live turn-2 content contains the fragment)", res.Dimensions.Fragments)
	}
	if res.TurnCount != 2 {
		t.Errorf("turn count = %d, want 2 (live transcript shape)", res.TurnCount)
	}
}

func TestLiveRunnerRequestShape(t *testing.T) {
	p := &queueProvider{}
	p.enqueue(liveResponses()...)
	lr := &LiveRunner{Provider: p, Model: "example/model-a", Judge: fixedJudge{score: 1.0}}

	if _, err := lr.Run([]*Case{liveModeCase()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	req, ok := p.lastRequest()
	if !ok {
		t.Fatal("provider saw no requests")
	}
	if req.Model != "example/model-a" {
		t.Errorf("request model = %q, want example/model-a", req.Model)
	}
	// Second call must carry the first captured exchange as history, ending
	// with the second user turn.
	if n := len(req.Messages); n < 4 {
		t.Fatalf("second call messages = %d, want >= 4 (system + user1 + asst1 + user2)", n)
	}
	if req.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", req.Messages[0].Role)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "CRC errors climbing") {
		t.Errorf("last message = %+v, want user turn 2 content", last)
	}
}

func TestLiveRunnerValidation(t *testing.T) {
	tests := []struct {
		name    string
		lr      *LiveRunner
		wantErr string
	}{
		{
			name:    "nil provider",
			lr:      &LiveRunner{Model: "m", Judge: fixedJudge{score: 1}},
			wantErr: "provider is required",
		},
		{
			name:    "empty model",
			lr:      &LiveRunner{Provider: &queueProvider{}, Judge: fixedJudge{score: 1}},
			wantErr: "model is required",
		},
		{
			name:    "nil judge",
			lr:      &LiveRunner{Provider: &queueProvider{}, Model: "m"},
			wantErr: "judge is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.lr.Run([]*Case{liveModeCase()})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLiveRunnerProviderErrorFails(t *testing.T) {
	p := &queueProvider{}
	lr := &LiveRunner{Provider: p, Model: "example/model-a", Judge: fixedJudge{score: 1.0}}

	_, err := lr.Run([]*Case{liveModeCase()})
	if err == nil {
		t.Fatal("Run succeeded with an empty provider queue; want error")
	}
	if !strings.Contains(err.Error(), "case ev-live-1") || !strings.Contains(err.Error(), "turn 1") {
		t.Errorf("error = %v, want case + turn context", err)
	}
	// No usage accumulated from failed calls.
	if u := lr.Usage(); u.PromptTokens != 0 || u.CompletionTokens != 0 {
		t.Errorf("usage after failure = %+v, want zero", u)
	}
}

func TestLiveRunnerNoUserTurns(t *testing.T) {
	p := &queueProvider{}
	lr := &LiveRunner{Provider: p, Model: "example/model-a", Judge: fixedJudge{score: 1.0}}

	c := &Case{ID: "ev-live-empty", Title: "no user turns", Turns: []Turn{
		{Turn: 1, Role: "assistant", Content: "scripted only"},
	}}
	_, err := lr.Run([]*Case{c})
	if err == nil || !strings.Contains(err.Error(), "no user turns") {
		t.Errorf("err = %v, want no-user-turns error", err)
	}
	if p.calls() != 0 {
		t.Errorf("provider calls = %d, want 0", p.calls())
	}
}

func TestLiveRunnerUsageAccounting(t *testing.T) {
	p := &queueProvider{}
	p.enqueue(liveResponses()...)
	lr := &LiveRunner{Provider: p, Model: "claude-haiku-4-5", Judge: fixedJudge{score: 1.0}}

	summary, err := lr.Run([]*Case{liveModeCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Run-level accounting: 100+150 prompt, 20+30 completion tokens.
	u := lr.Usage()
	if u.PromptTokens != 250 || u.CompletionTokens != 50 {
		t.Errorf("run usage = %+v, want 250/50", u)
	}
	if summary.LiveModel != "claude-haiku-4-5" {
		t.Errorf("summary.LiveModel = %q, want claude-haiku-4-5", summary.LiveModel)
	}
	if summary.LiveUsage.Calls != 2 {
		t.Errorf("summary.LiveUsage.Calls = %d, want 2", summary.LiveUsage.Calls)
	}

	// Per-case accounting: the model-under-test's spend lands on the case.
	lu := summary.PerCase[0].LiveUsage
	if lu == nil {
		t.Fatal("per-case LiveUsage is nil, want live accounting")
	}
	if lu.Calls != 2 || lu.PromptTokens != 250 || lu.CompletionTokens != 50 {
		t.Errorf("per-case LiveUsage = %+v, want 2 calls, 250/50 tokens", lu)
	}
	// claude-haiku-4-5 is in the pricing matrix, so the cost is resolver-priced.
	if !lu.CostPriced || lu.CostUSD <= 0 {
		t.Errorf("per-case LiveUsage cost = %f priced=%v, want priced > 0", lu.CostUSD, lu.CostPriced)
	}
}
