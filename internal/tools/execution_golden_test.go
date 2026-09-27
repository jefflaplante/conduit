package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"conduit/internal/ai"
)

// Golden trace for the tool loop (conduit-31jg.37).
//
// The fixture in testdata/toolloop_golden.json was captured from the
// recursive handleToolCallFlowRecursive BEFORE it was rewritten as a loop.
// Every request the engine sends to the provider (including EmptyGuard
// retries and length auto-continues) is snapshotted at call time and must
// match the fixture exactly, as must the final ConversationResponse.
//
// Regenerate (only for an intended behavior change, documented in the
// commit): go test ./internal/tools -run TestToolLoopGoldenTrace -update-golden

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/toolloop_golden.json")

const goldenPath = "testdata/toolloop_golden.json"

// scriptStep is one scripted provider reply.
type scriptStep struct {
	resp *ai.GenerateResponse
	err  error
}

// scriptedProvider replays a fixed script and records a deep snapshot of
// every request at call time, plus the request pointer and the Messages
// slice header it carried (for the no-retroactive-mutation check).
type scriptedProvider struct {
	t      *testing.T
	mu     sync.Mutex
	script []scriptStep
	calls  int
	snaps  [][]byte
	ptrs   []*ai.GenerateRequest
	views  [][]ai.ChatMessage // req.Messages as passed, sharing its backing array
	vsnaps [][]byte           // json of views at call time
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) GenerateResponse(_ context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		p.t.Fatalf("marshal request: %v", err)
	}
	p.snaps = append(p.snaps, b)
	p.ptrs = append(p.ptrs, req)
	vb, _ := json.Marshal(req.Messages)
	p.views = append(p.views, req.Messages)
	p.vsnaps = append(p.vsnaps, vb)
	if p.calls >= len(p.script) {
		p.t.Fatalf("provider called %d times, script has %d steps", p.calls+1, len(p.script))
	}
	s := p.script[p.calls]
	p.calls++
	if s.err != nil {
		return nil, s.err
	}
	cp := *s.resp // hand out a copy: callers may mutate Content/Usage
	return &cp, nil
}

func tcResp(prompt int, content string, calls ...string) *ai.GenerateResponse {
	r := &ai.GenerateResponse{Content: content, FinishReason: "tool_calls", Usage: ai.Usage{PromptTokens: prompt, CompletionTokens: 1}}
	for i, name := range calls {
		r.ToolCalls = append(r.ToolCalls, ai.ToolCall{
			ID:   fmt.Sprintf("tc-%d-%d", prompt, i),
			Name: name,
			Args: map[string]interface{}{"n": prompt, "i": i},
		})
	}
	return r
}

func textResp(prompt int, content, finish string) *ai.GenerateResponse {
	return &ai.GenerateResponse{Content: content, FinishReason: finish, Usage: ai.Usage{PromptTokens: prompt, CompletionTokens: 1}}
}

type goldenScenario struct {
	name      string
	maxChains int
	initial   *ai.GenerateResponse
	script    []scriptStep
}

func goldenScenarios() []goldenScenario {
	// deep: 22 alternating rounds so the conduit-8ba7 refocus (depth 20) and
	// circular-pattern think-step both fire, then a plain final.
	var deep []scriptStep
	for i := 1; i <= 22; i++ {
		name := "ok_a"
		if i%2 == 0 {
			name = "ok_b"
		}
		r := tcResp(100+i, "", name)
		r.ToolCalls[0].Args = map[string]interface{}{} // identical args: circular detection
		deep = append(deep, scriptStep{resp: r})
	}
	deep = append(deep, scriptStep{resp: textResp(200, "all done", "stop")})

	return []goldenScenario{
		{
			// Five tool rounds: guard retry at depth 0, pivot guidance at
			// depth 2 (3rd hard failure), a length-truncated reply there that
			// auto-continues into tool calls, then the depth-limit stop.
			name:      "chain",
			maxChains: 5,
			initial:   tcResp(1, "starting", "hard_fail"),
			script: []scriptStep{
				{resp: textResp(2, "", "stop")},                  // depth0: raw-empty
				{resp: tcResp(3, "", "hard_fail")},               // depth0: guard retry
				{resp: tcResp(4, "again", "hard_fail", "ok_a")},  // depth1
				{resp: textResp(5, "partial answer ", "length")}, // depth2 (with pivot guidance): truncated
				{resp: tcResp(6, "continued", "ok_b")},           // depth2 continue → tool calls
				{resp: tcResp(7, "", "ok_a")},                    // depth3
				{resp: tcResp(8, "nearly", "ok_b")},              // depth4 → depth5 hits max
			},
		},
		{
			// Final reply truncated twice, completed on the 2nd continue.
			name:      "final_continue",
			maxChains: 25,
			initial:   tcResp(1, "", "ok_a"),
			script: []scriptStep{
				{resp: textResp(2, "frag1 ", "length")},
				{resp: textResp(3, "frag2 ", "length")},
				{resp: textResp(4, "end", "stop")},
			},
		},
		{
			// Final reply truncated and the continuation errors.
			name:      "final_continue_error",
			maxChains: 25,
			initial:   tcResp(1, "", "ok_a"),
			script: []scriptStep{
				{resp: textResp(2, "only fragment", "length")},
				{err: errors.New("upstream 529")},
			},
		},
		{
			// Final still truncated after the continue budget.
			name:      "final_continue_exhausted",
			maxChains: 25,
			initial:   tcResp(1, "", "ok_b"),
			script: []scriptStep{
				{resp: textResp(2, "a ", "length")},
				{resp: textResp(3, "b ", "length")},
				{resp: textResp(4, "c", "length")},
			},
		},
		{
			name:      "deep_refocus",
			maxChains: 25,
			initial:   tcResp(100, "", "ok_a"),
			script:    deep,
		},
	}
}

type goldenResult struct {
	Content    string            `json:"content"`
	Usage      *ai.Usage         `json:"usage"`
	Steps      int               `json:"steps"`
	ChainDepth int               `json:"chain_depth"`
	ToolIDs    []string          `json:"tool_result_ids"`
	Requests   []json.RawMessage `json:"requests"`
}

func runGoldenScenario(t *testing.T, sc goldenScenario) goldenResult {
	t.Helper()
	engine := newChainTestEngine(t)
	engine.maxChains = sc.maxChains
	p := &scriptedProvider{t: t, script: sc.script}

	// Spare capacity: the engine must never write into the caller's array.
	callerMsgs := make([]ai.ChatMessage, 2, 16)
	callerMsgs[0] = ai.ChatMessage{Role: "system", Content: "sys"}
	callerMsgs[1] = ai.ChatMessage{Role: "user", Content: "do the " + sc.name + " thing"}
	callerBefore, _ := json.Marshal(callerMsgs[:cap(callerMsgs)])
	req := &ai.GenerateRequest{
		Messages:  callerMsgs,
		Model:     "claude-sonnet-4-6",
		MaxTokens: 1024,
		Tools:     []ai.Tool{{Name: "ok_a", Description: "a"}},
	}
	initial := *sc.initial

	resp, err := engine.HandleToolCallFlow(context.Background(), p, req, &initial)
	if err != nil {
		t.Fatalf("%s: HandleToolCallFlow: %v", sc.name, err)
	}
	if p.calls != len(sc.script) {
		t.Fatalf("%s: provider calls = %d, script has %d", sc.name, p.calls, len(sc.script))
	}
	if after, _ := json.Marshal(callerMsgs[:cap(callerMsgs)]); !bytes.Equal(after, callerBefore) {
		t.Errorf("%s: engine wrote into the caller's message array", sc.name)
	}
	// No retroactive mutation: every message slice the provider was handed
	// still holds, after the turn, exactly what it held at call time — no
	// later append (loop or auto-continue) wrote into a slot it covers.
	// (The request's Messages field itself may be re-sliced afterwards: the
	// auto-continue helper grows it and rewinds it after a failed call.)
	for i, view := range p.views {
		if got, _ := json.Marshal(view); !bytes.Equal(got, p.vsnaps[i]) {
			t.Errorf("%s: request %d's messages were overwritten after the call:\n got %s\nwant %s", sc.name, i, got, p.vsnaps[i])
		}
	}

	out := goldenResult{
		Content:    resp.Content,
		Usage:      resp.Usage,
		Steps:      resp.Steps,
		ChainDepth: resp.ChainDepth,
	}
	for _, r := range resp.ToolResults {
		out.ToolIDs = append(out.ToolIDs, r.ToolCall.ID)
	}
	for _, s := range p.snaps {
		out.Requests = append(out.Requests, json.RawMessage(s))
	}
	return out
}

func TestToolLoopGoldenTrace(t *testing.T) {
	got := map[string]goldenResult{}
	for _, sc := range goldenScenarios() {
		got[sc.name] = runGoldenScenario(t, sc)
	}

	if *updateGolden {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden to create): %v", err)
	}
	var want map[string]goldenResult
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for _, sc := range goldenScenarios() {
		g, w := got[sc.name], want[sc.name]
		if len(g.Requests) != len(w.Requests) {
			t.Errorf("%s: %d provider requests, golden has %d", sc.name, len(g.Requests), len(w.Requests))
		}
		for i := 0; i < len(g.Requests) && i < len(w.Requests); i++ {
			if !jsonEqual(t, g.Requests[i], w.Requests[i]) {
				t.Errorf("%s: request %d differs\n got %s\nwant %s", sc.name, i, g.Requests[i], w.Requests[i])
			}
		}
		g.Requests, w.Requests = nil, nil
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		if !bytes.Equal(gb, wb) {
			t.Errorf("%s: final response differs\n got %s\nwant %s", sc.name, gb, wb)
		}
	}
}

// jsonEqual compares two JSON documents after decoding both into
// ai.GenerateRequest (so formatting differences don't matter).
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var ra, rb ai.GenerateRequest
	if err := json.Unmarshal(a, &ra); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rb); err != nil {
		t.Fatal(err)
	}
	ab, _ := json.Marshal(ra)
	bb, _ := json.Marshal(rb)
	return bytes.Equal(ab, bb)
}

// conduit-31jg.37: rounds append to one history slice; without guidance or
// auto-continue, every round's request is a view of the same backing array
// (no per-depth history copy), capped so nothing can append into it.
func TestToolLoop_NoHistoryCopyPerRound(t *testing.T) {
	engine := newChainTestEngine(t)
	var script []scriptStep
	for i := 2; i <= 5; i++ {
		script = append(script, scriptStep{resp: tcResp(i, "", "ok_a")})
	}
	script = append(script, scriptStep{resp: textResp(9, "done", "stop")})
	p := &scriptedProvider{t: t, script: script}

	resp, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), tcResp(1, "", "ok_b"))
	if err != nil || resp.Content != "done" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	arrays := map[*ai.ChatMessage]bool{}
	for i, req := range p.ptrs {
		if len(req.Messages) != cap(req.Messages) {
			t.Errorf("request %d: Messages not capacity-capped (len %d cap %d)", i, len(req.Messages), cap(req.Messages))
		}
		arrays[&req.Messages[0]] = true
	}
	if len(arrays) != 1 {
		t.Errorf("%d backing arrays across %d rounds, want 1 (history copied per round)", len(arrays), len(p.ptrs))
	}
}

// conduit-31jg.51: a truncated reply whose continuation ends in tool calls
// must not re-add the earlier fragment to history (it is already there as
// its own assistant turn), and the round's usage is counted exactly once.
func TestToolLoop_AutoContinueIntoToolCalls_NoDuplicateText(t *testing.T) {
	engine := newChainTestEngine(t)
	p := &scriptedProvider{t: t, script: []scriptStep{
		{resp: textResp(10, "FRAGMENT-ONE ", "length")},
		{resp: tcResp(20, "then tools", "ok_b")},
		{resp: textResp(40, "done", "stop")},
	}}
	resp, err := engine.HandleToolCallFlow(context.Background(), p, chainReq("go"), tcResp(1, "", "ok_a"))
	if err != nil {
		t.Fatal(err)
	}
	last := p.ptrs[len(p.ptrs)-1]
	n := 0
	for _, m := range last.Messages {
		n += strings.Count(m.Content, "FRAGMENT-ONE")
	}
	if n != 1 {
		t.Errorf("fragment appears %d times in the next round's history, want 1: %+v", n, last.Messages)
	}
	if resp.Usage.PromptTokens != 1+10+20+40 {
		t.Errorf("turn prompt tokens = %d, want %d (each call once)", resp.Usage.PromptTokens, 71)
	}
}
