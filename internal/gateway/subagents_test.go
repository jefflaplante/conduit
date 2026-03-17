package gateway

import (
	"context"
	"testing"
	"time"
)

func TestSubAgent_ContextPropagation(t *testing.T) {
	// Verify that deriveSubAgentContext derives from the parent context,
	// so that cancelling the parent also cancels the sub-agent context.

	parentCtx, parentCancel := context.WithCancel(context.Background())

	subCtx, subCancel := deriveSubAgentContext(parentCtx, 30)
	defer subCancel()

	// Sub-agent context should not be done yet
	select {
	case <-subCtx.Done():
		t.Fatal("Sub-agent context should not be done before parent cancellation")
	default:
		// expected
	}

	// Cancel the parent context
	parentCancel()

	// Sub-agent context should now be done (with small wait for propagation)
	select {
	case <-subCtx.Done():
		// expected - parent cancellation propagated
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Sub-agent context should be cancelled when parent is cancelled")
	}
}

func TestSubAgent_ContextTimeout(t *testing.T) {
	// Verify that the sub-agent context respects its own timeout
	parentCtx := context.Background()

	subCtx, subCancel := deriveSubAgentContext(parentCtx, 1) // 1 second timeout
	defer subCancel()

	select {
	case <-subCtx.Done():
		// expected - timed out
	case <-time.After(3 * time.Second):
		t.Fatal("Sub-agent context should have timed out after 1 second")
	}
}
