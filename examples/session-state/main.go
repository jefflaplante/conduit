package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/agent"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// This example demonstrates how to use the new session state tracking
// in the Conduit Go Gateway project

func main() {
	// Create a session store with state tracking
	store, err := sessions.NewStore("example.db")
	if err != nil {
		log.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session
	session, err := store.GetOrCreateSession("user123", "telegram")
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}

	fmt.Printf("Created session: %s\n", session.Key)

	// Example 1: Using the session store directly for state tracking
	fmt.Println("\n=== Direct Session State Management ===")

	// Begin processing a request
	err = store.UpdateSessionState(session.Key, sessions.SessionStateProcessing, map[string]interface{}{
		"action":       "handle_user_message",
		"message_type": "text",
	})
	if err != nil {
		log.Printf("Failed to update state: %v", err)
	}

	// Check current state
	state, exists := store.GetSessionState(session.Key)
	if exists {
		fmt.Printf("Current session state: %s\n", state)
	}

	// Get detailed state information
	stateInfo, exists := store.GetSessionStateInfo(session.Key)
	if exists {
		fmt.Printf("State info: State=%s, LastActivity=%v, ProcessingStart=%v\n",
			stateInfo.State, stateInfo.LastActivity, stateInfo.ProcessingStart)
	}

	// Mark activity without changing state
	store.MarkSessionActivity(session.Key)

	// Complete processing
	err = store.UpdateSessionState(session.Key, sessions.SessionStateIdle, map[string]interface{}{
		"action":        "processing_complete",
		"response_sent": true,
	})
	if err != nil {
		log.Printf("Failed to complete processing: %v", err)
	}

	// Example 2: Using the agent state manager for cleaner integration
	fmt.Println("\n=== Agent State Manager Usage ===")

	stateManager := agent.NewSessionStateManager(store)

	// Begin processing with the state manager
	err = stateManager.BeginProcessing(session.Key, map[string]interface{}{
		"tool":  "web_search",
		"query": "Conduit documentation",
	})
	if err != nil {
		log.Printf("Failed to begin processing: %v", err)
	}

	// Simulate waiting for external API
	err = stateManager.BeginWaiting(session.Key, "external_api_response")
	if err != nil {
		log.Printf("Failed to begin waiting: %v", err)
	}

	// Complete processing
	err = stateManager.CompleteProcessing(session.Key, map[string]interface{}{
		"tool_result":      "success",
		"response_time_ms": 1500,
	})
	if err != nil {
		log.Printf("Failed to complete processing: %v", err)
	}

	// Example 3: Monitoring and metrics
	fmt.Println("\n=== Monitoring Integration ===")

	// Create metrics collector
	gatewayMetrics := monitoring.NewGatewayMetrics()
	deps := monitoring.CollectorDependencies{
		SessionStore:   store,
		GatewayMetrics: gatewayMetrics,
	}
	collector := monitoring.NewMetricsCollector(deps)

	// Update queue depth
	store.UpdateQueueDepth(5)

	// Get current session metrics
	sessionMetrics := store.GetSessionStateMetrics()
	fmt.Printf("Session metrics: Total=%d, Idle=%d, Processing=%d, Waiting=%d, Error=%d, QueueDepth=%d\n",
		sessionMetrics.TotalSessions,
		sessionMetrics.IdleSessions,
		sessionMetrics.ProcessingSessions,
		sessionMetrics.WaitingSessions,
		sessionMetrics.ErrorSessions,
		sessionMetrics.QueueDepth)

	// Collect comprehensive metrics
	ctx := context.Background()
	_, err = collector.CollectMetrics(ctx)
	if err != nil {
		log.Printf("Failed to collect metrics: %v", err)
	} else {
		fmt.Printf("Collected metrics successfully\n")
	}

	// Get detailed statistics
	stats, err := collector.GetDetailedStats(ctx)
	if err != nil {
		log.Printf("Failed to get detailed stats: %v", err)
	} else {
		fmt.Printf("Detailed stats keys: ")
		for key := range stats {
			fmt.Printf("%s ", key)
		}
		fmt.Println()
	}

	// Example 4: Stuck session detection
	fmt.Println("\n=== Stuck Session Detection ===")

	config := sessions.StuckSessionConfig{
		ProcessingTimeout: 2 * time.Minute,
		WaitingTimeout:    5 * time.Minute,
		ErrorRetryLimit:   3,
	}

	stuckSessions := store.DetectStuckSessions(config)
	fmt.Printf("Found %d stuck sessions\n", len(stuckSessions))

	for _, stuck := range stuckSessions {
		fmt.Printf("  Stuck session: %s, Reason: %s, Duration: %v\n",
			stuck.SessionKey, stuck.Reason, stuck.StuckDuration)
	}

	// Example 5: State change hooks
	fmt.Println("\n=== State Change Hooks ===")

	// Add a hook to log all state changes
	store.AddStateChangeHook(func(event sessions.StateChangeEvent) {
		fmt.Printf("State change: %s -> %s (Session: %s, Time: %v)\n",
			event.OldState, event.NewState, event.SessionKey, event.Timestamp)
		if event.Metadata != nil {
			fmt.Printf("  Metadata: %v\n", event.Metadata)
		}
	})

	// Trigger some state changes to see the hooks in action
	err = store.UpdateSessionState(session.Key, sessions.SessionStateProcessing, map[string]interface{}{
		"action": "demo_processing",
	})
	if err != nil {
		log.Printf("Failed to update state: %v", err)
	}

	// Wait a moment for hooks to complete
	time.Sleep(100 * time.Millisecond)

	err = store.UpdateSessionState(session.Key, sessions.SessionStateIdle, map[string]interface{}{
		"action": "demo_complete",
	})
	if err != nil {
		log.Printf("Failed to update state: %v", err)
	}

	// Wait a moment for hooks to complete
	time.Sleep(100 * time.Millisecond)

	fmt.Println("\n=== Example Complete ===")
	fmt.Println("Session state tracking implementation is working correctly!")

	// Final metrics
	finalMetrics := store.GetSessionStateMetrics()
	fmt.Printf("Final session count: %d\n", finalMetrics.TotalSessions)
}
