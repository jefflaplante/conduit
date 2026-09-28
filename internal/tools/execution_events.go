package tools

import (
	"context"
	"fmt"
	"time"
)

// toolEventCallbackKey is the context key for tool event callbacks
type toolEventCallbackKey struct{}

// ToolEventInfo contains information about a tool execution event
type ToolEventInfo struct {
	ToolName  string
	EventType string // "start", "complete", "error", "thinking"
	Args      map[string]interface{}
	Result    string
	Error     string
	Duration  time.Duration
}

// ToolEventCallback is called during tool execution to notify listeners
type ToolEventCallback func(event ToolEventInfo)

// WithToolEventCallback returns a context with a tool event callback attached
func WithToolEventCallback(ctx context.Context, cb ToolEventCallback) context.Context {
	return context.WithValue(ctx, toolEventCallbackKey{}, cb)
}

// getToolEventCallback extracts the tool event callback from context, if any
func getToolEventCallback(ctx context.Context) ToolEventCallback {
	cb, _ := ctx.Value(toolEventCallbackKey{}).(ToolEventCallback)
	return cb
}

// startThinkingIndicator emits periodic "thinking" events via the tool event callback.
// Returns a stop function that must be called when the LLM responds.
func startThinkingIndicator(ctx context.Context, depth int) func() {
	cb := getToolEventCallback(ctx)
	if cb == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		msg := thinkingMessage(depth)
		// Thinking indicators go to ring buffer only (pure noise in journal)
		cb(ToolEventInfo{
			ToolName:  msg,
			EventType: "thinking",
		})
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				msg := thinkingMessage(depth)
				cb(ToolEventInfo{
					ToolName:  msg,
					EventType: "thinking",
				})
			}
		}
	}()
	return func() { close(done) }
}

func thinkingMessage(depth int) string {
	if depth == 0 {
		return "Thinking..."
	}
	return fmt.Sprintf("Thinking (step %d)...", depth+1)
}
