package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

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

	promptText := joinPromptParts(joinSectionPartsWithCache(allSections, included, droppedNames...))
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
	wakeSource := types.WakeSource(ctx)

	raw := []promptSection{
		// P1 — Critical: identity and runtime facts (never dropped)
		{name: "Identity", priority: 1, build: func() string { return pb.buildIdentitySection(isOAuth) }},
		{name: "Runtime", priority: 1, build: func() string {
			return buildRuntimeSection(params, pb.buildRuntimeInfo(session))
		}},
		{name: "Wake Context", priority: 1, dynamic: true, build: func() string { return buildWakeContextSection(wakeSource) }},

		// P2 — Grounding data: project context, memory, tool availability (reference)
		{name: "Project Context", priority: 2, build: func() string { return pb.buildWorkspaceContextSection(ctx, session) }},
		{name: "Memory Recall", priority: 2, build: func() string { return buildMemorySection(params) }},
		{name: "Memory Persistence", priority: 2, build: func() string { return buildMemoryPersistenceSection(params) }},
		{name: "Brain", priority: 2, build: func() string { return buildBrainSection(params) }},
		{name: "Situation Awareness", priority: 2, dynamic: true, build: func() string {
			return pb.buildSituationAwareness(ctx, params)
		}},
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
		{name: "Gateway Actions", priority: 4, build: func() string { return buildSelfUpdateSection(params) }},
		// P4 (LAST) — Time Context. Sections flagged dynamic (this one, Wake
		// Context, Situation Awareness) render in the trailing dynamic system
		// block, after the cache breakpoint (conduit-31jg.14). Every other
		// section must stay byte-stable between requests.
		{name: "Time Context", priority: 4, dynamic: true, build: func() string {
			return buildTimeContextSection(params)
		}},
	}

	// Stable sort by priority (preserves order within same priority).
	sort.SliceStable(raw, func(i, j int) bool {
		return raw[i].priority < raw[j].priority
	})

	return raw
}

// joinPromptParts joins the static and dynamic prompt parts the way a
// single-string system prompt carries them (conduit-31jg.14).
func joinPromptParts(static, dynamic string) string {
	switch {
	case dynamic == "":
		return static
	case static == "":
		return dynamic
	}
	return static + "\n\n" + dynamic
}

// joinSectionPartsWithCache assembles the static part (every non-dynamic
// section, then the compact-mode notice) and the dynamic part (sections
// flagged dynamic, in declaration order). conduit-31jg.14: dynamic sections
// change every turn, so they must sit after the cached prefix.
func joinSectionPartsWithCache(sections []promptSection, included []bool, dropped ...string) (string, string) {
	var static, dynamic []promptSection
	var staticIncl, dynamicIncl []bool
	for i := range sections {
		inc := included == nil || included[i]
		if sections[i].dynamic {
			dynamic = append(dynamic, sections[i])
			dynamicIncl = append(dynamicIncl, inc)
		} else {
			static = append(static, sections[i])
			staticIncl = append(staticIncl, inc)
		}
	}
	return joinSectionsWithCache(static, staticIncl, dropped...), joinSectionsWithCache(dynamic, dynamicIncl)
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
