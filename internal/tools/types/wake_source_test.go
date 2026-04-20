package types

import (
	"context"
	"testing"
)

func TestWakeSource(t *testing.T) {
	t.Run("unset returns empty", func(t *testing.T) {
		if got := WakeSource(context.Background()); got != "" {
			t.Fatalf("WakeSource on bare context = %q, want %q", got, "")
		}
	})

	t.Run("round trip", func(t *testing.T) {
		ctx := WithWakeSource(context.Background(), WakeSourceSubAgentCallback)
		if got := WakeSource(ctx); got != WakeSourceSubAgentCallback {
			t.Fatalf("WakeSource = %q, want %q", got, WakeSourceSubAgentCallback)
		}
	})

	t.Run("empty source is a no-op", func(t *testing.T) {
		// WithWakeSource("") must not overwrite an existing tag with a zero value.
		ctx := WithWakeSource(context.Background(), WakeSourceInterSession)
		ctx = WithWakeSource(ctx, "")
		if got := WakeSource(ctx); got != WakeSourceInterSession {
			t.Fatalf("expected empty WithWakeSource to be a no-op, got %q", got)
		}
	})
}
