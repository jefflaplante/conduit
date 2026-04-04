package agent

import (
	"context"
	"fmt"
	"log"
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
	name     string // human-readable name for compact-mode notice
	priority int    // 1=critical, 4=nice-to-have
	build    func() string
	cached   string // cached result of build() to avoid double-building
	built    bool   // whether cached has been populated
}

// PromptSectionInfo describes a single section of the system prompt for debug inspection.
type PromptSectionInfo struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Chars    int    `json:"chars"`
	Included bool   `json:"included"`
}

// PromptDebugInfo provides a complete debug snapshot of the system prompt.
type PromptDebugInfo struct {
	PromptText        string              `json:"prompt_text"`
	TotalChars        int                 `json:"total_chars"`
	EstimatedTokens   int                 `json:"estimated_tokens"`
	ContextWindow     int                 `json:"context_window"`
	BudgetChars       int                 `json:"budget_chars"`
	BudgetConstrained bool                `json:"budget_constrained"`
	Sections          []PromptSectionInfo `json:"sections"`
	DroppedSections   []string            `json:"dropped_sections"`
}

// PromptBuilder handles building system prompts with full Conduit integration
type PromptBuilder struct {
	agentName        string
	personality      string
	email            config.AgentEmail
	identity         IdentityConfig
	capabilities     AgentCapabilities
	tools            []ai.Tool
	workspaceContext *workspace.WorkspaceContext
	summaryManager   *workspace.SummaryManager
	skillsManager    *skills.Manager
	sectionParams    *SectionParams
	promptScaling    config.PromptScalingConfig
}

// NewPromptBuilder creates a new prompt builder with full integration.
// modelAliases maps short names (e.g. "haiku") to full model identifiers.
// If nil, a built-in default set is used.
// promptScaling controls budget allocation for small-context models.
// summaryManager is optional; if provided, enables AI-powered summarization for small-context models.
func NewPromptBuilder(
	agentName, personality string,
	email config.AgentEmail,
	identity IdentityConfig,
	capabilities AgentCapabilities,
	tools []ai.Tool,
	workspaceContext *workspace.WorkspaceContext,
	summaryManager *workspace.SummaryManager,
	skillsManager *skills.Manager,
	modelAliases map[string]string,
	promptScaling *config.PromptScalingConfig,
	timezone string,
	runtimeChannel string,
) *PromptBuilder {
	params := NewSectionParams(tools)

	// Set defaults that can be overridden
	params.WorkspaceDir = "./workspace"
	if timezone != "" {
		params.UserTimezone = timezone
	} else {
		params.UserTimezone = "UTC"
	}
	params.HeartbeatPrompt = "Read HEARTBEAT.md if it exists (workspace context). Follow it strictly. Do not infer or repeat old tasks from prior chats. If nothing needs attention, reply HEARTBEAT_OK."
	params.TTSEnabled = true
	params.TTSVoice = "en-US-AriaNeural"
	params.ReactionsEnabled = true
	params.ReactionsMode = "MINIMAL"
	if runtimeChannel != "" {
		params.RuntimeChannel = runtimeChannel
	} else {
		params.RuntimeChannel = "websocket"
	}
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
		email:            email,
		identity:         identity,
		capabilities:     capabilities,
		tools:            tools,
		workspaceContext: workspaceContext,
		summaryManager:   summaryManager,
		skillsManager:    skillsManager,
		sectionParams:    params,
		promptScaling:    scaling,
	}
}

// Build constructs the complete system prompt
func (pb *PromptBuilder) Build(ctx context.Context, session *sessions.Session, isOAuth bool) ([]ai.SystemBlock, error) {
	// Work on a local copy of sectionParams to avoid mutating shared state.
	// This makes Build safe to call concurrently with different sessions.
	localParams := *pb.sectionParams
	localParams.Session = session

	// Determine if minimal mode
	isMinimal := false // Could be set based on config
	localParams.IsMinimal = isMinimal

	// Build the complete prompt text using the local copy
	promptText := pb.buildFullPromptWithParams(ctx, session, isOAuth, &localParams)

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
	return pb.buildFullPromptWithParams(ctx, session, isOAuth, pb.sectionParams)
}

// buildFullPromptWithParams creates the complete system prompt text using the given params.
// This avoids mutating shared state and is safe for concurrent use with different sessions.
func (pb *PromptBuilder) buildFullPromptWithParams(ctx context.Context, session *sessions.Session, isOAuth bool, params *SectionParams) string {
	isCron := session != nil && strings.HasPrefix(session.Key, CronSessionKeyPrefix)

	// Build the priority-tagged section list using the provided params.
	allSections := pb.buildSectionListWithParams(ctx, session, isOAuth, isCron, params)

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

	if len(dropped) > 0 {
		log.Printf("[PromptBuilder] Budget-constrained: dropped sections %v (budget=%d chars, model context=%d)",
			dropped, budgetChars, contextWindow)
	}

	return joinSectionsWithCache(allSections, included, dropped...)
}

// BuildDebug constructs the system prompt and returns detailed debug info about each section.
// Always bypasses the prompt cache.
func (pb *PromptBuilder) BuildDebug(ctx context.Context, session *sessions.Session, isOAuth bool) (*PromptDebugInfo, error) {
	localParams := *pb.sectionParams
	localParams.Session = session
	localParams.IsMinimal = false

	isCron := session != nil && strings.HasPrefix(session.Key, CronSessionKeyPrefix)
	allSections := pb.buildSectionListWithParams(ctx, session, isOAuth, isCron, &localParams)

	// Determine context window from session model.
	model := ""
	if session != nil && session.Context != nil {
		model = session.Context["model"]
	}
	contextWindow := ai.ContextWindowForModel(model)

	largeCtxThreshold := pb.promptScaling.LargeContextThreshold
	if largeCtxThreshold <= 0 {
		largeCtxThreshold = defaultLargeContextThreshold
	}

	cpt := pb.promptScaling.CharsPerToken
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}

	budgetPercent := pb.promptScaling.PromptBudgetPercent
	if budgetPercent <= 0 {
		budgetPercent = defaultPromptBudgetPercent
	}

	budgetConstrained := contextWindow < largeCtxThreshold
	budgetChars := contextWindow * budgetPercent / 100 * cpt

	// Build and cache all sections.
	for i := range allSections {
		if !allSections[i].built {
			allSections[i].cached = strings.TrimSpace(allSections[i].build())
			allSections[i].built = true
		}
	}

	// Determine inclusion per section.
	usedChars := 0
	included := make([]bool, len(allSections))
	var droppedNames []string
	sectionInfos := make([]PromptSectionInfo, len(allSections))

	for i := range allSections {
		text := allSections[i].cached
		chars := len(text)
		info := PromptSectionInfo{
			Name:     allSections[i].name,
			Priority: allSections[i].priority,
			Chars:    chars,
		}

		if !budgetConstrained || text == "" {
			info.Included = true
			included[i] = true
			usedChars += chars
		} else if usedChars+chars <= budgetChars {
			info.Included = true
			included[i] = true
			usedChars += chars
		} else {
			info.Included = false
			droppedNames = append(droppedNames, allSections[i].name)
		}

		sectionInfos[i] = info
	}

	promptText := joinSectionsWithCache(allSections, included, droppedNames...)
	totalChars := len(promptText)

	return &PromptDebugInfo{
		PromptText:        promptText,
		TotalChars:        totalChars,
		EstimatedTokens:   totalChars / cpt,
		ContextWindow:     contextWindow,
		BudgetChars:       budgetChars,
		BudgetConstrained: budgetConstrained,
		Sections:          sectionInfos,
		DroppedSections:   droppedNames,
	}, nil
}

// buildSectionList returns all prompt sections tagged with priorities.
// Sections are ordered by priority (1 first), preserving relative order within each priority.
func (pb *PromptBuilder) buildSectionList(ctx context.Context, session *sessions.Session, isOAuth, isCron bool) []promptSection {
	return pb.buildSectionListWithParams(ctx, session, isOAuth, isCron, pb.sectionParams)
}

// buildSectionListWithParams returns all prompt sections using the provided params.
func (pb *PromptBuilder) buildSectionListWithParams(ctx context.Context, session *sessions.Session, isOAuth, isCron bool, params *SectionParams) []promptSection {

	// Define all sections with priorities.
	// P1=critical (never dropped), P2=grounding data and reference, P3=behavioral rules, P4=cosmetic/optional.
	// Within each priority, declaration order is preserved by stable sort.
	// Ordering principle: data/context before instructions (per Anthropic prompt engineering guidelines).
	raw := []promptSection{
		// P1 — Critical: identity and runtime facts (never dropped)
		{name: "Identity", priority: 1, build: func() string { return pb.buildIdentitySection(isOAuth) }},
		{name: "Runtime", priority: 1, build: func() string {
			return buildRuntimeSection(params, pb.buildRuntimeInfo(session))
		}},

		// P2 — Grounding data: project context, memory, tool availability (reference)
		{name: "Project Context", priority: 2, build: func() string { return pb.buildWorkspaceContextSection(ctx, session) }},
		{name: "Memory Recall", priority: 2, build: func() string { return buildMemorySection(params) }},
		{name: "Memory Persistence", priority: 2, build: func() string { return buildMemoryPersistenceSection(params) }},
		{name: "Brain", priority: 2, build: func() string { return buildBrainSection(params) }},
		{name: "Tooling", priority: 2, build: func() string { return pb.buildToolingSection() }},
		{name: "Heartbeats", priority: 2, build: func() string { return buildHeartbeatsSection(params) }},
		{name: "Messaging", priority: 2, build: func() string { return buildMessagingSection(params) }},
		{name: "Email", priority: 2, build: func() string { return pb.buildEmailSection() }},
		{name: "Cron Delivery", priority: 2, build: func() string {
			if isCron {
				return buildCronDeliverySection(params)
			}
			return ""
		}},
		{name: "Workspace", priority: 2, build: func() string { return pb.buildWorkspaceSection() }},

		// P3 — Behavioral rules: how to use tools, error handling, safety
		{name: "Tool Strategy", priority: 3, build: func() string { return buildToolStrategySection(params.IsMinimal) }},
		{name: "Tool Integrity", priority: 3, build: func() string { return pb.buildToolCallStyleSection() }},
		{name: "Error Recovery", priority: 3, build: func() string { return buildErrorRecoverySection(params.IsMinimal) }},
		{name: "Safety", priority: 3, build: func() string { return buildSafetySection(params.IsMinimal) }},
		{name: "Skills", priority: 2, build: func() string {
			if pb.capabilities.SkillsIntegration && pb.skillsManager != nil {
				return pb.buildSkillsSection(ctx, session)
			}
			return ""
		}},
		{name: "MQTT/IoT", priority: 3, build: func() string { return buildMQTTSection(params) }},
		{name: "Reply Tags", priority: 3, build: func() string { return buildReplyTagsSection(params.IsMinimal) }},
		{name: "Model Aliases", priority: 3, build: func() string { return buildModelAliasesSection(params) }},
		{name: "Docs", priority: 3, build: func() string { return buildDocsSection(params) }},

		// P4 — Nice-to-have: cosmetic features, CLI reference
		{name: "Silent Replies", priority: 4, build: func() string { return buildSilentRepliesSection(params.IsMinimal) }},
		{name: "Voice/TTS", priority: 4, build: func() string { return buildVoiceSection(params) }},
		{name: "Reactions", priority: 4, build: func() string { return buildReactionsSection(params) }},
		{name: "Conduit CLI", priority: 4, build: func() string { return buildConduitCLISection(params.IsMinimal) }},
		{name: "Self-Update", priority: 4, build: func() string { return buildSelfUpdateSection(params) }},
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

// defaultOperatingPrinciples are used when no custom principles are configured.
var defaultOperatingPrinciples = []string{
	"Think before acting. Understand what is being asked before executing.",
	"Ask before destroying. Confirm deletions, restarts, or irreversible changes.",
	"Verify before claiming. (See Tool Integrity for fabrication rules.)",
	"Understand blast radius. Know what systems, users, or data an action affects.",
	"Write down what you learn. Memory resets between sessions; files persist.",
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
		// Default role statement: sardonic assistant + home automation agent
		builder.WriteString("You are a sardonic, competent personal assistant and home automation agent. You are running inside Conduit.\n")
	}

	// Add operating principles
	principles := pb.identity.OperatingPrinciples
	if len(principles) == 0 {
		principles = defaultOperatingPrinciples
	}

	builder.WriteString("\n## Operating Principles\n")
	for _, p := range principles {
		builder.WriteString("- ")
		builder.WriteString(p)
		builder.WriteString("\n")
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
	builder.WriteString("To delegate work, call SessionsSpawn — this is the ONLY way to spawn a sub-agent. Never claim you spawned one without the tool call. Results arrive automatically (announce=true) or via SessionStatus (announce=false).\n")

	return builder.String()
}

// buildToolCallStyleSection creates tool integrity and style guidelines
func (pb *PromptBuilder) buildToolCallStyleSection() string {
	return `## Tool Integrity
**Never fabricate tool results.**
- Always call the tool before reporting its results.
- Always confirm tool execution succeeded before claiming an action was completed.
- Always include proof — log lines, tool output, return values. Receipts or it didn't happen.
- If you cannot call a tool, say so explicitly rather than approximating.

**Narration style:** For routine tool calls, call the tool without announcing it first — but ALWAYS actually call it. "Silent" means no narration, not no tool call. Narrate when it helps: multi-step work, complex problems, sensitive actions, or when the user explicitly asks. Keep narration brief.`
}

// buildWorkspaceSection creates workspace directory info
func (pb *PromptBuilder) buildWorkspaceSection() string {
	return fmt.Sprintf(`## Workspace
Your working directory is: %s
Treat this directory as the single global workspace for file operations unless explicitly instructed otherwise.
Key locations: MEMORY.md (long-term memory), memory/ (daily logs), HEARTBEAT.md (monitoring tasks), SOUL.md (personality).`, pb.sectionParams.WorkspaceDir)
}

// buildEmailSection creates the email identity section
func (pb *PromptBuilder) buildEmailSection() string {
	if pb.email.Address == "" {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("## Email\n")
	builder.WriteString(fmt.Sprintf("Your email address: %s\n", pb.email.Address))

	displayName := pb.email.DisplayName
	if displayName == "" {
		displayName = pb.agentName
	}
	builder.WriteString(fmt.Sprintf("Display name: %s\n", displayName))

	if len(pb.email.Aliases) > 0 {
		builder.WriteString(fmt.Sprintf("Aliases: %s\n", strings.Join(pb.email.Aliases, ", ")))
	}

	builder.WriteString("Use this address as your \"from\" identity when composing or referencing email. Recognize messages to any of these addresses as addressed to you.\n")
	return builder.String()
}

// buildSkillsSection creates skills integration context.
// When session contains a "skill_filter", only those skills are included in the prompt.
func (pb *PromptBuilder) buildSkillsSection(ctx context.Context, session *sessions.Session) string {
	if pb.skillsManager == nil || !pb.skillsManager.IsEnabled() {
		return ""
	}

	// Initialize if needed
	if !pb.skillsManager.IsInitialized() {
		if err := pb.skillsManager.Initialize(ctx); err != nil {
			return ""
		}
	}

	// Parse skill filter from session context
	var skillFilter map[string]bool
	if session != nil && session.Context != nil {
		if filterStr := session.Context["skill_filter"]; filterStr != "" {
			skillFilter = make(map[string]bool)
			for _, name := range strings.Split(filterStr, ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					skillFilter[trimmed] = true
				}
			}
		}
	}

	var skillsContext string
	var err error
	if len(skillFilter) > 0 {
		skillsContext, err = pb.skillsManager.BuildSystemPromptContextFiltered(ctx, skillFilter)
	} else {
		skillsContext, err = pb.skillsManager.BuildSystemPromptContext(ctx)
	}
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

	// Check if we should use summarized content for small-context models
	files := bundle.Files
	if pb.shouldUseSummaries(session) && pb.summaryManager != nil {
		summarized, err := pb.summaryManager.GetSummarizedContext(ctx, bundle.Files)
		if err == nil {
			files = summarized
		}
		// On error, fall through to use full content
	}

	var builder strings.Builder
	builder.WriteString("# Project Context\n\n")
	builder.WriteString("The following project context files have been loaded:\n")
	builder.WriteString("If SOUL.md is present, embody its persona and tone. Avoid stiff, generic replies; follow its guidance unless higher-priority instructions override it.\n\n")

	// Core files in specific order
	coreFiles := []string{"SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "IDENTITY.md", "HEARTBEAT.md", "BOOTSTRAP.md"}
	for _, filename := range coreFiles {
		if content, exists := files[filename]; exists {
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// Memory files
	for filename, content := range files {
		if strings.HasPrefix(filename, "memory/") && strings.HasSuffix(filename, ".md") {
			if len(content) > 4000 {
				content = content[:4000] + "\n...(truncated)"
			}
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// MEMORY.md only in main sessions
	if sessionType == "main" {
		if content, exists := files["MEMORY.md"]; exists {
			builder.WriteString(fmt.Sprintf("## MEMORY.md\n%s\n", content))
		}
	}

	return builder.String()
}

// shouldUseSummaries determines if summarized content should be used
func (pb *PromptBuilder) shouldUseSummaries(session *sessions.Session) bool {
	if pb.summaryManager == nil || !pb.summaryManager.IsEnabled() {
		return false
	}

	// Determine context window from session model
	model := ""
	if session != nil && session.Context != nil {
		model = session.Context["model"]
	}
	contextWindow := ai.ContextWindowForModel(model)

	// Use config threshold
	threshold := pb.promptScaling.LargeContextThreshold
	if threshold <= 0 {
		threshold = defaultLargeContextThreshold
	}

	return workspace.ShouldSummarize(contextWindow, threshold)
}

// buildRuntimeInfo creates runtime information map
func (pb *PromptBuilder) buildRuntimeInfo(session *sessions.Session) map[string]string {
	info := make(map[string]string)

	info["agent"] = "main"

	hostname, _ := os.Hostname()
	info["host"] = hostname

	info["repo"] = pb.sectionParams.WorkspaceDir

	info["os"] = fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH)

	info["node"] = runtime.Version()

	// Get model from session context, or fall back to config default.
	model := ""
	if session != nil && session.Context != nil && session.Context["model"] != "" {
		model = session.Context["model"]
	}
	if model == "" {
		model = config.DefaultModelAliases()["default"]
	}
	info["model"] = model

	info["channel"] = pb.sectionParams.RuntimeChannel

	return info
}
