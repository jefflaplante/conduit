package tools

// Tests for the conduit-8ba7 sunset review: the mid-chain progress injection
// is REMOVED. What remains is log-only, zero-token chain_depth telemetry.
//
// Behavior after removal:
//   - NO message is ever injected into the conversation at depth >= 20: no
//     request, at any depth, ever contains "Turn progress:" in any role.
//   - At the FIRST depth >= 20 in a chain, exactly one log line:
//     [ExecutionEngine] operation=chain_depth first_deep=20 max=<M> (conduit-8ba7)
//   - Depth milestones 30/40/50 keep logging
//     operation=chain_depth milestone=N max=<M> exactly as before.
//   - operation=refocus_inject never appears anywhere.
//   - Each chain (HandleToolCallFlow call) logs its own first_deep line.

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
)

// progressMarker is the content prefix the removed injection used to carry.
// No request may contain it, ever.
const progressMarker = "Turn progress: depth"

// captureLogs redirects the standard logger for one test and restores it after.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// countLogLines counts log lines containing sub.
func countLogLines(buf *bytes.Buffer, sub string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

func newDeepChainTestEngine(t *testing.T, maxChains int) (*ExecutionEngine, *ai.MockProvider) {
	t.Helper()
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "ok"}, nil
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, maxChains)
	provider := ai.NewMockProvider("test")
	return engine, provider
}

func deepChainRequest(goal string) (*ai.GenerateRequest, *ai.GenerateResponse) {
	initialReq := &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: goal}},
		Model:     "test-model",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}
	initialResp := &ai.GenerateResponse{
		Content:   "",
		ToolCalls: []ai.ToolCall{{ID: "c0", Name: "test_tool", Args: map[string]interface{}{}}},
	}
	return initialReq, initialResp
}

// runDeepChain drives one chain: rounds tool-calling responses then a final
// answer, returning the recorded provider calls.
func runDeepChain(t *testing.T, engine *ExecutionEngine, provider *ai.MockProvider, rounds int, goal string) []ai.MockCall {
	t.Helper()
	for i := 0; i < rounds; i++ {
		provider.AddResponse("", []ai.ToolCall{{ID: "c" + string(rune('a'+i%26)), Name: "test_tool", Args: map[string]interface{}{}}})
	}
	provider.AddResponse("final answer", nil)

	initialReq, initialResp := deepChainRequest(goal)
	if _, err := engine.HandleToolCallFlow(context.Background(), provider, initialReq, initialResp); err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}
	return provider.GetCalls()
}

// assertNoProgressContent fails if ANY recorded request carries the removed
// injection content, in any message role — the strong form of behavior 1.
func assertNoProgressContent(t *testing.T, calls []ai.MockCall) {
	t.Helper()
	for i, call := range calls {
		for _, msg := range call.Request.Messages {
			if strings.Contains(msg.Content, progressMarker) {
				t.Errorf("Request %d (role %q) carries removed injection content: %s", i, msg.Role, msg.Content)
			}
			if msg.Role == "system" && strings.Contains(msg.Content, "Original request:") {
				t.Errorf("Request %d carries system-role progress reminder: %s", i, msg.Content)
			}
		}
	}
}

// (1) Behavior 1: a chain well past depth 20 injects NOTHING. No request at
// any depth contains "Turn progress:".
func TestChainDepth_NoInjectionAtAnyDepth(t *testing.T) {
	engine, provider := newDeepChainTestEngine(t, 30)
	calls := runDeepChain(t, engine, provider, 25, "Please analyze this complex data")

	if len(calls) != 26 {
		t.Fatalf("Expected 26 provider calls, got %d", len(calls))
	}
	assertNoProgressContent(t, calls)
}

// (2) Behavior 2: first depth >= 20 logs first_deep telemetry exactly once,
// and the old refocus_inject line is gone.
func TestChainDepth_FirstDeepLoggedOnce(t *testing.T) {
	engine, provider := newDeepChainTestEngine(t, 25)
	buf := captureLogs(t)

	calls := runDeepChain(t, engine, provider, 21, "Deep chain telemetry")

	if len(calls) != 22 {
		t.Fatalf("Expected 22 provider calls, got %d", len(calls))
	}
	if got := countLogLines(buf, "operation=chain_depth first_deep=20 max=25"); got != 1 {
		t.Errorf("Expected exactly 1 first_deep=20 max=25 log line, got %d:\n%s", got, buf.String())
	}
	if got := countLogLines(buf, "operation=refocus_inject"); got != 0 {
		t.Errorf("operation=refocus_inject must never appear, got %d lines:\n%s", got, buf.String())
	}
	assertNoProgressContent(t, calls)
}

// (3) Behavior 2: milestone telemetry at 30/40/50 is unchanged — each logged
// exactly once, with the first_deep line alongside, and still no injection.
func TestChainDepth_Milestones30_40_50(t *testing.T) {
	engine, provider := newDeepChainTestEngine(t, 60)
	buf := captureLogs(t)

	calls := runDeepChain(t, engine, provider, 55, "Deep chain for milestones")

	if len(calls) != 56 {
		t.Fatalf("Expected 56 provider calls, got %d", len(calls))
	}
	for _, want := range []string{
		"operation=chain_depth first_deep=20 max=60",
		"operation=chain_depth milestone=30 max=60",
		"operation=chain_depth milestone=40 max=60",
		"operation=chain_depth milestone=50 max=60",
	} {
		if got := countLogLines(buf, want); got != 1 {
			t.Errorf("Expected exactly 1 log line %q, got %d:\n%s", want, got, buf.String())
		}
	}
	if got := countLogLines(buf, "operation=refocus_inject"); got != 0 {
		t.Errorf("operation=refocus_inject must never appear, got %d lines", got)
	}
	assertNoProgressContent(t, calls)
}

// (4) Per-chain reset: a second independent chain logs its own first_deep
// line — deep-chain distribution stays measurable per chain.
func TestChainDepth_PerChainReset(t *testing.T) {
	engine, provider := newDeepChainTestEngine(t, 25)
	buf := captureLogs(t)

	for i := 0; i < 2; i++ {
		runDeepChain(t, engine, provider, 21, "Two chains")
		provider.Reset() // isolate call records; log buffer accumulates across chains
	}

	if got := countLogLines(buf, "operation=chain_depth first_deep=20 max=25"); got != 2 {
		t.Errorf("Expected exactly 2 first_deep lines (one per chain), got %d:\n%s", got, buf.String())
	}
}
