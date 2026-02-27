package briefing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Message represents a session message for briefing analysis.
// This mirrors sessions.Message but avoids importing that package directly,
// so the briefing package stays self-contained.
type Message struct {
	ID        string            `json:"id"`
	Role      string            `json:"role"`
	Content   string            `json:"content"`
	Timestamp time.Time         `json:"timestamp"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Briefing is a structured summary of a session's activity.
type Briefing struct {
	ID            string        `json:"id"`
	SessionID     string        `json:"session_id"`
	Timestamp     time.Time     `json:"timestamp"`
	Summary       string        `json:"summary"`
	KeyDecisions  []string      `json:"key_decisions,omitempty"`
	FilesChanged  []string      `json:"files_changed,omitempty"`
	ToolsUsed     []ToolUsage   `json:"tools_used,omitempty"`
	OpenQuestions []string      `json:"open_questions,omitempty"`
	NextSteps     []string      `json:"next_steps,omitempty"`
	Duration      time.Duration `json:"duration"`
	MessageCount  int           `json:"message_count"`
}

// ToolUsage tracks how many times a tool was invoked in a session.
type ToolUsage struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// BriefingSummary is a lightweight representation for list views.
type BriefingSummary struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	Timestamp time.Time     `json:"timestamp"`
	Summary   string        `json:"summary"`
	Duration  time.Duration `json:"duration"`
}

// BriefingGenerator creates briefings from session data.
type BriefingGenerator struct{}

// NewGenerator creates a new BriefingGenerator.
func NewGenerator() *BriefingGenerator {
	return &BriefingGenerator{}
}

// Generate creates a briefing from a session ID and its messages.
func (g *BriefingGenerator) Generate(sessionID string, messages []Message) (*Briefing, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to generate briefing from")
	}

	b := &Briefing{
		ID:           generateBriefingID(sessionID),
		SessionID:    sessionID,
		Timestamp:    time.Now(),
		MessageCount: len(messages),
	}

	// Calculate session duration from first to last message.
	if len(messages) >= 2 {
		b.Duration = messages[len(messages)-1].Timestamp.Sub(messages[0].Timestamp)
	}

	b.Summary = g.buildSummary(messages)
	b.KeyDecisions = g.extractDecisions(messages)
	b.FilesChanged = g.extractFilesChanged(messages)
	b.ToolsUsed = g.extractToolUsage(messages)
	b.OpenQuestions = g.extractOpenQuestions(messages)
	b.NextSteps = g.extractNextSteps(messages)

	return b, nil
}

// GenerateFromMessages is an alias that generates a briefing without a specific session ID.
func (g *BriefingGenerator) GenerateFromMessages(messages []Message) (*Briefing, error) {
	return g.Generate("unknown", messages)
}

// Save persists a briefing to the given directory as a JSON file.
func Save(briefing *Briefing, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create briefing directory %s: %w", dir, err)
	}

	filename := fmt.Sprintf("%s.json", briefing.ID)
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(briefing, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal briefing: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("failed to write briefing file %s: %w", path, err)
	}

	return nil
}

// Load reads a briefing from a JSON file.
func Load(path string) (*Briefing, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read briefing file %s: %w", path, err)
	}

	var b Briefing
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("failed to parse briefing file %s: %w", path, err)
	}

	return &b, nil
}

// ListBriefings returns summaries of all briefings in a directory,
// sorted by timestamp descending (most recent first).
func ListBriefings(dir string) ([]BriefingSummary, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read briefing directory %s: %w", dir, err)
	}

	var summaries []BriefingSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		b, err := Load(path)
		if err != nil {
			// Skip files that cannot be parsed.
			continue
		}

		summary := b.Summary
		if len(summary) > 120 {
			summary = summary[:117] + "..."
		}

		summaries = append(summaries, BriefingSummary{
			ID:        b.ID,
			SessionID: b.SessionID,
			Timestamp: b.Timestamp,
			Summary:   summary,
			Duration:  b.Duration,
		})
	}

	// Sort by timestamp descending.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Timestamp.After(summaries[j].Timestamp)
	})

	return summaries, nil
}

// --- internal helpers ---

func generateBriefingID(sessionID string) string {
	ts := time.Now().Format("20060102-150405")
	// Use a short prefix from session ID for traceability.
	prefix := sessionID
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	return fmt.Sprintf("briefing-%s-%s", prefix, ts)
}

// buildSummary creates a high-level summary from the conversation.
func (g *BriefingGenerator) buildSummary(messages []Message) string {
	// Collect the first user message as the opening topic, and the last
	// assistant message as the closing state.
	var firstUserMsg, lastAssistantMsg string
	for _, m := range messages {
		if m.Role == "user" && firstUserMsg == "" {
			firstUserMsg = truncate(m.Content, 200)
		}
		if m.Role == "assistant" {
			lastAssistantMsg = truncate(m.Content, 200)
		}
	}

	userCount := 0
	assistantCount := 0
	for _, m := range messages {
		switch m.Role {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		}
	}

	parts := []string{
		fmt.Sprintf("Session with %d messages (%d user, %d assistant).", len(messages), userCount, assistantCount),
	}

	if firstUserMsg != "" {
		parts = append(parts, fmt.Sprintf("Started with: %s", firstUserMsg))
	}
	if lastAssistantMsg != "" {
		parts = append(parts, fmt.Sprintf("Ended with: %s", lastAssistantMsg))
	}

	return strings.Join(parts, " ")
}

// extractDecisions looks for patterns indicating decisions were made.
func (g *BriefingGenerator) extractDecisions(messages []Message) []string {
	decisionKeywords := []string{
		"decided to", "decision:", "we'll go with", "let's use",
		"agreed to", "choosing", "selected", "going with",
	}

	var decisions []string
	seen := make(map[string]bool)

	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		contentLower := strings.ToLower(m.Content)
		for _, keyword := range decisionKeywords {
			if strings.Contains(contentLower, keyword) {
				// Extract the sentence containing the keyword.
				sentence := extractSentence(m.Content, keyword)
				if sentence != "" && !seen[sentence] {
					seen[sentence] = true
					decisions = append(decisions, truncate(sentence, 200))
				}
			}
		}
	}

	return decisions
}

// extractFilesChanged looks for file path patterns in tool usage.
func (g *BriefingGenerator) extractFilesChanged(messages []Message) []string {
	filePatterns := []string{
		"wrote to ", "created ", "edited ", "modified ",
		"writing to ", "saving to ", "updating ",
	}

	seen := make(map[string]bool)
	var files []string

	for _, m := range messages {
		contentLower := strings.ToLower(m.Content)
		for _, pattern := range filePatterns {
			idx := strings.Index(contentLower, pattern)
			if idx == -1 {
				continue
			}
			// Extract potential file path after the pattern.
			rest := m.Content[idx+len(pattern):]
			path := extractFilePath(rest)
			if path != "" && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}

	return files
}

// extractToolUsage counts tool invocations mentioned in messages.
func (g *BriefingGenerator) extractToolUsage(messages []Message) []ToolUsage {
	toolCounts := make(map[string]int)

	for _, m := range messages {
		if m.Metadata == nil {
			continue
		}
		if toolName, ok := m.Metadata["tool_name"]; ok && toolName != "" {
			toolCounts[toolName]++
		}
	}

	// Also detect tool usage from content patterns like "Using tool: X" or tool_use blocks.
	toolKeywords := []string{
		"Read", "Write", "Edit", "Bash", "Glob",
		"WebSearch", "WebFetch", "Message", "Cron", "Image",
		"MemorySearch", "Gateway", "Tts",
	}

	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tool := range toolKeywords {
			if strings.Contains(m.Content, "tool_use") && strings.Contains(m.Content, tool) {
				toolCounts[tool]++
			}
		}
	}

	var usage []ToolUsage
	for name, count := range toolCounts {
		usage = append(usage, ToolUsage{Name: name, Count: count})
	}

	sort.Slice(usage, func(i, j int) bool {
		return usage[i].Count > usage[j].Count
	})

	return usage
}

// extractOpenQuestions finds unresolved questions in the conversation.
func (g *BriefingGenerator) extractOpenQuestions(messages []Message) []string {
	if len(messages) == 0 {
		return nil
	}

	var questions []string
	seen := make(map[string]bool)

	// Look at the last few messages for questions.
	start := len(messages) - 10
	if start < 0 {
		start = 0
	}

	for _, m := range messages[start:] {
		sentences := splitSentences(m.Content)
		for _, s := range sentences {
			s = strings.TrimSpace(s)
			if strings.HasSuffix(s, "?") && !seen[s] {
				seen[s] = true
				questions = append(questions, truncate(s, 200))
			}
		}
	}

	return questions
}

// extractNextSteps looks for action items and future work.
func (g *BriefingGenerator) extractNextSteps(messages []Message) []string {
	nextKeywords := []string{
		"next step", "todo", "to do", "should also",
		"remaining", "still need", "follow up", "follow-up",
		"need to", "will need", "later we",
	}

	var steps []string
	seen := make(map[string]bool)

	// Focus on later messages which are more likely to contain next steps.
	start := len(messages) / 2
	if start < 0 {
		start = 0
	}

	for _, m := range messages[start:] {
		if m.Role != "assistant" {
			continue
		}
		contentLower := strings.ToLower(m.Content)
		for _, keyword := range nextKeywords {
			if strings.Contains(contentLower, keyword) {
				sentence := extractSentence(m.Content, keyword)
				if sentence != "" && !seen[sentence] {
					seen[sentence] = true
					steps = append(steps, truncate(sentence, 200))
				}
			}
		}
	}

	return steps
}

// --- string utilities ---

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// extractSentence returns the sentence containing the keyword.
func extractSentence(content, keyword string) string {
	lower := strings.ToLower(content)
	idx := strings.Index(lower, keyword)
	if idx == -1 {
		return ""
	}

	// Walk backwards to find sentence start.
	sentStart := idx
	for sentStart > 0 && content[sentStart-1] != '.' && content[sentStart-1] != '\n' {
		sentStart--
	}

	// Walk forward to find sentence end.
	sentEnd := idx + len(keyword)
	for sentEnd < len(content) && content[sentEnd] != '.' && content[sentEnd] != '\n' {
		sentEnd++
	}
	if sentEnd < len(content) && content[sentEnd] == '.' {
		sentEnd++ // include the period
	}

	return strings.TrimSpace(content[sentStart:sentEnd])
}

// extractFilePath attempts to pull a filesystem path from text.
func extractFilePath(text string) string {
	// Find the first word that looks like a path.
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}

	candidate := strings.Trim(fields[0], "\"'`,;:")
	// A path should contain a slash or a dot extension.
	if strings.Contains(candidate, "/") || strings.Contains(candidate, ".") {
		if len(candidate) > 2 {
			return candidate
		}
	}

	return ""
}

// splitSentences does a simple sentence split on periods and newlines.
func splitSentences(text string) []string {
	// Replace newlines with periods for uniform splitting.
	text = strings.ReplaceAll(text, "\n", ". ")
	parts := strings.Split(text, ".")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
