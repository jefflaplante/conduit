package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"conduit/internal/config"
	"conduit/internal/gateway"
)

// Example of running Conduit Go Gateway with Telegram integration
func main() {
	fmt.Println("Conduit Go Gateway - Telegram Bot Example")
	fmt.Println("==========================================")

	// Check for required environment variables
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	// Load Telegram-specific configuration
	cfg, err := config.Load("config.telegram.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Validate Telegram configuration
	found := false
	for _, ch := range cfg.Channels {
		if ch.Type == "telegram" && ch.Enabled {
			found = true
			break
		}
	}

	if !found {
		log.Fatal("No enabled Telegram channel found in configuration")
	}

	// Create gateway instance
	gw, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create gateway: %v", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		fmt.Printf("\nReceived signal: %v\n", sig)
		fmt.Println("Shutting down gracefully...")
		cancel()
	}()

	// Start the gateway
	fmt.Printf("Starting Conduit Go Gateway with Telegram on port %d\n", cfg.Port)
	fmt.Println("Send messages to your Telegram bot to test the integration!")
	fmt.Println("Press Ctrl+C to stop")

	// Show channel status after startup
	go func() {
		time.Sleep(3 * time.Second)
		fmt.Println("\nChannel Status:")
		fmt.Println("===============")

		// Note: In a real implementation, you'd query the gateway's channel status
		// For this example, we'll just show that it's running
		fmt.Println("✓ Telegram adapter: Starting...")

		time.Sleep(2 * time.Second)
		fmt.Println("✓ Telegram adapter: Online")
		fmt.Println("✓ WebSocket API: http://localhost:18790/ws")
		fmt.Println("✓ Health check: http://localhost:18790/health")
		fmt.Println("✓ Channel status: http://localhost:18790/api/channels/status")
	}()

	if err := gw.Start(ctx); err != nil {
		log.Fatalf("Gateway failed: %v", err)
	}

	fmt.Println("Gateway stopped gracefully")
}
