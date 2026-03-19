package gateway

import (
	"context"
	"testing"
	"time"
)

func TestSubAgent_GatewayContextUsed(t *testing.T) {
	// Verify that sub-agents use the gateway context, not request context.
	// Gateway shutdown should cancel sub-agents, but request cancellation should not.

	gatewayCtx, gatewayCancel := context.WithCancel(context.Background())
	defer gatewayCancel()

	subCtx, subCancel := deriveSubAgentContext(gatewayCtx, 30)
	defer subCancel()

	// Sub-agent context should not be done yet
	select {
	case <-subCtx.Done():
		t.Fatal("Sub-agent context should not be done before gateway shutdown")
	default:
		// expected
	}

	// Cancel the gateway context (simulating shutdown)
	gatewayCancel()

	// Sub-agent context should now be done
	select {
	case <-subCtx.Done():
		// expected - gateway shutdown propagated
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Sub-agent context should be cancelled when gateway shuts down")
	}
}

func TestSubAgent_RequestCancellationDoesNotAffectSubAgent(t *testing.T) {
	// Verify that cancelling the request context does NOT cancel sub-agents.
	// Sub-agents are fire-and-forget and should outlive the parent request.

	gatewayCtx := context.Background() // long-lived gateway context

	// Sub-agent derives from gateway context, not request context
	subCtx, subCancel := deriveSubAgentContext(gatewayCtx, 30)
	defer subCancel()

	// Simulate a request context that gets cancelled
	_, requestCancel := context.WithCancel(context.Background())
	requestCancel() // Request completes/cancels

	// Sub-agent context should still be active (not affected by request cancellation)
	select {
	case <-subCtx.Done():
		t.Fatal("Sub-agent context should NOT be cancelled by request cancellation")
	default:
		// expected - sub-agent continues running
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
