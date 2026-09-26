package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.18(b): trimRequestToFitContext ran only on the first request
// of a turn; each tool round appended results with no re-check, so long
// chains hit "prompt is too long" 400s.

func requestChars(req *GenerateRequest) int {
	n := 0
	for _, m := range req.Messages {
		n += estimateChatMessageChars(m)
	}
	return n
}

func toolRound(i, resultChars int) []ChatMessage {
	id := fmt.Sprintf("toolu_%d", i)
	return []ChatMessage{
		{Role: "assistant", Content: fmt.Sprintf("step %d", i), ToolCalls: []ToolCall{{ID: id, Name: "Bash", Args: map[string]interface{}{"command": "ls"}}}},
		{Role: "tool", ToolCallID: id, Content: strings.Repeat("x", resultChars)},
	}
}

// assertToolPairsIntact checks every tool result follows the assistant
// message that issued its tool_use id (the Anthropic API's pairing rule).
func assertToolPairsIntact(t *testing.T, msgs []ChatMessage) {
	t.Helper()
	open := map[string]bool{}
	for i, m := range msgs {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			open = map[string]bool{}
			for _, tc := range m.ToolCalls {
				open[tc.ID] = true
			}
		case m.Role == "tool":
			if !open[m.ToolCallID] {
				t.Fatalf("message %d: tool result %q has no preceding tool_use", i, m.ToolCallID)
			}
		default:
			open = map[string]bool{}
		}
	}
}

func TestFitRequestToWindow_FitsUnchanged(t *testing.T) {
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}, MaxTokens: 100}
	if got := fitRequestToWindow(req, 200000); got != req {
		t.Error("a request that fits must be returned as-is")
	}
}

func TestFitRequestToWindow_DropsWholeRoundsAndKeepsAnchors(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: strings.Repeat("old history ", 400)},
		{Role: "assistant", Content: strings.Repeat("old answer ", 400)},
		{Role: "user", Content: "THE GOAL"},
	}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, toolRound(i, 4000)...)
	}
	req := &GenerateRequest{Messages: msgs, MaxTokens: 1000}
	orig := append([]ChatMessage(nil), msgs...)

	window := 1000 + 20000/guardCharsPerToken // ~20k chars of prompt budget
	got := fitRequestToWindow(req, window)

	if got == req {
		t.Fatal("expected a trimmed copy")
	}
	budget := (window - req.MaxTokens) * guardCharsPerToken
	if c := requestChars(got); c > budget {
		t.Errorf("trimmed request ~%d chars exceeds budget %d", c, budget)
	}
	if got.Messages[0].Content != "system prompt" {
		t.Error("system prompt dropped")
	}
	foundGoal := false
	for _, m := range got.Messages {
		if m.Content == "THE GOAL" {
			foundGoal = true
		}
		if strings.HasPrefix(m.Content, "old history") {
			t.Error("prior history should be dropped before tool rounds")
		}
	}
	if !foundGoal {
		t.Error("the turn's user message was dropped")
	}
	last := got.Messages[len(got.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "toolu_9" {
		t.Errorf("latest tool round not preserved: last = %+v", last.ToolCallID)
	}
	assertToolPairsIntact(t, got.Messages)
	// Caller's request untouched.
	if len(req.Messages) != len(orig) {
		t.Error("caller's Messages slice was modified")
	}
}

func TestFitRequestToWindow_TruncatesOversizedLatestResult(t *testing.T) {
	msgs := []ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "goal"}}
	msgs = append(msgs, toolRound(0, 200000)...)
	req := &GenerateRequest{Messages: msgs, MaxTokens: 1000}
	window := 1000 + 30000/guardCharsPerToken
	got := fitRequestToWindow(req, window)

	budget := (window - req.MaxTokens) * guardCharsPerToken
	if c := requestChars(got); c > budget {
		t.Errorf("trimmed request ~%d chars exceeds budget %d", c, budget)
	}
	res := got.Messages[len(got.Messages)-1]
	if !strings.HasSuffix(res.Content, toolResultTrimMarker) {
		t.Error("oversized tool result should be truncated with a marker")
	}
	if len(msgs[len(msgs)-1].Content) != 200000 {
		t.Error("caller's tool result content was modified")
	}
	assertToolPairsIntact(t, got.Messages)
}

// growingToolLoopEngine mimics tools.ExecutionEngine: each round appends
// the assistant tool_use turn plus a large tool result and calls the
// provider it was handed — with no trimming of its own.
type growingToolLoopEngine struct {
	rounds, resultChars int
}

func (e *growingToolLoopEngine) HandleToolCallFlow(ctx context.Context, provider Provider, req *GenerateRequest, resp *GenerateResponse) (ConversationResponse, error) {
	msgs := append([]ChatMessage(nil), req.Messages...)
	for i := 0; i < e.rounds; i++ {
		msgs = append(msgs, toolRound(i, e.resultChars)...)
		next := &GenerateRequest{Messages: msgs, Model: req.Model, Tools: req.Tools, MaxTokens: req.MaxTokens}
		r, err := provider.GenerateResponse(ctx, next)
		if err != nil {
			return nil, err
		}
		resp = r
	}
	return &SimpleConversationResponse{Content: resp.Content, Usage: &resp.Usage, Steps: e.rounds + 1}, nil
}

func TestToolLoopRoundsAreTrimmedBeforeEachCall(t *testing.T) {
	const window = 8000 // tokens; configured context_window override
	engine := &growingToolLoopEngine{rounds: 12, resultChars: 6000}
	router, err := NewRouterWithExecution(config.AIConfig{}, nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	mock := NewMockProvider("small")
	router.RegisterProvider("small", mock)
	router.providerMeta["small"] = ProviderMeta{Name: "small", Type: "openai", DefaultModel: "tiny-model", ContextWindow: window}

	responses := []MockResponse{{Content: "", ToolCalls: []ToolCall{{ID: "toolu_start", Name: "Bash", Args: map[string]interface{}{}}}}}
	for i := 0; i < engine.rounds; i++ {
		responses = append(responses, MockResponse{Content: fmt.Sprintf("round %d", i)})
	}
	mock.SetResponses(responses)

	_, err = router.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), "do a long task", "small", "tiny-model", nil)
	if err != nil {
		t.Fatalf("GenerateResponseWithToolsAndProgress: %v", err)
	}
	calls := mock.GetCalls()
	if len(calls) != engine.rounds+1 {
		t.Fatalf("provider calls = %d, want %d", len(calls), engine.rounds+1)
	}
	budget := (window - router.chainMaxTokens()) * guardCharsPerToken
	for i, c := range calls {
		if n := requestChars(c.Request); n > budget {
			t.Errorf("call %d sent ~%d chars, over the %d-char budget for a %d-token window", i, n, budget, window)
		}
		assertToolPairsIntact(t, c.Request.Messages)
	}
}
