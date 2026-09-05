package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
)

// bd-1k3o: finish_reason must be parsed in BOTH the non-stream path
// (parseOpenAIContent) and the SSE stream parser, so the tool loop can detect
// length-truncated finals instead of treating them as complete answers.

func TestParseOpenAIContent_FinishReason(t *testing.T) {
	cases := []struct {
		name string
		body map[string]interface{}
		want string
	}{
		{
			name: "length",
			body: map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"message":       map[string]interface{}{"content": "truncated mid sent"},
						"finish_reason": "length",
					},
				},
			},
			want: "length",
		},
		{
			name: "stop",
			body: map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"message":       map[string]interface{}{"content": "all done"},
						"finish_reason": "stop",
					},
				},
			},
			want: "stop",
		},
		{
			name: "tool_calls",
			body: map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"message":       map[string]interface{}{"content": ""},
						"finish_reason": "tool_calls",
					},
				},
			},
			want: "tool_calls",
		},
		{
			name: "content_filter",
			body: map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"message":       map[string]interface{}{"content": "I can"},
						"finish_reason": "content_filter",
					},
				},
			},
			want: "content_filter",
		},
		{
			name: "missing finish_reason",
			body: map[string]interface{}{
				"choices": []interface{}{
					map[string]interface{}{
						"message": map[string]interface{}{"content": "legacy"},
					},
				},
			},
			want: "",
		},
	}

	p := &OpenAIProvider{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, toolCalls, fr := p.parseOpenAIContent(openaiBodyWithFinishReason(t, tc.body))
			if fr != tc.want {
				t.Errorf("finish_reason: want %q, got %q", tc.want, fr)
			}
			// Sanity: content parsing unaffected
			if content == "" && tc.want != "tool_calls" && tc.want != "" {
				t.Errorf("content unexpectedly empty")
			}
			if len(toolCalls) != 0 {
				t.Errorf("unexpected tool calls: %d", len(toolCalls))
			}
		})
	}
}

// openaiBodyWithFinishReason re-encodes the body so parse gets the same
// shape the JSON decoder produces (map[string]interface{}).
func openaiBodyWithFinishReason(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestOpenAIProvider_GenerateResponse_FinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message":       map[string]interface{}{"content": "cut off at max toke"},
					"finish_reason": "length",
				},
			},
			"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 4000},
		})
	}))
	defer server.Close()

	p, _ := NewOpenAIProvider(config.ProviderConfig{
		Name:    "test",
		BaseURL: server.URL,
		Model:   "m",
	})

	resp, err := p.GenerateResponse(context.Background(), &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("expected finish_reason 'length', got %q", resp.FinishReason)
	}
	if resp.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestParseOpenAISSEStream_FinishReason_Length(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"truncated"},"index":0}]}`,
		`data: {"choices":[{"delta":{},"index":0,"finish_reason":"length"}]}`,
		`data: [DONE]`,
	}
	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(strings.NewReader(strings.Join(chunks, "\n")), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "truncated" {
		t.Errorf("expected content 'truncated', got %q", resp.Content)
	}
	if resp.FinishReason != "length" {
		t.Errorf("expected finish_reason 'length', got %q", resp.FinishReason)
	}
}

func TestParseOpenAISSEStream_FinishReason_Stop(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"complete"},"index":0}]}`,
		`data: {"choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(strings.NewReader(strings.Join(chunks, "\n")), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %q", resp.FinishReason)
	}
}

func TestParseOpenAISSEStream_FinishReason_Absent(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"hi"},"index":0}]}`,
		`data: [DONE]`,
	}
	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(strings.NewReader(strings.Join(chunks, "\n")), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "" {
		t.Errorf("expected empty finish_reason, got %q", resp.FinishReason)
	}
}

var _ = io.Discard
