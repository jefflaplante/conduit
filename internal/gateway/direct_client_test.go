package gateway

import (
	"context"
	"testing"

	"conduit/internal/tui"
)

func TestDirectClient_DroppedMessageCounter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := NewDirectClient(DirectClientConfig{
		ParentCtx: ctx,
		UserID:    "test-user",
		AgentName: "test-agent",
	})
	defer client.Close()

	// Verify initial drop count is 0
	if dropped := client.DroppedMessages(); dropped != 0 {
		t.Errorf("Expected 0 initial dropped messages, got %d", dropped)
	}

	// Fill the inbox (capacity is 256)
	for i := 0; i < 256; i++ {
		client.send(tui.StreamDeltaMsg{Delta: "x"})
	}

	// Inbox should be full now. Next sends should be dropped.
	client.send(tui.StreamDeltaMsg{Delta: "dropped1"})
	client.send(tui.StreamDeltaMsg{Delta: "dropped2"})
	client.send(tui.StreamDeltaMsg{Delta: "dropped3"})

	dropped := client.DroppedMessages()
	if dropped != 3 {
		t.Errorf("Expected 3 dropped messages, got %d", dropped)
	}

	// Drain one message from inbox and send again — should not increment drop counter
	<-client.inbox
	client.send(tui.StreamDeltaMsg{Delta: "not_dropped"})

	droppedAfter := client.DroppedMessages()
	if droppedAfter != 3 {
		t.Errorf("Expected still 3 dropped messages after successful send, got %d", droppedAfter)
	}
}
