package agent

import (
	"context"
	"log"
	"strings"
	"time"

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
	// dynamic marks per-turn content (timestamp, wake source, brain state).
	// Rendered in the trailing uncached system block (conduit-31jg.14).
	dynamic bool
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
	brainService     BrainLister
}

// NewPromptBuilder creates a new prompt builder with full integration.
// modelAliases maps short names (e.g. "haiku") to full model identifiers.
// If nil, a built-in default set is used.
// promptScaling controls budget allocation for small-context models.
// summaryManager is optional; if provided, enables AI-powered summarization for small-context models.
// brainService is optional; if provided, enables Situation Awareness section with reflection data.
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
	brainService BrainLister,
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
		brainService:     brainService,
	}
}

// Build constructs the complete system prompt
func (pb *PromptBuilder) Build(ctx context.Context, session *sessions.Session, isOAuth bool) ([]ai.SystemBlock, error) {
	// conduit-31jg.14: two blocks. The static block is byte-stable between
	// turns and carries the provider cache breakpoint; the dynamic block
	// (timestamp, wake context, situation awareness) follows it, outside the
	// cached prefix. Providers without block support join them with "\n\n".
	return pb.buildSplit(ctx, session, isOAuth).blocks(), nil
}

// promptSplit is a built prompt divided into its byte-stable static part and
// its per-turn dynamic part. dynamicSections names the dynamic sections the
// budget pass included, so the agent-level prompt cache can keep the static
// text and re-render only those sections each turn (conduit-31jg.65).
type promptSplit struct {
	static          string
	dynamic         string
	dynamicSections []string
}

func (p promptSplit) blocks() []ai.SystemBlock {
	blocks := []ai.SystemBlock{{Type: "text", Text: p.static}}
	if p.dynamic != "" {
		blocks = append(blocks, ai.SystemBlock{Type: "text", Text: p.dynamic, Dynamic: true})
	}
	return blocks
}

// buildSplit builds the full prompt (static + dynamic) for a session. It
// works on a local copy of sectionParams, so it is safe to call concurrently
// with different sessions.
func (pb *PromptBuilder) buildSplit(ctx context.Context, session *sessions.Session, isOAuth bool) promptSplit {
	localParams := *pb.sectionParams
	localParams.Session = session
	localParams.IsMinimal = false
	return pb.buildSplitWithParams(ctx, session, isOAuth, &localParams)
}

// BuildDynamic renders only the named dynamic sections (time, wake context,
// situation awareness) — the per-turn block that follows a cached static
// block. Static sections are not built. conduit-31jg.65.
func (pb *PromptBuilder) BuildDynamic(ctx context.Context, session *sessions.Session, isOAuth bool, sectionNames []string) string {
	if len(sectionNames) == 0 {
		return ""
	}
	want := make(map[string]bool, len(sectionNames))
	for _, n := range sectionNames {
		want[n] = true
	}
	localParams := *pb.sectionParams
	localParams.Session = session
	localParams.IsMinimal = false
	isCron := session != nil && strings.HasPrefix(session.Key, CronSessionKeyPrefix)
	var dyn []promptSection
	for _, sec := range pb.buildSectionListWithParams(ctx, session, isOAuth, isCron, &localParams) {
		if sec.dynamic && want[sec.name] {
			dyn = append(dyn, sec)
		}
	}
	return joinSectionsWithCache(dyn, nil)
}

// SetClock overrides the time source used by time-dependent sections (tests).
// conduit-31jg.14.
func (pb *PromptBuilder) SetClock(now func() time.Time) {
	pb.sectionParams.Now = now
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
	return joinPromptParts(pb.buildPromptPartsWithParams(ctx, session, isOAuth, params))
}

// buildPromptPartsWithParams builds the prompt split into its byte-stable
// static part and its per-turn dynamic part (conduit-31jg.14).
func (pb *PromptBuilder) buildPromptPartsWithParams(ctx context.Context, session *sessions.Session, isOAuth bool, params *SectionParams) (string, string) {
	p := pb.buildSplitWithParams(ctx, session, isOAuth, params)
	return p.static, p.dynamic
}

// buildSplitWithParams builds the prompt split and records which dynamic
// sections were included (conduit-31jg.65).
func (pb *PromptBuilder) buildSplitWithParams(ctx context.Context, session *sessions.Session, isOAuth bool, params *SectionParams) promptSplit {
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
		return newPromptSplit(allSections, nil)
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

	return newPromptSplit(allSections, included, dropped...)
}

func newPromptSplit(sections []promptSection, included []bool, dropped ...string) promptSplit {
	static, dynamic := joinSectionPartsWithCache(sections, included, dropped...)
	p := promptSplit{static: static, dynamic: dynamic}
	for i := range sections {
		if sections[i].dynamic && (included == nil || included[i]) {
			p.dynamicSections = append(p.dynamicSections, sections[i].name)
		}
	}
	return p
}
