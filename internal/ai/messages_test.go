package ai

import (
	"strings"
	"testing"
)

func TestTrimRequestToFitContext_NilAndSmall(t *testing.T) {
	// nil request — should not panic
	trimRequestToFitContext(nil, 0)

	// Single message — nothing to trim
	req := &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hello"}},
		Model:     "codellama", // 16384 context
		MaxTokens: 4000,
	}
	trimRequestToFitContext(req, 0)
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
}

func TestTrimRequestToFitContext_FitsWithinBudget(t *testing.T) {
	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "how are you?"},
		},
		Model:     "claude-sonnet-4", // 200k context — everything fits
		MaxTokens: 4000,
	}
	trimRequestToFitContext(req, 0)
	if len(req.Messages) != 4 {
		t.Errorf("expected 4 messages (no trimming needed), got %d", len(req.Messages))
	}
}

func TestTrimRequestToFitContext_TrimsOldestHistory(t *testing.T) {
	// codellama has 16384 context. With max_tokens=4000, budget = 12384 tokens = ~49536 chars.
	// System prompt + user message are small. Fill history with large messages to force trimming.
	bigContent := strings.Repeat("x", 20000) // ~5000 tokens each

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: bigContent},       // oldest history — should be dropped
			{Role: "assistant", Content: bigContent},   // old history — should be dropped
			{Role: "user", Content: bigContent},        // newer — might be kept
			{Role: "assistant", Content: bigContent},   // newer — might be kept
			{Role: "user", Content: "current question"},// current message — always kept
		},
		Model:     "codellama", // 16384 context
		MaxTokens: 4000,
	}

	origLen := len(req.Messages)
	trimRequestToFitContext(req, 0)

	if len(req.Messages) >= origLen {
		t.Errorf("expected messages to be trimmed, got %d (was %d)", len(req.Messages), origLen)
	}

	// System prompt must be first
	if req.Messages[0].Role != "system" {
		t.Errorf("expected first message to be system, got %s", req.Messages[0].Role)
	}

	// Current user message must be last
	last := req.Messages[len(req.Messages)-1]
	if last.Content != "current question" {
		t.Errorf("expected last message to be current question, got %q", last.Content)
	}
}

func TestTrimRequestToFitContext_PreservesSystemAndUser(t *testing.T) {
	// Even if system+user alone exceed budget, they should still be kept
	hugeSystem := strings.Repeat("s", 100000)

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: hugeSystem},
			{Role: "user", Content: "old message"},
			{Role: "assistant", Content: "old reply"},
			{Role: "user", Content: "current"},
		},
		Model:     "codellama", // 16384 context
		MaxTokens: 4000,
	}

	trimRequestToFitContext(req, 0)

	// Should have only system + current user (all history dropped)
	if len(req.Messages) != 2 {
		t.Errorf("expected 2 messages (system + user), got %d", len(req.Messages))
	}
	if req.Messages[0].Content != hugeSystem {
		t.Error("system message was modified")
	}
	if req.Messages[1].Content != "current" {
		t.Errorf("expected current user message, got %q", req.Messages[1].Content)
	}
}

func TestTrimRequestToFitContext_LargeModel_NoTrim(t *testing.T) {
	// With empty model, defaults to 200k context — should not trim reasonable history
	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: strings.Repeat("x", 1000)},
			{Role: "user", Content: "msg1"},
			{Role: "assistant", Content: "reply1"},
			{Role: "user", Content: "msg2"},
			{Role: "assistant", Content: "reply2"},
			{Role: "user", Content: "current"},
		},
		Model:     "", // defaults to 200k
		MaxTokens: 4000,
	}

	trimRequestToFitContext(req, 0)
	if len(req.Messages) != 6 {
		t.Errorf("expected no trimming for large context window, got %d messages", len(req.Messages))
	}
}

func TestTrimRequestToFitContext_AccountsForTools(t *testing.T) {
	// Create tools that eat into the budget
	var tools []Tool
	for i := 0; i < 50; i++ {
		tools = append(tools, Tool{
			Name:        strings.Repeat("t", 50),
			Description: strings.Repeat("d", 500),
		})
	}

	// With codellama (16384), tools alone use ~50*(50+500+200)=37500 chars ~9375 tokens
	// That leaves very little room for messages
	bigHistory := strings.Repeat("h", 10000)
	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: bigHistory},
			{Role: "assistant", Content: bigHistory},
			{Role: "user", Content: "current"},
		},
		Model:     "codellama",
		MaxTokens: 4000,
		Tools:     tools,
	}

	origLen := len(req.Messages)
	trimRequestToFitContext(req, 0)

	if len(req.Messages) >= origLen {
		t.Errorf("expected trimming with large tool set, got %d messages (was %d)", len(req.Messages), origLen)
	}
}

func TestTrimRequestToFitContext_ConfigOverride(t *testing.T) {
	bigContent := strings.Repeat("x", 20000) // ~5000 tokens each

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: bigContent},
			{Role: "assistant", Content: bigContent},
			{Role: "user", Content: bigContent},
			{Role: "assistant", Content: bigContent},
			{Role: "user", Content: "current question"},
		},
		Model:     "", // empty model — would default to 200K and not trim
		MaxTokens: 4000,
	}

	// Without override: empty model → 200K context → no trimming needed
	trimRequestToFitContext(req, 0)
	if len(req.Messages) != 6 {
		t.Fatalf("expected no trimming with default 200K window, got %d", len(req.Messages))
	}

	// With override: 16384 context → must trim
	trimRequestToFitContext(req, 16384)
	if len(req.Messages) >= 6 {
		t.Errorf("expected trimming with 16384 override, got %d messages", len(req.Messages))
	}

	// System and current user must survive
	if req.Messages[0].Role != "system" {
		t.Error("system message not preserved")
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Content != "current question" {
		t.Errorf("current user message not preserved, got %q", last.Content)
	}
}
