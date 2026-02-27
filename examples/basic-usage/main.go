package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// Example of basic Conduit Go Gateway usage
func main() {
	fmt.Println("Conduit Go Gateway - Basic Usage Example")
	fmt.Println("=========================================")

	// Load configuration
	cfg, err := config.Load("../config.example.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create session store
	store, err := sessions.NewStore(":memory:") // In-memory for example
	if err != nil {
		log.Fatalf("Failed to create session store: %v", err)
	}
	defer store.Close()

	// Create AI router (nil agent system for basic example)
	aiRouter, err := ai.NewRouter(cfg.AI, nil)
	if err != nil {
		log.Printf("Note: AI router creation failed (expected without API keys): %v", err)
	}

	// Example 1: Create a session
	fmt.Println("\n1. Creating a session...")
	session, err := store.GetOrCreateSession("user123", "telegram")
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	fmt.Printf("   Session created: %s\n", session.Key)

	// Example 2: Add messages to the session
	fmt.Println("\n2. Adding messages to session...")

	userMessage, err := store.AddMessage(session.Key, "user", "Hello, I need help with Go programming", nil)
	if err != nil {
		log.Fatalf("Failed to add user message: %v", err)
	}
	fmt.Printf("   User message added: %s\n", userMessage.ID)

	assistantMessage, err := store.AddMessage(session.Key, "assistant", "I'd be happy to help you with Go programming! What specific topic would you like to explore?", nil)
	if err != nil {
		log.Fatalf("Failed to add assistant message: %v", err)
	}
	fmt.Printf("   Assistant message added: %s\n", assistantMessage.ID)

	// Example 3: Retrieve session messages
	fmt.Println("\n3. Retrieving session messages...")
	messages, err := store.GetMessages(session.Key, 0) // 0 = all messages
	if err != nil {
		log.Fatalf("Failed to get messages: %v", err)
	}

	for _, msg := range messages {
		fmt.Printf("   [%s] %s: %s\n", msg.Timestamp.Format(time.RFC3339), msg.Role, msg.Content)
	}

	// Example 4: List active sessions
	fmt.Println("\n4. Listing active sessions...")
	activeSessions, err := store.ListActiveSessions(10)
	if err != nil {
		log.Fatalf("Failed to list sessions: %v", err)
	}

	for _, s := range activeSessions {
		fmt.Printf("   Session %s: %s@%s (%d messages)\n",
			s.Key, s.UserID, s.ChannelID, s.MessageCount)
	}

	// Example 5: AI Generation (if configured)
	if aiRouter != nil {
		fmt.Println("\n5. Generating AI response...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		response, err := aiRouter.GenerateResponse(ctx, session, "What is a goroutine?", "")
		if err != nil {
			log.Printf("   AI generation failed (expected without API keys): %v", err)
		} else {
			fmt.Printf("   AI Response: %s\n", response.Content)
		}
	}

	fmt.Println("\n✓ Basic usage example completed successfully!")
	fmt.Println("\nNext steps:")
	fmt.Println("- Set up API keys in config.json")
	fmt.Println("- Run the full gateway with 'make run'")
	fmt.Println("- Connect channel adapters (Telegram, WhatsApp, etc.)")
}
