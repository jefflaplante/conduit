package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/sessions"
	"conduit/internal/workspace"
)

// buildWorkspaceSection creates workspace directory info
func (pb *PromptBuilder) buildWorkspaceSection() string {
	return fmt.Sprintf(`## Workspace
Your working directory is: %s
Treat this directory as the single global workspace for file operations unless explicitly instructed otherwise.
Key locations: MEMORY.md (long-term memory), memory/ (daily logs), HEARTBEAT.md (monitoring tasks), SOUL.md (personality).`, pb.sectionParams.WorkspaceDir)
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
		if filterStr := session.Context[sessions.ContextSkillFilter]; filterStr != "" {
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

	securityCtx := workspaceSecurityContext(session)
	sessionType := securityCtx.SessionType
	sessionKey := securityCtx.SessionID

	bundle, err := pb.workspaceContext.LoadContext(ctx, securityCtx)
	if err != nil || len(bundle.Files) == 0 {
		return ""
	}

	// The state file renders in its own Session State section (bd-3ia);
	// keep it out of Project Context and away from the summarizer.
	files := bundle.Files
	if stateFile := pb.workspaceContext.StateFile(); stateFile != "" {
		if _, ok := files[stateFile]; ok {
			files = make(map[string]string, len(bundle.Files))
			for name, content := range bundle.Files {
				if name != stateFile {
					files[name] = content
				}
			}
		}
	}

	// Check if we should use summarized content for small-context models
	if pb.shouldUseSummaries(session) && pb.summaryManager != nil {
		summarized, err := pb.summaryManager.GetSummarizedContext(ctx, files)
		if err == nil {
			files = summarized
		}
		// On error, fall through to use full content
	}

	var builder strings.Builder
	builder.WriteString("# Project Context\n\n")
	builder.WriteString("The following project context files have been loaded:\n")
	builder.WriteString("If SOUL.md is present, embody its persona and tone. Avoid stiff, generic replies; follow its guidance unless higher-priority instructions override it.\n\n")

	// Core files in configured order (workspace.files.core). MEMORY.md and
	// daily memory files render last, below. HEARTBEAT.md is
	// heartbeat/cron-session guidance: only inject for those session types.
	// Regular chat sessions never need it.
	var coreFiles []string
	for _, filename := range pb.workspaceContext.CoreFiles() {
		if filename != "MEMORY.md" && filename != "HEARTBEAT.md" && !strings.HasPrefix(filename, "memory/") {
			coreFiles = append(coreFiles, filename)
		}
	}
	isHeartbeatSession := strings.HasPrefix(sessionKey, "heartbeat_") || strings.HasPrefix(sessionKey, "cron_")
	if isHeartbeatSession {
		coreFiles = append([]string{"HEARTBEAT.md"}, coreFiles...)
	}
	skip := contextSkipSet(session)
	for _, filename := range coreFiles {
		if skip[strings.ToUpper(filename)] {
			continue
		}
		if content, exists := files[filename]; exists {
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// Memory files — tail-truncated (recent entries are the signal; the file
	// is appended chronologically so the last N chars are the most recent).
	// conduit-31jg.14: sorted, not map order — random order across builds
	// made the "static" system block differ byte-for-byte and missed the
	// prompt cache whenever more than one daily memory file was loaded.
	memoryFiles := make([]string, 0, len(files))
	for filename := range files {
		memoryFiles = append(memoryFiles, filename)
	}
	sort.Strings(memoryFiles)
	for _, filename := range memoryFiles {
		content := files[filename]
		if strings.HasPrefix(filename, "memory/") && strings.HasSuffix(filename, ".md") {
			if len(content) > 4000 {
				cut := len(content) - 4000
				// Advance to the next newline so we don't start mid-entry
				if idx := strings.IndexByte(content[cut:], '\n'); idx >= 0 {
					cut += idx + 1
				}
				content = "…(older entries truncated)\n" + content[cut:]
			}
			builder.WriteString(fmt.Sprintf("## %s\n%s\n", filename, content))
		}
	}

	// MEMORY.md only in main sessions, and only while memory_md_inject is on
	// (bd-3ia: default true; the state file is meant to replace it).
	if sessionType == "main" && !skip["MEMORY.MD"] && pb.workspaceContext.MemoryMDInject() {
		if content, exists := files["MEMORY.md"]; exists {
			builder.WriteString(fmt.Sprintf("## MEMORY.md\n%s\n", content))
		}
	}

	return builder.String()
}

// workspaceSecurityContext derives the workspace access context for a
// session: group chats are "shared", everything else "main".
func workspaceSecurityContext(session *sessions.Session) workspace.SecurityContext {
	sc := workspace.SecurityContext{SessionType: "main"}
	if session != nil {
		sc.ChannelID = session.ChannelID
		sc.UserID = session.UserID
		sc.SessionID = session.Key
		if strings.Contains(sc.ChannelID, "group") || strings.Contains(sc.ChannelID, "-100") {
			sc.SessionType = "shared"
		}
	}
	return sc
}

// contextSkipSet returns the upper-cased context files the session's
// sessions.ContextSkip key leaves out (a topic prompt profile,
// conduit-331g). SOUL.md is never skipped: it carries identity and safety.
func contextSkipSet(session *sessions.Session) map[string]bool {
	if session == nil || session.Context[sessions.ContextSkip] == "" {
		return nil
	}
	skip := map[string]bool{}
	for _, name := range strings.Split(session.Context[sessions.ContextSkip], ",") {
		if name = strings.ToUpper(strings.TrimSpace(name)); name != "" && name != "SOUL.MD" {
			skip[name] = true
		}
	}
	return skip
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
