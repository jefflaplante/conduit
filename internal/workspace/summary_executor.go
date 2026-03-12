package workspace

import (
	"context"
	"fmt"
	"strings"
)

// SummaryAIExecutor is the interface for AI-based summarization
// This follows the pattern from internal/heartbeat/integration.go:369-397
type SummaryAIExecutor interface {
	// Summarize generates a compressed version of content
	Summarize(ctx context.Context, content, fileType string, targetRatio float64, preserveKeys []string) (string, error)
}

// SummaryAIResponse is the interface for AI responses
type SummaryAIResponse interface {
	GetContent() string
}

// SummaryAIRouter is the interface for the AI router
// Matches the signature used in heartbeat integration
type SummaryAIRouter interface {
	GenerateSimpleResponse(ctx context.Context, prompt, model string) (SummaryAIResponse, error)
}

// workspaceSummaryExecutor adapts the AI router to SummaryAIExecutor
type workspaceSummaryExecutor struct {
	aiRouter SummaryAIRouter
	model    string
}

// NewSummaryExecutor creates a new summary executor
func NewSummaryExecutor(aiRouter SummaryAIRouter, model string) SummaryAIExecutor {
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &workspaceSummaryExecutor{
		aiRouter: aiRouter,
		model:    model,
	}
}

// Summarize generates a compressed version of content using AI
func (e *workspaceSummaryExecutor) Summarize(ctx context.Context, content, fileType string, targetRatio float64, preserveKeys []string) (string, error) {
	prompt := buildSummarizationPrompt(content, fileType, targetRatio, preserveKeys)

	response, err := e.aiRouter.GenerateSimpleResponse(ctx, prompt, e.model)
	if err != nil {
		return "", fmt.Errorf("AI summarization failed: %w", err)
	}

	return response.GetContent(), nil
}

// buildSummarizationPrompt creates the prompt for AI summarization
func buildSummarizationPrompt(content, fileType string, targetRatio float64, preserveKeys []string) string {
	targetPercent := int(targetRatio * 100)

	var preserveSection string
	if len(preserveKeys) > 0 {
		preserveSection = fmt.Sprintf("PRESERVE: %s\n", strings.Join(preserveKeys, ", "))
	}

	// Choose instructions based on file type
	var typeInstructions string
	switch fileType {
	case "SOUL":
		typeInstructions = `This is an AI personality configuration file.
PRESERVE: Core personality traits, communication style, tone markers, voice characteristics, key mannerisms.
CONDENSE: Verbose explanations, extensive examples (keep the most illustrative), repeated concepts.
OUTPUT: A compressed personality definition that maintains the agent's distinct character.`

	case "USER":
		typeInstructions = `This is a user preferences and requirements file.
PRESERVE: User preferences, hard constraints, requirements, communication preferences, important context.
CONDENSE: Background explanations, optional suggestions, verbose context.
OUTPUT: A compressed user profile that captures essential preferences and requirements.`

	case "AGENTS":
		typeInstructions = `This is an operational rules and restrictions file.
PRESERVE: Mandatory rules, restrictions, do/don't lists, safety guidelines, required behaviors.
CONDENSE: Extended explanations, optional suggestions, contextual background.
OUTPUT: A compressed ruleset that maintains all mandatory constraints.`

	case "TOOLS":
		typeInstructions = `This is a tools usage guide.
PRESERVE: Command syntax, required parameters, important warnings, key examples.
CONDENSE: Detailed explanations, alternative approaches, extensive examples.
OUTPUT: A compressed tools reference with essential usage patterns.`

	case "IDENTITY":
		typeInstructions = `This is an agent identity file.
PRESERVE: Core identity statements, name, role, key distinguishing characteristics.
CONDENSE: Background context, elaboration.
OUTPUT: A compressed identity definition.`

	case "HEARTBEAT":
		typeInstructions = `This is a scheduled tasks/heartbeat file.
PRESERVE: Active tasks, schedules, priorities, required actions.
CONDENSE: Completed tasks, historical context, optional items.
OUTPUT: A compressed task list focused on current/upcoming work.`

	default:
		typeInstructions = `This is a workspace context file.
PRESERVE: Key information, requirements, constraints, important context.
CONDENSE: Verbose explanations, examples, background.
OUTPUT: A compressed version maintaining essential content.`
	}

	return fmt.Sprintf(`Compress the following %s file to approximately %d%% of its original size.

%s
%s
RULES:
- Output ONLY the compressed content, no preamble or explanation
- Maintain the same format/structure where helpful (headers, bullets)
- Do not lose any mandatory rules, restrictions, or critical information
- Preserve specific names, values, and identifiers exactly
- Use concise language while maintaining clarity

CONTENT:
%s`, fileType, targetPercent, typeInstructions, preserveSection, content)
}

// DetectFileType identifies the type of workspace file
func DetectFileType(filename string) string {
	// Strip path and extension
	base := strings.TrimSuffix(filename, ".md")
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "\\"); idx >= 0 {
		base = base[idx+1:]
	}

	switch strings.ToUpper(base) {
	case "SOUL":
		return "SOUL"
	case "USER":
		return "USER"
	case "AGENTS":
		return "AGENTS"
	case "TOOLS":
		return "TOOLS"
	case "IDENTITY":
		return "IDENTITY"
	case "HEARTBEAT":
		return "HEARTBEAT"
	case "MEMORY":
		return "MEMORY"
	default:
		return "CONTEXT"
	}
}

// Truncate provides fallback truncation when AI summarization fails
func Truncate(content string, targetLen int) string {
	if len(content) <= targetLen {
		return content
	}

	// Try to truncate at a paragraph boundary
	truncated := content[:targetLen]
	if idx := strings.LastIndex(truncated, "\n\n"); idx > targetLen/2 {
		truncated = truncated[:idx]
	} else if idx := strings.LastIndex(truncated, "\n"); idx > targetLen/2 {
		truncated = truncated[:idx]
	}

	return truncated + "\n\n[...content truncated for context window...]"
}
