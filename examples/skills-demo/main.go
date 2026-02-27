package main

import (
	"context"
	"fmt"
	"log"

	"conduit/internal/skills"
)

func main() {
	log.Println("Conduit Skills System Demo")

	// Create skills configuration
	config := skills.SkillsConfig{
		Enabled: true,
		SearchPaths: []string{
			"./skills",
			"/usr/local/lib/conduit/skills",
		},
		Execution: skills.ExecutionConfig{
			TimeoutSeconds: 30,
			Environment: map[string]string{
				"SKILL_MODE": "demo",
			},
		},
		Cache: skills.CacheConfig{
			Enabled:    true,
			TTLSeconds: 600, // 10 minutes
		},
	}

	// Create skills manager
	manager := skills.NewManager(config)

	// Initialize the skills system
	ctx := context.Background()
	if err := manager.Initialize(ctx); err != nil {
		log.Fatalf("Failed to initialize skills system: %v", err)
	}

	// Get available skills
	availableSkills, err := manager.GetAvailableSkills(ctx)
	if err != nil {
		log.Fatalf("Failed to get available skills: %v", err)
	}

	fmt.Printf("\n=== Discovered %d Skills ===\n", len(availableSkills))
	for _, skill := range availableSkills {
		emoji := skill.Metadata.Conduit.Emoji
		if emoji == "" {
			emoji = "🔧"
		}
		fmt.Printf("%s %s: %s\n", emoji, skill.Name, skill.Description)
		fmt.Printf("   Location: %s\n", skill.Location)

		if len(skill.Scripts) > 0 {
			fmt.Printf("   Scripts: ")
			for i, script := range skill.Scripts {
				if i > 0 {
					fmt.Printf(", ")
				}
				fmt.Printf("%s (%s)", script.Name, script.Language)
			}
			fmt.Printf("\n")
		}

		if len(skill.References) > 0 {
			fmt.Printf("   References: %d files\n", len(skill.References))
		}

		fmt.Printf("\n")
	}

	// Get skill status with validation
	statuses, err := manager.GetSkillStatus(ctx)
	if err != nil {
		log.Fatalf("Failed to get skill status: %v", err)
	}

	fmt.Printf("=== Skill Validation Status ===\n")
	availableCount := 0
	for _, status := range statuses {
		if status.Available {
			availableCount++
			fmt.Printf("✅ %s: Available (%d actions)\n", status.Name, len(status.Actions))
		} else {
			fmt.Printf("❌ %s: %s\n", status.Name, status.Error)
			if len(status.MissingRequirements) > 0 {
				for reqType, missing := range status.MissingRequirements {
					fmt.Printf("   Missing %s: %v\n", reqType, missing)
				}
			}
		}
	}

	fmt.Printf("\nSummary: %d/%d skills available\n", availableCount, len(statuses))

	// Generate tools from skills
	skillTools, err := manager.GenerateTools(ctx)
	if err != nil {
		log.Fatalf("Failed to generate tools: %v", err)
	}

	fmt.Printf("\n=== Generated %d Tools ===\n", len(skillTools))
	for _, tool := range skillTools {
		fmt.Printf("📦 %s: %s\n", tool.Name(), tool.Description())
	}

	// Build skills context for agent prompts
	skillsContext, err := manager.BuildSystemPromptContext(ctx)
	if err != nil {
		log.Fatalf("Failed to build skills context: %v", err)
	}

	if skillsContext != "" {
		fmt.Printf("\n=== Skills Context for Agent ===\n")
		fmt.Printf("%s", skillsContext)
	}

	// Demo: Execute a skill (if available)
	if len(availableSkills) > 0 && availableCount > 0 {
		// Find first available skill
		var testSkill *skills.Skill
		for _, status := range statuses {
			if status.Available && len(status.Actions) > 0 {
				for _, skill := range availableSkills {
					if skill.Name == status.Name {
						testSkill = &skill
						break
					}
				}
				break
			}
		}

		if testSkill != nil {
			fmt.Printf("\n=== Demo: Executing %s ===\n", testSkill.Name)

			// Get the first available action
			status, _ := findSkillStatus(statuses, testSkill.Name)
			if len(status.Actions) > 0 {
				action := status.Actions[0]
				args := map[string]interface{}{
					"demo": true,
				}

				result, err := manager.ExecuteSkill(ctx, testSkill.Name, action, args)
				if err != nil {
					fmt.Printf("Execution error: %v\n", err)
				} else {
					fmt.Printf("Action: %s\n", action)
					fmt.Printf("Success: %v\n", result.Success)
					if result.Output != "" {
						fmt.Printf("Output: %s\n", result.Output)
					}
					if result.Error != "" {
						fmt.Printf("Error: %s\n", result.Error)
					}
				}
			}
		}
	}

	fmt.Printf("\nSkills system demo completed successfully!\n")
}

func findSkillStatus(statuses []skills.SkillStatus, name string) (*skills.SkillStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return &status, true
		}
	}
	return nil, false
}
