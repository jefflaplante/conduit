package tui

import (
	"context"
	"testing"

	"conduit/internal/protocol"
)

func TestTUIAdapter_StopDoesNotPanic(t *testing.T) {
	adapter := NewAdapter("test-adapter", nil)

	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// First stop should succeed
	if err := adapter.Stop(); err != nil {
		t.Fatalf("First Stop failed: %v", err)
	}

	// Second stop should not panic (idempotent)
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Second Stop failed: %v", err)
	}
}

func TestTUIAdapter_SendAfterStop(t *testing.T) {
	adapter := NewAdapter("test-adapter", nil)

	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Sending to a stopped adapter should not panic
	err := adapter.SendMessage(&protocol.OutgoingMessage{
		Text: "test message",
	})
	if err == nil {
		t.Error("expected error when sending to stopped adapter")
	}

	// SendIncomingMessage should also not panic
	err = adapter.SendIncomingMessage(&protocol.IncomingMessage{
		Text: "test incoming",
	})
	if err == nil {
		t.Error("expected error when sending incoming to stopped adapter")
	}
}
