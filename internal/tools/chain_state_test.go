package tools

// conduit-31jg.13: per-turn failure/pattern trackers; ephemeral, user-role
// loop guidance that never accumulates across depths or leaks across sessions.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

const pivotText = "Multiple failures detected with"
const thinkText = "Circular pattern detected"

func newChainTestEngine(t *testing.T) *ExecutionEngine {
	t.Helper()
	registry := NewMockRegistry()
	add := func(name string, fn func(ctx context.Context, args map[string]interface{}) (*ToolResult, error)) {
		registry.AddTool(&MockTool{
			name:        name,
			description: name,
			parameters:  map[string]interface{}{"type": "object"},
			executeFunc: fn,
		})
	}
	add("hard_fail", func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		return nil, errors.New("boom")
	})
	add("soft_fail", func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Success: false, Error: "not found"}, nil
	})
	add("ok_a", func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Success: true, Content: "a"}, nil
	})
	add("ok_b", func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Success: true, Content: "b"}, nil
	})
	return NewExecutionEngine(registry, 3, 30*time.Second, 25)
}

func chainReq(goal string) *ai.GenerateRequest {
	return &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: goal}},
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
	}
}

func toolCallResp(id, name string) *ai.GenerateResponse {
	return &ai.GenerateResponse{ToolCalls: []ai.ToolCall{{ID: id, Name: name, Args: map[string]interface{}{}}}}
}

// countIn counts messages in req containing needle, and reports whether any
// such message used the system role.
func countIn(req *ai.GenerateRequest, needle string) (n int, inSystem bool) {
	for _, m := range req.Messages {
		if strings.Contains(m.Content, needle) {
			n++
			if m.Role == "system" {
				inSystem = true
			}
		}
	}
	return
}

// Session 1 fails repeatedly while session 2 runs a clean chain on the SAME
// engine (as in production); session 2 must never see failure guidance —
// neither concurrently nor in a later turn.
func TestChainState_SessionsIsolated(t *testing.T) {
	engine := newChainTestEngine(t)

	run := func(provider *ai.MockProvider, first string) error {
		_, err := engine.HandleToolCallFlow(context.Background(), provider, chainReq("go"), toolCallResp("c0", first))
		return err
	}

	p1 := ai.NewMockProvider("s1")
	for i := 1; i <= 5; i++ {
		p1.AddResponse("", []ai.ToolCall{{ID: fmt.Sprintf("f%d", i), Name: "hard_fail", Args: map[string]interface{}{}}})
	}
	p1.AddResponse("gave up", nil)

	p2 := ai.NewMockProvider("s2")
	for i := 1; i <= 5; i++ {
		name := "ok_a"
		if i%2 == 0 {
			name = "ok_b"
		}
		p2.AddResponse("", []ai.ToolCall{{ID: fmt.Sprintf("o%d", i), Name: name, Args: map[string]interface{}{"i": i}}})
	}
	p2.AddResponse("done", nil)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- run(p1, "hard_fail") }()
	go func() { defer wg.Done(); errs <- run(p2, "ok_a") }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("HandleToolCallFlow: %v", err)
		}
	}

	// Session 2 runs again AFTER session 1 left hard_fail over threshold —
	// the old engine-wide tracker leaked here.
	p3 := ai.NewMockProvider("s2-later")
	p3.AddResponse("", []ai.ToolCall{{ID: "x1", Name: "ok_b", Args: map[string]interface{}{}}})
	p3.AddResponse("done", nil)
	if err := run(p3, "ok_a"); err != nil {
		t.Fatal(err)
	}

	total1 := 0
	for _, c := range p1.GetCalls() {
		n, _ := countIn(c.Request, pivotText)
		total1 += n
	}
	if total1 != 1 {
		t.Errorf("session 1: want exactly one pivot injection across the turn, got %d", total1)
	}
	for _, p := range []*ai.MockProvider{p2, p3} {
		for i, c := range p.GetCalls() {
			if n, _ := countIn(c.Request, pivotText); n != 0 {
				t.Errorf("%s call %d: failure injection leaked from another session", p.Name(), i)
			}
			if n, _ := countIn(c.Request, loopGuidanceMarker); n != 0 {
				t.Errorf("%s call %d: loop guidance leaked from another session", p.Name(), i)
			}
		}
	}
}

// A tool returning Success:false with a nil error counts toward the pivot
// threshold; across a 3-depth chain the pivot text appears exactly once in the
// final request, as a user-role message.
func TestChainState_SoftFailureCountsAndInjectsOnce(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("soft")
	p.AddResponse("", []ai.ToolCall{{ID: "s1", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("", []ai.ToolCall{{ID: "s2", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("final", nil)

	if _, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("find it"), toolCallResp("s0", "soft_fail")); err != nil {
		t.Fatal(err)
	}
	calls := p.GetCalls()
	if len(calls) != 3 {
		t.Fatalf("want 3 provider calls (depths 0..2), got %d", len(calls))
	}
	for i := 0; i < 2; i++ {
		if n, _ := countIn(calls[i].Request, pivotText); n != 0 {
			t.Errorf("depth %d: pivot injected before threshold", i)
		}
	}
	n, inSystem := countIn(calls[2].Request, pivotText)
	if n != 1 {
		t.Fatalf("final request: want pivot text exactly once, got %d", n)
	}
	if inSystem {
		t.Error("pivot guidance must be user-role, not system-role")
	}
	last := calls[2].Request.Messages[len(calls[2].Request.Messages)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, loopGuidanceMarker) {
		t.Errorf("guidance should be the trailing user message after tool results, got role=%q", last.Role)
	}
}

// Pivot guidance for a trigger is not carried into deeper requests, and
// further failures past the threshold do not re-inject it.
func TestChainState_PivotNotCarriedAcrossDepths(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("deep")
	for i := 1; i <= 5; i++ {
		p.AddResponse("", []ai.ToolCall{{ID: fmt.Sprintf("h%d", i), Name: "hard_fail", Args: map[string]interface{}{}}})
	}
	p.AddResponse("final", nil)
	if _, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), toolCallResp("h0", "hard_fail")); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, c := range p.GetCalls() {
		n, _ := countIn(c.Request, pivotText)
		total += n
		if n > 1 {
			t.Errorf("call %d: pivot text accumulated (%d copies)", i, n)
		}
	}
	if total != 1 {
		t.Errorf("want one pivot across the whole turn, got %d", total)
	}
	if n, _ := countIn(p.GetCalls()[5].Request, pivotText); n != 0 {
		t.Error("pivot guidance carried into a deeper request")
	}
}

// Circular pattern: the think-step lands once, user-role, after the results
// that closed the loop, and is stripped from deeper requests.
func TestChainState_ThinkStepOnceAndStripped(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("loop")
	// Calls: a b a b a b (6 executions: depths 0..5) → detected at depth 5.
	for i := 1; i <= 5; i++ {
		name := "ok_a"
		if i%2 == 1 {
			name = "ok_b"
		}
		p.AddResponse("", []ai.ToolCall{{ID: fmt.Sprintf("l%d", i), Name: name, Args: map[string]interface{}{}}})
	}
	p.AddResponse("", []ai.ToolCall{{ID: "l6", Name: "ok_a", Args: map[string]interface{}{"different": true}}})
	p.AddResponse("final", nil)

	if _, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("loop"), toolCallResp("l0", "ok_a")); err != nil {
		t.Fatal(err)
	}
	calls := p.GetCalls()
	if len(calls) != 7 {
		t.Fatalf("want 7 provider calls, got %d", len(calls))
	}
	total := 0
	for i, c := range calls {
		n, inSystem := countIn(c.Request, thinkText)
		total += n
		if inSystem {
			t.Errorf("call %d: think-step sent as system role", i)
		}
		if i == 5 && n != 1 {
			t.Errorf("depth 5 request: want think-step once, got %d", n)
		}
		if i != 5 && n != 0 {
			t.Errorf("call %d: think-step present outside its trigger round (%d)", i, n)
		}
	}
	if total != 1 {
		t.Errorf("want one think-step across the turn, got %d", total)
	}
}

// SPAR promotion still fires from per-turn trackers, attributed to the
// originating session.
func TestChainState_SPARHooksFire(t *testing.T) {
	engine := newChainTestEngine(t)
	var mu sync.Mutex
	var pivots, circulars []string
	engine.SetPivotHook(func(ctx context.Context, tool string, n int, lastErr string) {
		mu.Lock()
		defer mu.Unlock()
		pivots = append(pivots, types.RequestSessionKey(ctx)+"/"+tool+"/"+lastErr)
	})
	engine.SetCircularHook(func(ctx context.Context, pattern, hash string) {
		mu.Lock()
		defer mu.Unlock()
		circulars = append(circulars, types.RequestSessionKey(ctx)+"/"+pattern)
	})
	ctx := types.WithRequestContext(context.Background(), "chan", "user", "sess-A")

	p := ai.NewMockProvider("spar")
	p.AddResponse("", []ai.ToolCall{{ID: "s1", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("", []ai.ToolCall{{ID: "s2", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("final", nil)
	if _, err := engine.HandleToolCallFlow(ctx, p, chainReq("x"), toolCallResp("s0", "soft_fail")); err != nil {
		t.Fatal(err)
	}

	p2 := ai.NewMockProvider("spar-loop")
	for i := 1; i <= 5; i++ {
		name := "ok_a"
		if i%2 == 1 {
			name = "ok_b"
		}
		p2.AddResponse("", []ai.ToolCall{{ID: fmt.Sprintf("l%d", i), Name: name, Args: map[string]interface{}{}}})
	}
	p2.AddResponse("final", nil)
	if _, err := engine.HandleToolCallFlow(ctx, p2, chainReq("y"), toolCallResp("l0", "ok_a")); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pivots) != 1 || pivots[0] != "sess-A/soft_fail/not found" {
		t.Errorf("pivot hook: got %v", pivots)
	}
	if len(circulars) != 1 || circulars[0] != "sess-A/ok_a -> ok_b" {
		t.Errorf("circular hook: got %v", circulars)
	}
}

// End-to-end through the real Anthropic request builder: the guidance must
// not be hoisted into the system blocks (cache prefix stays stable), and it
// must follow the tool_result it relates to.
func TestChainState_GuidanceNotInAnthropicSystem(t *testing.T) {
	engine := newChainTestEngine(t)
	p := ai.NewMockProvider("soft")
	p.AddResponse("", []ai.ToolCall{{ID: "s1", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("", []ai.ToolCall{{ID: "s2", Name: "soft_fail", Args: map[string]interface{}{}}})
	p.AddResponse("final", nil)
	req := chainReq("find it")
	req.Messages = append([]ai.ChatMessage{{Role: "system", Content: "You are Conduit."}}, req.Messages...)
	if _, err := engine.HandleToolCallFlow(context.Background(), p, req, toolCallResp("s0", "soft_fail")); err != nil {
		t.Fatal(err)
	}
	finalReq := p.GetCalls()[2].Request

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "m", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
			"content":     []interface{}{map[string]interface{}{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()
	ap, err := ai.NewAnthropicProvider(config.ProviderConfig{Name: "t", APIKey: "sk-ant-api03-test", BaseURL: srv.URL, Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ap.GenerateResponse(context.Background(), finalReq); err != nil {
		t.Fatal(err)
	}

	var sent struct {
		System   json.RawMessage          `json:"system"`
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sent.System), pivotText) {
		t.Fatalf("guidance hoisted into system blocks: %s", sent.System)
	}
	lastMsg := sent.Messages[len(sent.Messages)-1]
	prevMsg := sent.Messages[len(sent.Messages)-2]
	if lastMsg["role"] != "user" || !strings.Contains(fmt.Sprint(lastMsg["content"]), pivotText) {
		t.Errorf("guidance should be the trailing user message, got %v", lastMsg)
	}
	if prevMsg["role"] != "user" || !strings.Contains(fmt.Sprint(prevMsg["content"]), "tool_result") {
		t.Errorf("guidance should directly follow the tool_result user turn, got %v", prevMsg)
	}
}
