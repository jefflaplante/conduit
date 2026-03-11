package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

// CronSessionKeyPrefix is the prefix used for session keys created by cron/scheduled jobs.
const CronSessionKeyPrefix = "cron_"

// Default prompt scaling constants (used when config not provided).
const (
	defaultLargeContextThreshold = 128000 // tokens; skip budget logic above this
	defaultPromptBudgetPercent   = 15     // % of context window allocated to system prompt
	defaultCharsPerToken         = 4      // rough chars-per-token estimate for budget math
)

// promptSection pairs a builder function with its priority for budget-based inclusion.
type promptSection struct {
	name     string       // human-readable name for compact-mode notice
	priority int          // 1=critical, 4=nice-to-have
	build    func() string
	cached   string       // cached result of build() to avoid double-building
	built    bool         // whether cached has been populated
}

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
	promptScaling    config.PromptScalingConfig
}

// NewPromptBuilder creates a new prompt builder with full integration.
// modelAliases maps short names (e.g. "haiku") to full model identifiers.
// If nil, a built-in default set is used.
// promptScaling controls budget allocation for small-context models.
func NewPromptBuilder(
	agentName, personality string,
	identity IdentityConfig,
	capabilities AgentCapabilities,
	tools []ai.Tool,
	workspaceContext *workspace.WorkspaceContext,
	skillsManager *skills.Manager,
	modelAliases map[string]string,
	promptScaling *config.PromptScalingConfig,
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
	params.MessageChannels = SupportedChannels

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

	// Use provided scaling config or defaults
	scaling := config.DefaultPromptScalingConfig()
	if promptScaling != nil {
		if promptScaling.LargeContextThreshold > 0 {
			scaling.LargeContextThreshold = promptScaling.LargeContextThreshold
		}
		if promptScaling.PromptBudgetPercent > 0 {
			scaling.PromptBudgetPercent = promptScaling.PromptBudgetPercent
		}
		if promptScaling.CharsPerToken > 0 {
			scaling.CharsPerToken = promptScaling.CharsPerToken
		}
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
		promptScaling:    scaling,
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

// buildFullPrompt creates the complete system prompt text.
// For models with large context windows (>= threshold), all sections are included.
// For smaller models, sections are included by priority order within a token budget.
func (pb *PromptBuilder) buildFullPrompt(ctx context.Context, session *sessions.Session, isOAuth bool) string {
	isCron := session != nil && strings.HasPrefix(session.Key, CronSessionKeyPrefix)

	// Build the priority-tagged section list. Order within each priority is preserved.
	allSections := pb.buildSectionList(ctx, session, isOAuth, isCron)

	// Determine context window from session model.
	model := ""
	if session != nil && session.Context != nil {
		model = session.Context["model"]
	}
	contextWindow := ai.ContextWindowForModel(model)

	// Use config values for scaling thresholds
	largeCtxThreshold := pb.promptScaling.LargeContextThreshold
	if largeCtxThreshold <= 0 {
		largeCtxThreshold = defaultLargeContextThreshold
	}

	// Short circuit: large-context models get everything.
	if contextWindow >= largeCtxThreshold {
		return joinSectionsWithCache(allSections, nil)
	}

	// Get budget parameters from config
	budgetPercent := pb.promptScaling.PromptBudgetPercent
	if budgetPercent <= 0 {
		budgetPercent = defaultPromptBudgetPercent
	}
	cpt := pb.promptScaling.CharsPerToken
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}

	// Budget-constrained assembly for small-context models.
	budgetChars := contextWindow * budgetPercent / 100 * cpt
	usedChars := 0
	included := make([]bool, len(allSections))
	var dropped []string

	// Sections are already ordered by priority (stable within same priority).
	// Build and cache each section's text to avoid double-building.
	for i := range allSections {
		if !allSections[i].built {
			allSections[i].cached = strings.TrimSpace(allSections[i].build())
			allSections[i].built = true
		}
		text := allSections[i].cached
		if text == "" {
			included[i] = true // empty sections are free
			continue
		}
		cost := len(text)
		if usedChars+cost <= budgetChars {
			usedChars += cost
			included[i] = true
		} else {
			dropped = append(dropped, allSections[i].name)
		}
	}

	return joinSectionsWithCache(allSections, included, dropped...)
}

// buildSectionList returns all prompt sections tagged with priorities.
// Sections are ordered by priority (1 first), preserving relative order within each priority.
func (pb *PromptBuilder) buildSectionList(ctx context.Context, session *sessions.Session, isOAuth, isCron bool) []promptSection {
	params := pb.sectionParams

	// Define all sections with priorities.
	// P1=critical, P2=needed for delivery, P3=enhances behavior, P4=largest/optional.
	raw := []promptSection{
		// P1 — Critical
		{name: "Identity", priority: 1, build: func() string { return pb.buildIdentitySection(isOAuth) }},
		{name: "Tooling", priority: 1, build: func() string { return pb.buildToolingSection() }},
		{name: "Tool Call Style", priority: 1, build: func() string { return pb.buildToolCallStyleSection() }},
		{name: "Silent Replies", priority: 1, build: func() string { return buildSilentRepliesSection(params.IsMinimal) }},
		{name: "Runtime", priority: 1, build: func() string {
			return buildRuntimeSection(params, pb.buildRuntimeInfo(session))
		}},

		// P2 — Needed for proper channel delivery and tool usage
		{name: "Reply Tags", priority: 2, build: func() string { return buildReplyTagsSection(params.IsMinimal) }},
		{name: "Messaging", priority: 2, build: func() string { return buildMessagingSection(params) }},
		{name: "Cron Delivery", priority: 2, build: func() string {
			if isCron {
				return buildCronDeliverySection(params)
			}
			return ""
		}},
		{name: "Workspace", priority: 2, build: func() string { return pb.buildWorkspaceSection() }},
		{name: "Docs", priority: 2, build: func() string { return buildDocsSection(params) }},
		{name: "Model Aliases", priority: 2, build: func() string { return buildModelAliasesSection(params) }},
		{name: "Timezone", priority: 2, build: func() string {
			if params.UserTimezone != "" {
				return "If you need the current date, time, or day of week, run session_status."
			}
			return ""
		}},

		// P3 — Enhance behavior but model works without
		{name: "Safety", priority: 3, build: func() string { return buildSafetySection(params.IsMinimal) }},
		{name: "Memory Recall", priority: 3, build: func() string { return buildMemorySection(params) }},
		{name: "Memory Persistence", priority: 3, build: func() string { return buildMemoryPersistenceSection(params) }},
		{name: "Voice/TTS", priority: 3, build: func() string { return buildVoiceSection(params) }},
		{name: "Reactions", priority: 3, build: func() string { return buildReactionsSection(params) }},
		{name: "Heartbeats", priority: 3, build: func() string { return buildHeartbeatsSection(params) }},

		// P4 — Largest sections; nice-to-have
		{name: "Conduit CLI", priority: 4, build: func() string { return buildConduitCLISection(params.IsMinimal) }},
		{name: "Skills", priority: 4, build: func() string {
			if pb.capabilities.SkillsIntegration && pb.skillsManager != nil {
				return pb.buildSkillsSection(ctx)
			}
			return ""
		}},
		{name: "Self-Update", priority: 4, build: func() string { return buildSelfUpdateSection(params) }},
		{name: "Project Context", priority: 4, build: func() string { return pb.buildWorkspaceContextSection(ctx, session) }},
	}

	// Stable sort by priority (preserves order within same priority).
	sort.SliceStable(raw, func(i, j int) bool {
		return raw[i].priority < raw[j].priority
	})

	return raw
}

// joinSections assembles prompt text from sections (legacy, rebuilds each section).
// If included is nil, all sections are built. Otherwise only included[i]==true sections are used.
// If dropped names are provided, a compact-mode notice is appended.
func joinSections(sections []promptSection, included []bool, dropped ...string) string {
	var nonEmpty []string
	for i, sec := range sections {
		if included != nil && !included[i] {
			continue
		}
		text := strings.TrimSpace(sec.build())
		if text != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}

	result := strings.Join(nonEmpty, "\n\n")

	if len(dropped) > 0 {
		result += fmt.Sprintf("\n\n---\n[Compact mode: omitted %s to fit context window. Core capabilities remain active.]",
			strings.Join(dropped, ", "))
	}

	return result
}

// joinSectionsWithCache assembles prompt text using cached section values when available.
// This avoids double-building sections during budget calculation and final assembly.
func joinSectionsWithCache(sections []promptSection, included []bool, dropped ...string) string {
	var nonEmpty []string
	for i := range sections {
		if included != nil && !included[i] {
			continue
		}

		var text string
		if sections[i].built {
			// Use cached value
			text = sections[i].cached
		} else {
			// Build and cache
			text = strings.TrimSpace(sections[i].build())
			sections[i].cached = text
			sections[i].built = true
		}

		if text != "" {
			nonEmpty = append(nonEmpty, text)
		}
	}

	result := strings.Join(nonEmpty, "\n\n")

	if len(dropped) > 0 {
		result += fmt.Sprintf("\n\n---\n[Compact mode: omitted %s to fit context window. Core capabilities remain active.]",
			strings.Join(dropped, ", "))
	}

	return result
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
