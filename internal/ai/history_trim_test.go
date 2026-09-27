package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.63: once history reached its budget every trimming layer
// dropped the oldest message on every turn, so the message prefix — and
// every history cache breakpoint — changed each turn. Trims now cut down to
// a low-water mark in whole user turns and leave the prefix alone between
// trims.

// normalizeAPIMessage removes cache_control markers and normalizes string
// content to a single text block (the API treats the two forms the same),
// so only the cached bytes are compared.
func normalizeAPIMessage(t *testing.T, m interface{}) string {
	t.Helper()
	var strip func(v interface{}) interface{}
	strip = func(v interface{}) interface{} {
		switch x := v.(type) {
		case map[string]interface{}:
			out := map[string]interface{}{}
			for k, vv := range x {
				if k != "cache_control" {
					out[k] = strip(vv)
				}
			}
			return out
		case []interface{}:
			out := make([]interface{}, len(x))
			for i, vv := range x {
				out[i] = strip(vv)
			}
			return out
		}
		return v
	}
	msg := strip(m).(map[string]interface{})
	if s, ok := msg["content"].(string); ok {
		msg["content"] = []interface{}{map[string]interface{}{"type": "text", "text": s}}
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func capturedMessages(t *testing.T, raw *[]byte) []string {
	t.Helper()
	body := capturedRequest(t, raw)
	arr, _ := body["messages"].([]interface{})
	out := make([]string, len(arr))
	for i, m := range arr {
		out[i] = normalizeAPIMessage(t, m)
	}
	return out
}

func isPrefix(prev, cur []string) bool {
	if len(prev) > len(cur) {
		return false
	}
	for i := range prev {
		if prev[i] != cur[i] {
			return false
		}
	}
	return true
}

func newHistoryRouter(t *testing.T, hc config.HistoryConfig) (*Router, *sessions.Store, *sessions.Session) {
	t.Helper()
	store, err := sessions.NewStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	r, err := NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.SetSessionStore(store)
	r.SetHistoryConfig(&hc)
	s, err := store.GetOrCreateSession("u1", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	return r, store, s
}

func turnText(role string, n, size int) string {
	head := fmt.Sprintf("%s turn %d: ", role, n)
	return head + strings.Repeat(string(rune('a'+n%26)), size-len(head))
}

func addTurn(t *testing.T, store *sessions.Store, key string, n int) {
	t.Helper()
	if _, err := store.AddMessage(key, "user", turnText("user", n, 100), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(key, "assistant", turnText("assistant", n, 280), nil); err != nil {
		t.Fatal(err)
	}
}

// A simulated 30-turn session at budget, through the real request path:
// history from the store → trimRequestToFitContext → fitRequestToWindow →
// Anthropic provider → httptest capture. Between trims the message prefix
// sent to the provider must be byte-identical to the previous request.
func TestHistoryTrim_ThirtyTurnSessionPrefixStable(t *testing.T) {
	hc := config.HistoryConfig{MaxTokens: 4000, MinMessages: 4, MaxMessages: 1000, CharsPerToken: 4}
	r, store, sess := newHistoryRouter(t, hc)

	// Pre-fill to the budget (~400 chars per turn vs a 16000-char budget).
	for n := 0; n < 40; n++ {
		addTurn(t, store, sess.Key, n)
	}

	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, allCaching))
	if err != nil {
		t.Fatal(err)
	}
	p.isOAuth, p.authCfg = false, nil

	static := strings.Repeat("static instructions ", 500)
	var prev []string
	var prevSystem interface{}
	trims, stable := 0, 0
	for turn := 40; turn < 70; turn++ {
		userText := turnText("user", turn, 100)
		stored, err := store.AddMessage(sess.Key, "user", userText, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithCurrentUserMessageID(context.Background(), stored.ID)
		msgs, err := r.buildChatMessagesWithSystemPrompt(ctx, sess, userText, []SystemBlock{
			{Type: "text", Text: static},
			{Type: "text", Text: fmt.Sprintf("Current time: 09:%02d", turn), Dynamic: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 1000, Messages: msgs}
		trimRequestToFitContext(req, 200000)
		if _, err := p.GenerateResponse(context.Background(), fitRequestToWindow(req, 200000)); err != nil {
			t.Fatal(err)
		}
		cur := capturedMessages(t, raw)
		system := systemBlocksOf(t, capturedRequest(t, raw))[0]["text"]

		if prev != nil {
			if system != prevSystem {
				t.Fatalf("turn %d: static system block changed", turn)
			}
			if isPrefix(prev, cur) {
				stable++
			} else {
				trims++
			}
		}
		if !strings.Contains(cur[0], `"role":"user"`) {
			t.Errorf("turn %d: history does not start at a user turn: %.80s", turn, cur[0])
		}
		prev, prevSystem = cur, system
		if _, err := store.AddMessage(sess.Key, "assistant", turnText("assistant", turn, 280), nil); err != nil {
			t.Fatal(err)
		}
	}

	// 29 transitions; ~400 new chars per turn against a 4000-char hysteresis
	// band means a re-cut about every 10 turns. The old behavior re-cut on
	// every single turn.
	t.Logf("30 turns at budget: %d stable prefixes, %d trims", stable, trims)
	if trims == 0 {
		t.Error("expected at least one trim: the session is at its budget")
	}
	if trims > 4 {
		t.Errorf("prefix changed on %d of 29 turns; want <= 4 with hysteresis", trims)
	}
}

// A trim takes the history down to the low-water mark (75% of the budget),
// cut at a user turn.
func TestHistoryTrim_TrimReachesLowWater(t *testing.T) {
	hc := config.HistoryConfig{MaxTokens: 4000, MinMessages: 4, MaxMessages: 1000, CharsPerToken: 4}
	r, store, sess := newHistoryRouter(t, hc)
	budget := hc.MaxTokens * hc.CharsPerToken
	turnChars := 100 + 280 + len("user") + len("assistant") + 20

	var lastLen int
	trimmed := false
	for n := 0; n < 80 && !trimmed; n++ {
		addTurn(t, store, sess.Key, n)
		got, err := r.getRecentMessagesTokenAware(sess)
		if err != nil {
			t.Fatal(err)
		}
		chars := 0
		for _, m := range got {
			chars += historyMsgChars(m)
		}
		if chars > budget {
			t.Fatalf("turn %d: history %d chars over the %d budget", n, chars, budget)
		}
		if lastLen > 0 && len(got) < lastLen {
			trimmed = true
			low := int(float64(budget) * defaultHistoryLowWater)
			if chars > low || chars < low-turnChars {
				t.Errorf("after trim history is %d chars; want within one turn below the low-water mark %d", chars, low)
			}
			if got[0].Role != "user" {
				t.Errorf("trim cut mid-turn: first message role %q", got[0].Role)
			}
		}
		lastLen = len(got)
	}
	if !trimmed {
		t.Fatal("history never trimmed")
	}
}

// Hitting the MaxMessages fetch cap re-cuts to the low-water count instead
// of sliding the window one message per turn.
func TestHistoryTrim_MessageCapHysteresis(t *testing.T) {
	hc := config.HistoryConfig{MaxTokens: 1 << 20, MinMessages: 4, MaxMessages: 40, CharsPerToken: 4}
	r, store, sess := newHistoryRouter(t, hc)
	firsts := map[string]bool{}
	for n := 0; n < 60; n++ {
		addTurn(t, store, sess.Key, n)
		got, err := r.getRecentMessagesTokenAware(sess)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > hc.MaxMessages {
			t.Fatalf("turn %d: %d messages over the cap", n, len(got))
		}
		if got[0].Role != "user" {
			t.Fatalf("turn %d: window starts with %q", n, got[0].Role)
		}
		firsts[got[0].ID] = true
	}
	// 120 messages through a 40-message window re-cut to 30: ~10 cuts, not ~80.
	if len(firsts) > 12 {
		t.Errorf("window start moved %d times; want <= 12", len(firsts))
	}
}

// The context-window guard is stateless: with a stable prefix and a growing
// tail it must keep dropping the same units until another chunk is needed.
func TestTrimRequestToFitContext_HysteresisStablePrefix(t *testing.T) {
	const window = 16384 // tokens
	var history []ChatMessage
	firsts := map[string]bool{}
	for n := 0; n < 60; n++ {
		history = append(history,
			ChatMessage{Role: "user", Content: turnText("user", n, 1000)},
			ChatMessage{Role: "assistant", Content: turnText("assistant", n, 3000)})
		msgs := append([]ChatMessage{{Role: "system", Content: "system"}}, history...)
		msgs = append(msgs, ChatMessage{Role: "user", Content: "current"})
		req := &GenerateRequest{Model: "claude-sonnet-4-6", MaxTokens: 4000, Messages: msgs}
		trimRequestToFitContext(req, window)
		if req.Messages[1].Role != "user" {
			t.Fatalf("call %d: history starts with %q", n, req.Messages[1].Role)
		}
		if last := req.Messages[len(req.Messages)-1]; last.Content != "current" {
			t.Fatalf("call %d: current user message dropped", n)
		}
		if requestChars(req) > (window-4000)*4 {
			t.Fatalf("call %d: request over budget", n)
		}
		firsts[req.Messages[1].Content] = true
	}
	// ~4000 chars per turn, ~12400-char chunk: a re-cut every ~3 turns once
	// over the ~49.5k-char budget (turn 12), vs every turn before.
	if len(firsts) > 20 {
		t.Errorf("history start moved %d times in 60 calls; want <= 20", len(firsts))
	}
}

// Trimming never separates a tool_use from its tool_result, in either
// guard, even when prior history contains tool rounds.
func TestHistoryTrim_NoOrphanedToolPairs(t *testing.T) {
	var msgs []ChatMessage
	msgs = append(msgs, ChatMessage{Role: "system", Content: "sys"})
	for n := 0; n < 12; n++ {
		msgs = append(msgs, ChatMessage{Role: "user", Content: turnText("user", n, 500)})
		msgs = append(msgs, toolRound(n, 1500)...)
		msgs = append(msgs, ChatMessage{Role: "assistant", Content: turnText("assistant", n, 500)})
	}
	msgs = append(msgs, ChatMessage{Role: "user", Content: "THE GOAL"})
	for i := 100; i < 106; i++ {
		msgs = append(msgs, toolRound(i, 2000)...)
	}

	for _, window := range []int{1000 + 8000, 1000 + 12000, 1000 + 20000} {
		got := fitRequestToWindow(&GenerateRequest{Messages: msgs, MaxTokens: 1000}, window)
		assertToolPairsIntact(t, got.Messages)
		if got.Messages[0].Content != "sys" {
			t.Errorf("window %d: system message dropped", window)
		}
		if got.Messages[1].Role != "user" {
			t.Errorf("window %d: history starts mid-turn with %q", window, got.Messages[1].Role)
		}
		found := false
		for _, m := range got.Messages {
			found = found || m.Content == "THE GOAL"
		}
		if !found {
			t.Errorf("window %d: the turn's user message was dropped", window)
		}

		// First request of a turn shape: [system, history..., user].
		first := append([]ChatMessage(nil), msgs[:len(msgs)-12]...)
		req := &GenerateRequest{Messages: first, MaxTokens: 1000}
		trimRequestToFitContext(req, window)
		assertToolPairsIntact(t, req.Messages)
		if req.Messages[len(req.Messages)-1].Content != "THE GOAL" {
			t.Errorf("window %d: current user message dropped", window)
		}
		if len(req.Messages) > 2 && req.Messages[1].Role != "user" {
			t.Errorf("window %d: trimmed history starts with %q", window, req.Messages[1].Role)
		}
	}
}

func TestSnappedDropCount(t *testing.T) {
	units := []int{10, 10, 10, 10, 10, 10, 10, 10}
	if got := snappedDropCount(units, 100, 20); got != 0 {
		t.Errorf("fits: got %d", got)
	}
	// Over by 1 → drop to the first 20-char boundary (2 units).
	if got := snappedDropCount(units, 79, 20); got != 2 {
		t.Errorf("over by 1: got %d, want 2", got)
	}
	// Over by 20 → still 2 units: same drop point.
	if got := snappedDropCount(units, 60, 20); got != 2 {
		t.Errorf("over by 20: got %d, want 2", got)
	}
	if got := snappedDropCount(units, 59, 20); got != 4 {
		t.Errorf("over by 21: got %d, want 4", got)
	}
	if got := snappedDropCount(units, -5, 20); got != len(units) {
		t.Errorf("no room: got %d, want all", got)
	}
}
