package ai

import (
	"bytes"
	"io"
	"testing"
)

// errorReader is a reader that returns data then an error, simulating a broken stream.
type errorReader struct {
	data    []byte
	pos     int
	errOnce bool
}

func (r *errorReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		if !r.errOnce {
			r.errOnce = true
			return 0, io.ErrUnexpectedEOF
		}
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestStreamingResponse_PartialFlag(t *testing.T) {
	provider := &AnthropicProvider{
		model: "test-model",
	}

	// Simulate a stream that delivers partial content then errors.
	// The SSE format delivers a text delta, then the reader fails.
	sseData := `event: message_start
data: {"type": "message_start", "message": {"usage": {"input_tokens": 10}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello partial"}}

`

	// Use an errorReader that returns the SSE data then errors
	reader := &errorReader{data: []byte(sseData)}

	resp, err := provider.parseSSEStream(reader, nil)

	// We expect both an error AND a partial response
	if err == nil {
		t.Fatal("Expected an error from broken stream")
	}

	if resp == nil {
		t.Fatal("Expected a partial response with accumulated content")
	}

	if !resp.Partial {
		t.Error("Expected Partial flag to be true for mid-stream error response")
	}

	if resp.Content != "Hello partial" {
		t.Errorf("Expected partial content 'Hello partial', got '%s'", resp.Content)
	}
}

func TestStreamingResponse_CompleteHasNoPartialFlag(t *testing.T) {
	provider := &AnthropicProvider{
		model: "test-model",
	}

	// Simulate a complete SSE stream
	sseData := `event: message_start
data: {"type": "message_start", "message": {"usage": {"input_tokens": 10}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Complete response"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 5}}

event: message_stop
data: {"type": "message_stop"}

`

	reader := bytes.NewReader([]byte(sseData))
	resp, err := provider.parseSSEStream(reader, nil)

	if err != nil {
		t.Fatalf("Expected no error for complete stream, got: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected a response")
	}

	if resp.Partial {
		t.Error("Expected Partial flag to be false for complete response")
	}

	if resp.Content != "Complete response" {
		t.Errorf("Expected content 'Complete response', got '%s'", resp.Content)
	}
}

func TestStreamingResponse_EmptyPartialReturnsNilResponse(t *testing.T) {
	provider := &AnthropicProvider{
		model: "test-model",
	}

	// Simulate a stream that errors before any content is received
	reader := &errorReader{data: []byte{}}

	resp, err := provider.parseSSEStream(reader, nil)

	if err == nil {
		t.Fatal("Expected an error from broken stream")
	}

	// When there is no partial content, response should be nil
	if resp != nil {
		t.Error("Expected nil response when no partial content was accumulated")
	}
}
