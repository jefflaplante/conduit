package main

import (
	"context"
	"flag"
	"log"
	"strings"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

func main() {
	var (
		configPath   = flag.String("config", "config.live-test.json", "Path to configuration file")
		workspaceDir = flag.String("workspace", "./workspace", "Workspace directory")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	log.Println("Testing Conduit Integration...")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Test 1: Workspace Context
	log.Println("\n=== Testing Workspace Context ===")

	workspaceContext := workspace.NewWorkspaceContext(*workspaceDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	securityCtx := workspace.SecurityContext{
		SessionType: "main",
		ChannelID:   "test",
		UserID:      "test",
		SessionID:   "test",
	}

	bundle, err := workspaceContext.LoadContext(ctx, securityCtx)
	if err != nil {
		log.Printf("❌ Failed to load workspace context: %v", err)
	} else {
		log.Printf("✅ Workspace context loaded: %d files", len(bundle.Files))
		for filename, content := range bundle.Files {
			log.Printf("  - %s (%d bytes)", filename, len(content))
			if filename == "SOUL.md" {
				log.Printf("    Preview: %.100s...", content)
			}
		}
	}

	// Test 2: Skills Manager
	log.Println("\n=== Testing Skills Manager ===")

	if cfg.Skills.Enabled {
		skillsManager := skills.NewManager(cfg.Skills)

		if err := skillsManager.Initialize(ctx); err != nil {
			log.Printf("❌ Failed to initialize skills manager: %v", err)
		} else {
			availableSkills, err := skillsManager.GetAvailableSkills(ctx)
			if err != nil {
				log.Printf("❌ Failed to get available skills: %v", err)
			} else {
				log.Printf("✅ Skills manager initialized: %d skills", len(availableSkills))
				for _, skill := range availableSkills {
					log.Printf("  - %s: %s (%s)", skill.Name, skill.Description, skill.Location)
				}

				// Test skills context generation
				skillsContext, err := skillsManager.BuildSystemPromptContext(ctx)
				if err != nil {
					log.Printf("❌ Failed to build skills context: %v", err)
				} else {
					log.Printf("✅ Skills context generated (%d bytes)", len(skillsContext))
				}
			}
		}
	} else {
		log.Println("⚠️ Skills system is disabled in configuration")
	}

	// Test 3: Agent System Integration
	log.Println("\n=== Testing Agent System Integration ===")

	// Create a dummy session
	session := &sessions.Session{
		Key:       "test_session",
		UserID:    "test_user",
		ChannelID: "test_channel",
		CreatedAt: time.Now(),
		Context:   make(map[string]string),
	}

	// Initialize skills manager for agent
	var skillsManager *skills.Manager
	if cfg.Skills.Enabled {
		skillsManager = skills.NewManager(cfg.Skills)
		if err := skillsManager.Initialize(ctx); err != nil {
			log.Printf("Warning: Failed to initialize skills manager: %v", err)
			skillsManager = nil
		}
	}

	// Create agent config
	agentCfg := agent.AgentConfig{
		Name:        cfg.Agent.Name,
		Personality: cfg.Agent.Personality,
		Identity: agent.IdentityConfig{
			OAuthIdentity:  cfg.Agent.Identity.OAuthIdentity,
			APIKeyIdentity: cfg.Agent.Identity.APIKeyIdentity,
		},
		Capabilities: agent.AgentCapabilities{
			MemoryRecall:      cfg.Agent.Capabilities.MemoryRecall,
			ToolChaining:      cfg.Agent.Capabilities.ToolChaining,
			SkillsIntegration: cfg.Agent.Capabilities.SkillsIntegration,
			Heartbeats:        cfg.Agent.Capabilities.Heartbeats,
			SilentReplies:     cfg.Agent.Capabilities.SilentReplies,
		},
	}

	// Create integrated agent
	integratedAgent := agent.NewConduitAgentWithIntegration(
		agentCfg,
		[]ai.Tool{}, // No tools for this test
		workspaceContext,
		skillsManager,
		cfg.AI.ModelAliases,
	)

	// Initialize agent
	if err := integratedAgent.Initialize(ctx); err != nil {
		log.Printf("❌ Failed to initialize integrated agent: %v", err)
	} else {
		log.Printf("✅ Integrated agent initialized: %s", integratedAgent.Name())
	}

	// Test system prompt building
	systemBlocks, err := integratedAgent.BuildSystemPrompt(ctx, session)
	if err != nil {
		log.Printf("❌ Failed to build system prompt: %v", err)
	} else {
		log.Printf("✅ System prompt built: %d blocks", len(systemBlocks))
		totalChars := 0
		for i, block := range systemBlocks {
			totalChars += len(block.Text)
			log.Printf("  Block %d: %d characters", i+1, len(block.Text))

			// Show preview of first block (identity)
			if i == 0 {
				lines := strings.Split(block.Text, "\n")
				if len(lines) > 0 {
					log.Printf("    Preview: %s", lines[0])
				}
			}

			// Check for workspace context
			if strings.Contains(block.Text, "SOUL.md") || strings.Contains(block.Text, "Project Context") {
				log.Printf("    ✅ Contains workspace context")
			}

			// Check for skills context
			if strings.Contains(block.Text, "Skills Integration") || strings.Contains(block.Text, "skills") {
				log.Printf("    ✅ Contains skills context")
			}
		}
		log.Printf("  Total prompt size: %d characters", totalChars)
	}

	log.Println("\n=== Integration Test Complete ===")
}
