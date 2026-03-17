package web

import (
	"testing"
)

func TestWebSearch_ShortAPIKey(t *testing.T) {
	// Previously the code did braveAPIKey[:8]+"..." which panics
	// if the key is shorter than 8 characters. The fix logs only
	// the key length, so this test verifies no panic occurs with
	// a short key.
	tool := &WebSearchTool{
		braveAPIKey: "abc", // Only 3 chars, would panic with [:8]
	}

	// Execute should reach the logging line and not panic.
	// It will return an error because we don't have a real HTTP client,
	// but the important thing is it doesn't panic.
	result, err := tool.Execute(nil, map[string]interface{}{
		"query": "test",
	})

	// We expect a non-nil result (the API key is set, so it won't hit
	// the "not configured" error). It might fail later due to nil httpClient
	// or nil context, but we just need to verify no panic from logging.
	if err != nil {
		// An actual error from nil context/client is fine.
		// The test is about not panicking.
		return
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestWebSearch_EmptyAPIKey(t *testing.T) {
	tool := &WebSearchTool{
		braveAPIKey: "",
	}

	result, err := tool.Execute(nil, map[string]interface{}{
		"query": "test",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.Success {
		t.Error("expected failure when API key is not configured")
	}
}
