package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

// PromptBuilder handles building system prompts with full Conduit integration
type PromptBuilder struct {
	agentName        string
	personality      string
	identity         IdentityConfig
	capabilities     AgentCapabilities
	tools            []ai.Tool
	workspaceContext *workspace.WorkspaceContext
	skillsManager    *skills.Manager
	sectionParams    *SectionParams
}

// NewPromptBuilder creates a new prompt builder with full integration.
// modelAliases maps short names (e.g. "haiku") to full model identifiers.
// If nil, a built-in default set is used.
func NewPromptBuilder(
	agentName, personality string,
	identity IdentityConfig,
	capabilities AgentCapabilities,
	tools []ai.Tool,
	workspaceContext *workspace.WorkspaceContext,
	skillsManager *skills.Manager,
	modelAliases map[string]string,
) *PromptBuilder {
	params := NewSectionParams(tools)

	// Set defaults that can be overridden
	params.WorkspaceDir = "./workspace"
	params.UserTimezone = "UTC"
	params.HeartbeatPrompt = "Read HEARTBEAT.md if it exists (workspace context). Follow it strictly. Do not infer or repeat old tasks from prior chats. If nothing needs attention, reply HEARTBEAT_OK."
	params.TTSEnabled = true
	params.TTSVoice = "en-US-AriaNeural"
	params.ReactionsEnabled = true
	params.ReactionsMode = "MINIMAL"
	params.RuntimeChannel = "telegram"
	params.InlineButtons = true
	params.MessageChannels = []string{"telegram", "whatsapp", "discord", "signal", "slack"}

	// Build prompt-format aliases from config (add "anthropic/" prefix where needed).
	// Fall back to config.DefaultModelAliases() when none are provided.
	if len(modelAliases) == 0 {
		modelAliases = config.DefaultModelAliases()
	}
	promptAliases := make(map[string]string, len(modelAliases))
	for alias, model := range modelAliases {
		if alias == "default" || model == "" {
			continue // skip the "default" reset alias in the prompt
		}
		if !strings.Contains(model, "/") {
			model = "anthropic/" + model
		}
		promptAliases[alias] = model
	}
	params.ModelAliases = promptAliases

	if workspaceContext != nil {
		params.WorkspaceDir = workspaceContext.GetWorkspaceDir()
	}

	return &PromptBuilder{
		agentName:        agentName,
		personality:      personality,
		identity:         identity,
		capabilities:     capabilities,
		tools:            tools,
		workspaceContext: workspaceContext,
		skillsManager:    skillsManager,
		sectionParams:    params,
	}
}

// Build constructs the complete system prompt
func (pb *PromptBuilder) Build(ctx context.Context, session *sessions.Session, isOAuth bool) ([]ai.SystemBlock, error) {
	pb.sectionParams.Session = session

	// Determine if minimal mode
	isMinimal := false // Could be set based on config
	pb.sectionParams.IsMinimal = isMinimal

	// Build the complete prompt text
	promptText := pb.buildFullPrompt(ctx, session, isOAuth)

	return []ai.SystemBlock{
		{
			Type: "text",
			Text: promptText,
		},
	}, nil
}

// buildFullPrompt creates the complete system prompt text
func (pb *PromptBuilder) buildFullPrompt(ctx context.Context, session *sessions.Session, isOAuth bool) string {
	var sections []string

	// 1. Core Identity
	sections = append(sections, pb.buildIdentitySection(isOAuth))

	// 2. Tooling section
	sections = append(sections, pb.buildToolingSection())

	// 3. Tool Call Style
	sections = append(sections, pb.buildToolCallStyleSection())

	// 4. Safety
	sections = append(sections, buildSafetySection(pb.sectionParams.IsMinimal))

	// 5. Conduit CLI Quick Reference
	sections = append(sections, buildConduitCLISection(pb.sectionParams.IsMinimal))

	// 6. Skills section (if available)
	if pb.capabilities.SkillsIntegration && pb.skillsManager != nil {
		if skillsSection := pb.buildSkillsSection(ctx); skillsSection != "" {
			sections = append(sections, skillsSection)
		}
	}

	// 7. Memory Recall (if memory tools available)
	sections = append(sections, buildMemorySection(pb.sectionParams))

	// 7b. Memory Persistence (writing to memory files)
	sections = append(sections, buildMemoryPersistenceSection(pb.sectionParams))

	// 8. Self-Update (if gateway tool available)
	sections = append(sections, buildSelfUpdateSection(pb.sectionParams))

	// 9. Model Aliases
	sections = append(sections, buildModelAliasesSection(pb.sectionParams))

	// 10. Timezone (uses session_status hint)
	if pb.sectionParams.UserTimezone != "" {
		sections = append(sections, "If you need the current date, time, or day of week, run session_status (📊 session_status).")
	}

	// 11. Workspace
	sections = append(sections, pb.buildWorkspaceSection())

	// 12. Documentation
	sections = append(sections, buildDocsSection(pb.sectionParams))

	// 13. Reply Tags
	sections = append(sections, buildReplyTagsSection(pb.sectionParams.IsMinimal))

	// 14. Messaging
	sections = append(sections, buildMessagingSection(pb.sectionParams))

	// 15. Voice/TTS
	sections = append(sections, buildVoiceSection(pb.sectionParams))

	// 16. Reactions
	sections = append(sections, buildReactionsSection(pb.sectionParams))

	// 17. Project Context (workspace files)
	if workspaceSection := pb.buildWorkspaceContextSection(ctx, session); workspaceSection != "" {
		sections = append(sections, workspaceSection)
	}

	// 18. Silent Replies
	sections = append(sections, buildSilentRepliesSection(pb.sectionParams.IsMinimal))

	// 19. Heartbeats
	sections = append(sections, buildHeartbeatsSection(pb.sectionParams))

	// 20. Runtime
	runtimeInfo := pb.buildRuntimeInfo(session)
	sections = append(sections, buildRuntimeSection(pb.sectionParams, runtimeInfo))

	// Filter empty sections and join
	var nonEmpty []string
	for _, s := range sections {
		if strings.TrimSpace(s) != "" {
			nonEmpty = append(nonEmpty, strings.TrimSpace(s))
		}
	}

	return strings.Join(nonEmpty, "\n\n")
}

// buildIdentitySection creates the identity/personality section
func (pb *PromptBuilder) buildIdentitySection(isOAuth bool) string {
	var identity string
	if isOAuth {
		identity = pb.identity.OAuthIdentity
	} else {
		identity = pb.identity.APIKeyIdentity
	}

	var builder strings.Builder

	if identity != "" {
		builder.WriteString(identity)
		builder.WriteString(" You are running inside Conduit.\n")
	} else {
		builder.WriteString("You are a personal assistant running inside Conduit.\n")
	}

	return builder.String()
}

// buildToolingSection creates the tools availability section
func (pb *PromptBuilder) buildToolingSection() string {
	var builder strings.Builder

	builder.WriteString("## Tooling\n")
	builder.WriteString("Tool availability (filtered by policy):\n")
	builder.WriteString("Tool names are case-sensitive. Call tools exactly as listed.\n")

	for _, tool := range pb.tools {
		builder.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description))
	}

	builder.WriteString("TOOLS.md does not control tool availability; it is user guidance for how to use external tools.\n")
	builder.WriteString("If a task is more complex or takes longer, spawn a sub-agent. It will do the work for you and ping you when it's done. You can always check up on it.\n")

	return builder.String()
}

// buildToolCallStyleSection creates tool call style guidelines
func (pb *PromptBuilder) buildToolCallStyleSection() string {
	return `## Tool Call Style
Default: do not narrate routine, low-risk tool calls (just call the tool).
Narrate only when it helps: multi-step work, complex/challenging problems, sensitive actions (e.g., deletions), or when the user explicitly asks.
Keep narration brief and value-dense; avoid repeating obvious steps.
Use plain human language for narration unless in a technical context.`
}

// buildWorkspaceSection creates workspace directory info
func (pb *PromptBuilder) buildWorkspaceSection() string {
	return fmt.Sprintf(`## Workspace
Your working directory is: %s
Treat this directory as the single global workspace for file operations unless explicitly instructed otherwise.`, pb.sectionParams.WorkspaceDir)
}

// buildSkillsSection creates skills integration context
func (pb *PromptBuilder) buildSkillsSection(ctx context.Context) string {
	if pb.skillsManager == nil || !pb.skillsManager.IsEnabled() {
		return ""
	}

	// Initialize if needed
	if !pb.skillsManager.IsInitialized() {
		if err := pb.skillsManager.Initialize(ctx); err != nil {
			return ""
		}
	}

	skillsContext, err := pb.skillsManager.BuildSystemPromptContext(ctx)
	if err != nil || skillsContext == "" {
		return ""
	}

	return fmt.Sprintf("## Skills (mandatory)\n%s", skillsContext)
}

// buildWorkspaceContextSection loads and formats workspace files
func (pb *PromptBuilder) buildWorkspaceContextSection(ctx context.Context, session *sessions.Session) string {
	if pb.workspaceContext == nil {
		return ""
	}

	// Determine session type
	sessionType := "main"
	channelID := ""
	userID := ""
	sessionKey := ""

	if session != nil {
		channelID = session.ChannelID
		userID = session.UserID
		sessionKey = session.Key

		if strings.Contains(channelID, "group") || strings.Contains(channelID, "-100") {
			sessionType = "shared"
		}
	}

	securityCtx := workspace.SecurityContext{
		SessionType: sessionType,
		ChannelID:   channelID,
		UserID:      userID,
		SessionID:   sessionKey,
	}

	bundle, err := pb.workspaceContext.LoadContext(ctx, securityCtx)
	if err != nil || len(bundle.Files) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Project Context\n\n")
	builder.WriteString("The following project context files have been loaded:\n")
	builder.WriteString("If SOUL.md is present, embody its persona and tone. Avoid stiff, generic replies; follow its guidance unless higher-priority instructions override it.\n\n")

	// Core files in specific order
	coreFiles := []string{"SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "IDENTITY.md", "HEARTBEAT.md", "BOOTSTRAP.md"}
	for _, filename := range coreFiles {
		if content, exists := bundle.Files[filename]; exists {
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// Memory files
	for filename, content := range bundle.Files {
		if strings.HasPrefix(filename, "memory/") && strings.HasSuffix(filename, ".md") {
			if len(content) > 4000 {
				content = content[:4000] + "\n...(truncated)"
			}
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// MEMORY.md only in main sessions
	if sessionType == "main" {
		if content, exists := bundle.Files["MEMORY.md"]; exists {
			builder.WriteString(fmt.Sprintf("## MEMORY.md\n%s\n", content))
		}
	}

	return builder.String()
}

// buildRuntimeInfo creates runtime information map
func (pb *PromptBuilder) buildRuntimeInfo(session *sessions.Session) map[string]string {
	info := make(map[string]string)

	info["agent"] = "main"

	hostname, _ := os.Hostname()
	info["host"] = hostname

	info["repo"] = pb.sectionParams.WorkspaceDir

	info["os"] = fmt.Sprintf("%s %s (%s)", runtime.GOOS, "", runtime.GOARCH)

	info["node"] = runtime.Version()

	// Get model from session context, or use default
	model := "anthropic/claude-sonnet-4-20250514"
	if session != nil && session.Context != nil && session.Context["model"] != "" {
		model = session.Context["model"]
	}
	info["model"] = model

	info["channel"] = pb.sectionParams.RuntimeChannel

	return info
}
