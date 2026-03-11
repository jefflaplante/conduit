package agent

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

func newTestPromptBuilder() *PromptBuilder {
	tools := []ai.Tool{
		{Name: "Read", Description: "Read a file"},
		{Name: "Write", Description: "Write a file"},
		{Name: "Bash", Description: "Run a command"},
		{Name: "MemorySearch", Description: "Search memory"},
		{Name: "Message", Description: "Send a message"},
		{Name: "Gateway", Description: "Gateway control"},
	}
	return NewPromptBuilder(
		"conduit", "helpful assistant",
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{SkillsIntegration: false},
		tools,
		nil, // no workspace context
		nil, // no skills manager
		nil, // default model aliases
		nil, // default prompt scaling
	)
}

func sessionWithModel(model string) *sessions.Session {
	return &sessions.Session{
		Key:       "test-session",
		ChannelID: "telegram-123",
		UserID:    "user1",
		Context:   map[string]string{"model": model},
	}
}

func TestBuildFullPrompt_LargeContext(t *testing.T) {
	pb := newTestPromptBuilder()
	pb.sectionParams.Session = sessionWithModel("claude-sonnet-4-20250514")
	pb.sectionParams.IsMinimal = false

	prompt := pb.buildFullPrompt(context.Background(), sessionWithModel("claude-sonnet-4-20250514"), false)

	// All sections should be present for a 200K model.
	if strings.Contains(prompt, "Compact mode") {
		t.Error("large-context model should not trigger compact mode")
	}
	// Spot-check that P1 through P4 sections are present.
	for _, want := range []string{"## Tooling", "## Tool Call Style", "## Silent Replies", "## Runtime", "## Messaging", "## Safety"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("expected section %q in full prompt", want)
		}
	}
}

func TestBuildFullPrompt_SmallContext(t *testing.T) {
	pb := newTestPromptBuilder()
	session := sessionWithModel("mistral")

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	// P1 sections must always be present regardless of budget.
	for _, want := range []string{"## Tooling", "## Tool Call Style", "## Silent Replies", "## Runtime"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("P1 section %q should be present for mistral (32K)", want)
		}
	}

	// Budget is applied (not short-circuited) — verify prompt is reasonably sized.
	// Mistral budget: 32768 * 15 / 100 * 4 = 19660 chars.
	budgetChars := 32768 * defaultPromptBudgetPercent / 100 * defaultCharsPerToken
	if len(prompt) > budgetChars+200 { // small tolerance for compact-mode notice
		t.Errorf("prompt (%d chars) exceeds budget (%d chars) for mistral", len(prompt), budgetChars)
	}
}

func TestBuildFullPrompt_SmallContext_DropsLargeSections(t *testing.T) {
	// Simulate a model with a very tight budget where sections must be dropped.
	// Use gemma2 (8K context) → budget = 8192 * 15 / 100 * 4 = 4915 chars.
	// With minimal tools, most sections are small. Verify budget enforcement.
	pb := newTestPromptBuilder()
	session := sessionWithModel("gemma2")

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	budgetChars := 8192 * defaultPromptBudgetPercent / 100 * defaultCharsPerToken
	// Prompt should be near or under budget (compact notice adds a bit).
	if len(prompt) > budgetChars+300 {
		t.Errorf("prompt (%d chars) far exceeds budget (%d chars) for gemma2", len(prompt), budgetChars)
	}
}

func TestBuildFullPrompt_TinyContext(t *testing.T) {
	pb := newTestPromptBuilder()
	session := sessionWithModel("gemma2") // 8K context

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	// Should have compact mode with many dropped sections.
	if !strings.Contains(prompt, "Compact mode") {
		t.Error("gemma2 (8K) should trigger compact mode notice")
	}

	// Count dropped sections from the notice.
	idx := strings.Index(prompt, "[Compact mode: omitted ")
	if idx == -1 {
		t.Fatal("compact mode notice not found")
	}
	notice := prompt[idx:]
	droppedCount := strings.Count(notice, ", ") + 1 // comma-separated list
	if droppedCount < 3 {
		t.Errorf("expected at least 3 dropped sections for 8K context, got %d", droppedCount)
	}
}

func TestBuildFullPrompt_UnknownModel(t *testing.T) {
	pb := newTestPromptBuilder()
	session := sessionWithModel("some-unknown-model-xyz")

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	// Unknown models default to 200K, so no compact mode.
	if strings.Contains(prompt, "Compact mode") {
		t.Error("unknown model should default to 200K and not trigger compact mode")
	}
}

func TestBuildFullPrompt_EmptyModel(t *testing.T) {
	pb := newTestPromptBuilder()
	session := sessionWithModel("")

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	// Empty model defaults to 200K, so no compact mode.
	if strings.Contains(prompt, "Compact mode") {
		t.Error("empty model should default to 200K and not trigger compact mode")
	}
}

func TestPromptSectionPriorities(t *testing.T) {
	pb := newTestPromptBuilder()
	session := sessionWithModel("claude-sonnet-4-20250514")

	sections := pb.buildSectionList(context.Background(), session, false, false)

	if len(sections) < 20 {
		t.Errorf("expected at least 20 sections, got %d", len(sections))
	}

	// Count by priority.
	counts := map[int]int{}
	for _, sec := range sections {
		counts[sec.priority]++
	}

	if counts[1] != 5 {
		t.Errorf("expected 5 P1 sections, got %d", counts[1])
	}
	if counts[2] < 5 {
		t.Errorf("expected at least 5 P2 sections, got %d", counts[2])
	}
	if counts[3] < 4 {
		t.Errorf("expected at least 4 P3 sections, got %d", counts[3])
	}
	if counts[4] < 3 {
		t.Errorf("expected at least 3 P4 sections, got %d", counts[4])
	}

	// Verify sorted by priority.
	for i := 1; i < len(sections); i++ {
		if sections[i].priority < sections[i-1].priority {
			t.Errorf("sections not sorted by priority: %s (P%d) before %s (P%d)",
				sections[i-1].name, sections[i-1].priority,
				sections[i].name, sections[i].priority)
		}
	}
}
