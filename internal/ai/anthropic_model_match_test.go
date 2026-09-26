package ai

import (
	"context"
	"testing"
)

// conduit-31jg.16: the model-parity check compared strings.Split(model,
// "-2025")[0] and, on a "mismatch", replaced the real reply (content AND tool
// calls) with a warning string returned as a SUCCESSFUL response. Aliases that
// resolve to 2024/2026 snapshots tripped it.

func TestAnthropicModelsMatch(t *testing.T) {
	cases := []struct {
		requested, got string
		want           bool
	}{
		{"claude-sonnet-4-6", "claude-sonnet-4-6", true},
		{"claude-sonnet-4-6", "claude-sonnet-4-6-20260217", true},     // alias → 2026 snapshot
		{"claude-3-5-sonnet-latest", "claude-3-5-sonnet-20241022", true}, // -latest alias → 2024 snapshot
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514", true},
		{"claude-opus-4-5", "claude-opus-4-5-20251101", true},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5", true}, // dated request, undated echo
		{"claude-sonnet-4-6", "claude-opus-4-6", false},
		{"claude-sonnet-4", "claude-sonnet-4-6", false}, // family prefix is NOT the same model
		{"claude-haiku-4-5", "claude-sonnet-4-5-20250929", false},
	}
	for _, c := range cases {
		if got := anthropicModelsMatch(c.requested, c.got); got != c.want {
			t.Errorf("anthropicModelsMatch(%q, %q) = %v, want %v", c.requested, c.got, got, c.want)
		}
	}
}

func TestAnthropicGenerateResponse_MismatchedDatedModelReturnsRealContent(t *testing.T) {
	resp := anthropicMsg("tool_use",
		textBlock("real answer"),
		toolUseBlock("toolu_1", "Read", map[string]interface{}{"path": "/tmp/x"}),
	)
	resp["model"] = "claude-sonnet-4-6-20260217" // alias resolved to a 2026 snapshot
	srv, _ := anthropicJSONServer(t, resp)
	p := newTestAnthropic(t, srv.URL)

	got, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Model:     "claude-sonnet-4-6",
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if got.Content != "real answer" {
		t.Errorf("content = %q, want the real reply", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "Read" {
		t.Errorf("tool calls dropped: %+v", got.ToolCalls)
	}
}

func TestAnthropicGenerateResponse_GenuineMismatchIsLogOnly(t *testing.T) {
	resp := anthropicMsg("end_turn", textBlock("answer from another model"))
	resp["model"] = "claude-opus-4-6"
	srv, _ := anthropicJSONServer(t, resp)
	p := newTestAnthropic(t, srv.URL)

	got, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Model:     "claude-sonnet-4-6",
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if got.Content != "answer from another model" {
		t.Errorf("content = %q, want the real reply (mismatch must be log-only)", got.Content)
	}
}
