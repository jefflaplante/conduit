package logging

import (
	"context"
	"testing"
)

func TestWithRequestID(t *testing.T) {
	t.Run("with provided ID", func(t *testing.T) {
		ctx := WithRequestID(context.Background(), "test-id-123")
		id := RequestIDFromContext(ctx)
		if id != "test-id-123" {
			t.Errorf("expected 'test-id-123', got: %s", id)
		}
	})

	t.Run("with empty ID generates random", func(t *testing.T) {
		ctx := WithRequestID(context.Background(), "")
		id := RequestIDFromContext(ctx)
		if id == "" {
			t.Error("expected generated ID, got empty string")
		}
		if len(id) != 16 { // 8 bytes = 16 hex chars
			t.Errorf("expected 16 char ID, got %d chars: %s", len(id), id)
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		ctx1 := WithRequestID(context.Background(), "")
		ctx2 := WithRequestID(context.Background(), "")
		id1 := RequestIDFromContext(ctx1)
		id2 := RequestIDFromContext(ctx2)
		if id1 == id2 {
			t.Errorf("expected unique IDs, got same ID: %s", id1)
		}
	})
}

func TestRequestIDFromContext(t *testing.T) {
	t.Run("nil context returns empty", func(t *testing.T) {
		id := RequestIDFromContext(nil)
		if id != "" {
			t.Errorf("expected empty string for nil context, got: %s", id)
		}
	})

	t.Run("context without ID returns empty", func(t *testing.T) {
		id := RequestIDFromContext(context.Background())
		if id != "" {
			t.Errorf("expected empty string for context without ID, got: %s", id)
		}
	})

	t.Run("context with ID returns ID", func(t *testing.T) {
		ctx := WithRequestID(context.Background(), "my-request-id")
		id := RequestIDFromContext(ctx)
		if id != "my-request-id" {
			t.Errorf("expected 'my-request-id', got: %s", id)
		}
	})
}

func TestContextWithRequestID(t *testing.T) {
	t.Run("with provided ID", func(t *testing.T) {
		ctx, id := ContextWithRequestID(context.Background(), "provided-id")
		if id != "provided-id" {
			t.Errorf("expected returned ID 'provided-id', got: %s", id)
		}
		storedID := RequestIDFromContext(ctx)
		if storedID != "provided-id" {
			t.Errorf("expected stored ID 'provided-id', got: %s", storedID)
		}
	})

	t.Run("with empty ID generates random", func(t *testing.T) {
		ctx, id := ContextWithRequestID(context.Background(), "")
		if id == "" {
			t.Error("expected generated ID, got empty string")
		}
		if len(id) != 16 { // 8 bytes = 16 hex chars
			t.Errorf("expected 16 char ID, got %d chars: %s", len(id), id)
		}
		storedID := RequestIDFromContext(ctx)
		if storedID != id {
			t.Errorf("expected stored ID '%s', got: %s", id, storedID)
		}
	})
}
