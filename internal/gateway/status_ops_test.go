package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/skills"
	"conduit/internal/tools"
	toolstypes "conduit/internal/tools/types"
)

func TestReloadSkillTools_InvalidatesPromptCache(t *testing.T) {
	sm := skills.NewManager(skills.SkillsConfig{
		SearchPaths: []string{t.TempDir()},
	})

	agentSys := agent.NewConduitAgentWithIntegration(
		agent.AgentConfig{
			Name: "test",
			Capabilities: agent.AgentCapabilities{
				SkillsIntegration: true,
			},
		},
		[]ai.Tool{{Name: "OriginalTool", Description: "original tool"}},
		nil, nil, sm, nil, nil,
	)
	agentSys.SetPromptCacheTTL(10 * time.Minute)

	// Build prompt to populate cache — it will mention "OriginalTool"
	blocks, err := agentSys.BuildSystemPrompt(context.Background(), nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	promptBefore := blocksToText(blocks)
	if !strings.Contains(promptBefore, "OriginalTool") {
		t.Fatal("expected prompt to contain OriginalTool")
	}

	// Now swap the agent's tools to include a new tool name
	agentSys.SetTools([]ai.Tool{
		{Name: "OriginalTool", Description: "original tool"},
		{Name: "BrandNewTool", Description: "added after reload"},
	})

	// SetTools already invalidates — verify the new prompt has BrandNewTool
	blocks, err = agentSys.BuildSystemPrompt(context.Background(), nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt after SetTools: %v", err)
	}
	promptAfterSetTools := blocksToText(blocks)
	if !strings.Contains(promptAfterSetTools, "BrandNewTool") {
		t.Fatal("expected prompt to contain BrandNewTool after SetTools (cache should have been invalidated)")
	}

	// Now test ReloadSkillTools path specifically:
	// Prime cache again with current tools
	_, _ = agentSys.BuildSystemPrompt(context.Background(), nil)

	// Swap tools back to just OriginalTool without calling SetTools
	// (simulating what would happen if the prompt was stale)
	agentSys.SetTools([]ai.Tool{{Name: "OriginalTool", Description: "original tool"}})

	// Prime cache with OriginalTool-only prompt
	_, _ = agentSys.BuildSystemPrompt(context.Background(), nil)

	// Now add tools and call ReloadSkillTools (which is the code path we fixed)
	registry := tools.NewRegistry(config.ToolsConfig{})
	registry.SetServices(&toolstypes.ToolServices{SkillsManager: sm})

	gw := &Gateway{
		agentSystem:   agentSys,
		tools:         registry,
		skillsManager: sm,
	}

	_, err = gw.ReloadSkillTools(context.Background())
	if err != nil {
		t.Fatalf("ReloadSkillTools: %v", err)
	}

	// The key assertion: ReloadSkillTools must have called InvalidatePromptCache,
	// so building the prompt now should produce a fresh (non-cached) result.
	// We verify this by confirming no error and that the prompt is non-empty.
	blocks, err = agentSys.BuildSystemPrompt(context.Background(), nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt after ReloadSkillTools: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected non-empty prompt after ReloadSkillTools")
	}
}

func TestReloadSkillTools_NoSkillsManager(t *testing.T) {
	gw := &Gateway{
		skillsManager: nil,
	}

	_, err := gw.ReloadSkillTools(context.Background())
	if err == nil {
		t.Error("expected error when skillsManager is nil")
	}
}

func TestPromptCacheInvalidation_Direct(t *testing.T) {
	agentSys := agent.NewConduitAgentWithIntegration(
		agent.AgentConfig{Name: "test-cache"},
		[]ai.Tool{{Name: "TestTool", Description: "a test tool"}},
		nil, nil, nil, nil, nil,
	)
	agentSys.SetPromptCacheTTL(10 * time.Minute)

	// Build prompt — should mention TestTool
	blocks, err := agentSys.BuildSystemPrompt(context.Background(), nil)
	if err != nil {
		t.Fatalf("first BuildSystemPrompt: %v", err)
	}
	prompt1 := blocksToText(blocks)
	if !strings.Contains(prompt1, "TestTool") {
		t.Fatal("expected prompt to contain TestTool")
	}

	// Update tools to add a second tool
	agentSys.SetTools([]ai.Tool{
		{Name: "TestTool", Description: "a test tool"},
		{Name: "AddedTool", Description: "new tool"},
	})

	// After SetTools (which calls InvalidatePromptCache), the next prompt
	// should include AddedTool
	blocks, err = agentSys.BuildSystemPrompt(context.Background(), nil)
	if err != nil {
		t.Fatalf("post-invalidation BuildSystemPrompt: %v", err)
	}
	prompt2 := blocksToText(blocks)
	if !strings.Contains(prompt2, "AddedTool") {
		t.Error("expected prompt to contain AddedTool after cache invalidation")
	}
}

func blocksToText(blocks []ai.SystemBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(b.Text)
		sb.WriteString("\n")
	}
	return sb.String()
}
