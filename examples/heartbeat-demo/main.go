// Example demonstrating the heartbeat system integration
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/config"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

func main() {
	// This example shows how the heartbeat system would work
	fmt.Println("Conduit Go Gateway Heartbeat System Demo")
	fmt.Println("==========================================")

	// Create a mock session store for demo
	sessionStore, err := sessions.NewStore(":memory:")
	if err != nil {
		log.Fatalf("Failed to create session store: %v", err)
	}
	defer sessionStore.Close()

	// Create some sample sessions
	createSampleSessions(sessionStore)

	// Initialize monitoring system
	gatewayMetrics := monitoring.NewGatewayMetrics()
	gatewayMetrics.SetVersion("0.1.0")
	gatewayMetrics.SetStatus("healthy")

	// Create event store
	eventStore := monitoring.NewMemoryEventStore(100)

	// Create metrics collector
	metricsCollector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   sessionStore,
		GatewayMetrics: gatewayMetrics,
	})

	// Simulate some activity
	metricsCollector.UpdateWebSocketConnections(3)
	metricsCollector.UpdateActiveRequests(2)
	metricsCollector.UpdateQueueDepth(5)
	metricsCollector.MarkActivity()

	// Configure heartbeat with short interval for demo
	heartbeatConfig := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 2, // 2 seconds for demo
		EnableMetrics:   true,
		EnableEvents:    true,
		LogLevel:        "debug",
		MaxQueueDepth:   1000,
	}

	// Create heartbeat service
	heartbeatService := monitoring.NewHeartbeatService(monitoring.HeartbeatDependencies{
		Config:     heartbeatConfig,
		Collector:  metricsCollector,
		EventStore: eventStore,
	})

	// Start heartbeat
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Println("\nStarting heartbeat service...")
	err = heartbeatService.Start(ctx)
	if err != nil {
		log.Fatalf("Failed to start heartbeat service: %v", err)
	}

	// Let it run for a few heartbeats
	fmt.Println("Heartbeat running... (5 heartbeats)")
	time.Sleep(11 * time.Second)

	// Show stats
	fmt.Println("\nHeartbeat Statistics:")
	fmt.Println("====================")
	stats := heartbeatService.GetStats()
	for key, value := range stats {
		fmt.Printf("  %s: %v\n", key, value)
	}

	// Show detailed system metrics
	fmt.Println("\nDetailed System Metrics:")
	fmt.Println("========================")
	detailedStats, err := metricsCollector.GetDetailedStats(ctx)
	if err != nil {
		log.Printf("Failed to get detailed stats: %v", err)
	} else {
		for key, value := range detailedStats {
			fmt.Printf("  %s: %v\n", key, value)
		}
	}

	// Show recent events
	fmt.Println("\nRecent Heartbeat Events:")
	fmt.Println("========================")
	events, err := heartbeatService.GetRecentEvents(5)
	if err != nil {
		log.Printf("Failed to get recent events: %v", err)
	} else {
		for i, event := range events {
			fmt.Printf("  %d. [%s] %s: %s\n", i+1, event.Type, event.Severity, event.Message)
		}
	}

	// Stop the heartbeat service
	fmt.Println("\nStopping heartbeat service...")
	err = heartbeatService.Stop()
	if err != nil {
		log.Printf("Error stopping heartbeat service: %v", err)
	}

	fmt.Println("Demo complete!")
}

func createSampleSessions(store *sessions.Store) {
	// Create some test sessions
	sessions := []struct {
		userID    string
		channelID string
		messages  int
	}{
		{"user1", "telegram", 5},
		{"user2", "telegram", 12},
		{"user3", "whatsapp", 3},
	}

	for _, s := range sessions {
		session, err := store.GetOrCreateSession(s.userID, s.channelID)
		if err != nil {
			log.Printf("Failed to create session: %v", err)
			continue
		}

		// Add some messages
		for i := 0; i < s.messages; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}

			_, err := store.AddMessage(session.Key, role, fmt.Sprintf("Test message %d", i+1), nil)
			if err != nil {
				log.Printf("Failed to add message: %v", err)
			}
		}
	}
}
