package workspace

import (
	"context"
	"fmt"
	"strings"
)

// This file provides integration examples for when the agent system from ticket #007 is ready

// SystemPromptBuilder demonstrates how to integrate workspace context with system prompt generation
type SystemPromptBuilder struct {
	workspace *Manager
}

// NewSystemPromptBuilder creates a new system prompt builder
func NewSystemPromptBuilder(workspace *Manager) *SystemPromptBuilder {
	return &SystemPromptBuilder{
		workspace: workspace,
	}
}

// SystemBlock represents a section of the system prompt
type SystemBlock struct {
	Type string      `json:"type"`
	Text string      `json:"text,omitempty"`
	Meta interface{} `json:"meta,omitempty"`
}

// BuildSystemPromptWithContext builds a complete system prompt including workspace context
func (spb *SystemPromptBuilder) BuildSystemPromptWithContext(ctx context.Context, sessionType, channelID, userID, sessionID string) ([]SystemBlock, error) {
	// Load workspace context
	bundle, err := spb.workspace.LoadContextForSession(ctx, sessionType, channelID, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace context: %w", err)
	}

	blocks := []SystemBlock{}

	// Add identity block (this would be determined by OAuth vs API key auth)
	identityText := "You are Claude Code, Anthropic's official CLI for Claude.You are a personal assistant running inside Conduit."
	blocks = append(blocks, SystemBlock{
		Type: "identity",
		Text: identityText,
	})

	// Add personality from SOUL.md
	if soulContent, exists := bundle.Files["SOUL.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "personality",
			Text: fmt.Sprintf("## Your Personality (SOUL.md)\n%s", soulContent),
		})
	}

	// Add user context from USER.md
	if userContent, exists := bundle.Files["USER.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "user_context",
			Text: fmt.Sprintf("## User Context (USER.md)\n%s", userContent),
		})
	}

	// Add operational instructions from AGENTS.md
	if agentsContent, exists := bundle.Files["AGENTS.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "instructions",
			Text: fmt.Sprintf("## Operational Instructions (AGENTS.md)\n%s", agentsContent),
		})
	}

	// Add tool configuration from TOOLS.md
	if toolsContent, exists := bundle.Files["TOOLS.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "tools_config",
			Text: fmt.Sprintf("## Tool Configuration (TOOLS.md)\n%s", toolsContent),
		})
	}

	// Add memory context (MEMORY.md only available in main sessions)
	if memoryContent, exists := bundle.Files["MEMORY.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "long_term_memory",
			Text: fmt.Sprintf("## Long-term Memory (MEMORY.md)\n%s", memoryContent),
		})
	}

	// Add recent daily memory for continuity
	memoryFiles := spb.extractMemoryFiles(bundle)
	if len(memoryFiles) > 0 {
		memoryText := spb.buildMemorySection(memoryFiles)
		blocks = append(blocks, SystemBlock{
			Type: "recent_memory",
			Text: memoryText,
		})
	}

	// Add heartbeat instructions (main sessions only)
	if heartbeatContent, exists := bundle.Files["HEARTBEAT.md"]; exists {
		blocks = append(blocks, SystemBlock{
			Type: "heartbeat",
			Text: fmt.Sprintf("## Heartbeat Instructions (HEARTBEAT.md)\n%s", heartbeatContent),
		})
	}

	// Add session metadata
	blocks = append(blocks, SystemBlock{
		Type: "session_meta",
		Text: spb.buildSessionMetaText(bundle),
		Meta: bundle.Metadata,
	})

	return blocks, nil
}

// extractMemoryFiles extracts daily memory files from the bundle
func (spb *SystemPromptBuilder) extractMemoryFiles(bundle *ContextBundle) map[string]string {
	memoryFiles := make(map[string]string)

	for path, content := range bundle.Files {
		if strings.HasPrefix(path, "memory/") && strings.HasSuffix(path, ".md") {
			memoryFiles[path] = content
		}
	}

	return memoryFiles
}

// buildMemorySection constructs the recent memory section
func (spb *SystemPromptBuilder) buildMemorySection(memoryFiles map[string]string) string {
	if len(memoryFiles) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("## Recent Memory (Daily Logs)\n")

	for path, content := range memoryFiles {
		// Extract date from filename
		filename := strings.TrimSuffix(strings.TrimPrefix(path, "memory/"), ".md")
		builder.WriteString(fmt.Sprintf("### %s\n%s\n\n", filename, content))
	}

	return builder.String()
}

// buildSessionMetaText creates session metadata text
func (spb *SystemPromptBuilder) buildSessionMetaText(bundle *ContextBundle) string {
	var builder strings.Builder
	builder.WriteString("## Session Context\n")

	if sessionType, ok := bundle.Metadata["session_type"].(string); ok {
		builder.WriteString(fmt.Sprintf("- Session Type: %s\n", sessionType))
	}

	if fileCount, ok := bundle.Metadata["file_count"].(int); ok {
		builder.WriteString(fmt.Sprintf("- Context Files Loaded: %d\n", fileCount))
	}

	builder.WriteString(fmt.Sprintf("- Timestamp: %s\n", bundle.Timestamp.Format("2006-01-02 15:04:05 UTC")))

	return builder.String()
}

// Example usage for future integration:

/*
// In internal/agent/conduit.go (when ticket #007 is complete):

type ConduitAgent struct {
    workspace *workspace.Manager
    promptBuilder *workspace.SystemPromptBuilder
}

func NewConduitAgent(config AgentConfig, workspaceManager *workspace.Manager) *ConduitAgent {
    return &ConduitAgent{
        workspace: workspaceManager,
        promptBuilder: workspace.NewSystemPromptBuilder(workspaceManager),
    }
}

func (a *ConduitAgent) BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]SystemBlock, error) {
    return a.promptBuilder.BuildSystemPromptWithContext(
        ctx,
        session.Type,
        session.ChannelID,
        session.UserID,
        session.ID,
    )
}
*/
