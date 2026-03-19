package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
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
		config.AgentEmail{}, // no email configured
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{SkillsIntegration: false},
		tools,
		nil, // no workspace context
		nil, // no summary manager
		nil, // no skills manager
		nil, // default model aliases
		nil, // default prompt scaling
		"",  // default timezone
		"",  // default runtime channel
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
	for _, want := range []string{"## Tooling", "## Tool Integrity", "## Silent Replies", "## Runtime", "## Messaging", "## Safety", "## Operating Principles", "## Error Handling"} {
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
	for _, want := range []string{"## Tooling", "## Tool Integrity", "## Silent Replies", "## Runtime"} {
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

	if len(sections) < 22 {
		t.Errorf("expected at least 22 sections, got %d", len(sections))
	}

	// Count by priority.
	counts := map[int]int{}
	for _, sec := range sections {
		counts[sec.priority]++
	}

	if counts[1] != 6 {
		t.Errorf("expected 6 P1 sections, got %d", counts[1])
	}
	if counts[2] < 8 {
		t.Errorf("expected at least 8 P2 sections, got %d", counts[2])
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

// --- System Prompt Caching Tests ---

func newTestAgent() *ConduitAgentWithIntegration {
	tools := []ai.Tool{
		{Name: "Read", Description: "Read a file"},
		{Name: "Write", Description: "Write a file"},
	}
	cfg := AgentConfig{
		Name:        "test-agent",
		Personality: "helpful",
		Identity:    IdentityConfig{APIKeyIdentity: "You are a test agent."},
		Capabilities: AgentCapabilities{
			SkillsIntegration: false,
		},
	}
	return NewConduitAgentWithIntegration(cfg, tools, nil, nil, nil, nil)
}

func TestBuildSystemPrompt_CachesResult(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()
	session := sessionWithModel("claude-sonnet-4-20250514")

	// First call should build and cache
	blocks1, err := agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("first BuildSystemPrompt failed: %v", err)
	}
	if len(blocks1) == 0 {
		t.Fatal("expected non-empty blocks")
	}

	// Second call should return cached result
	blocks2, err := agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("second BuildSystemPrompt failed: %v", err)
	}

	// Both should have the same content
	if len(blocks1) != len(blocks2) {
		t.Errorf("cached result has different length: %d vs %d", len(blocks1), len(blocks2))
	}
	if blocks1[0].Text != blocks2[0].Text {
		t.Error("cached result has different content")
	}
}

func TestBuildSystemPrompt_DifferentModelsGetDifferentCache(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()

	// Session with large context model
	sessionLarge := &sessions.Session{
		Key:       "test-session",
		ChannelID: "telegram-123",
		UserID:    "user1",
		Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
	}

	// Session with small context model (will have different prompt due to budget)
	sessionSmall := &sessions.Session{
		Key:       "test-session",
		ChannelID: "telegram-123",
		UserID:    "user1",
		Context:   map[string]string{"model": "gemma2"},
	}

	blocksLarge, err := agent.BuildSystemPrompt(ctx, sessionLarge)
	if err != nil {
		t.Fatalf("BuildSystemPrompt for large model failed: %v", err)
	}

	blocksSmall, err := agent.BuildSystemPrompt(ctx, sessionSmall)
	if err != nil {
		t.Fatalf("BuildSystemPrompt for small model failed: %v", err)
	}

	// Prompts should be different (gemma2 triggers compact mode)
	if blocksLarge[0].Text == blocksSmall[0].Text {
		t.Error("expected different prompts for different model context sizes")
	}
}

func TestBuildSystemPrompt_DifferentSessionsGetDifferentCache(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()

	session1 := &sessions.Session{
		Key:       "session-1",
		ChannelID: "telegram-123",
		UserID:    "user1",
		Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
	}

	session2 := &sessions.Session{
		Key:       "session-2",
		ChannelID: "telegram-456",
		UserID:    "user2",
		Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
	}

	// Build prompts for both sessions
	_, err := agent.BuildSystemPrompt(ctx, session1)
	if err != nil {
		t.Fatalf("BuildSystemPrompt for session1 failed: %v", err)
	}

	_, err = agent.BuildSystemPrompt(ctx, session2)
	if err != nil {
		t.Fatalf("BuildSystemPrompt for session2 failed: %v", err)
	}

	// Invalidate only session1's cache
	agent.InvalidatePromptCacheForSession("session-1")

	// Verify session2's cache is still valid by checking the cache directly
	cacheKey2 := agent.buildPromptCacheKey(session2, true) // OAuth default
	if _, ok := agent.promptCache.Load(cacheKey2); !ok {
		t.Error("session2's cache should still exist after invalidating session1")
	}
}

func TestInvalidatePromptCache_ClearsAllEntries(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()

	// Build prompts for multiple sessions
	sessions := []*sessions.Session{
		{Key: "s1", Context: map[string]string{"model": "m1"}},
		{Key: "s2", Context: map[string]string{"model": "m2"}},
		{Key: "s3", Context: map[string]string{"model": "m3"}},
	}

	for _, s := range sessions {
		_, err := agent.BuildSystemPrompt(ctx, s)
		if err != nil {
			t.Fatalf("BuildSystemPrompt failed: %v", err)
		}
	}

	// Verify cache has entries
	count := 0
	agent.promptCache.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	if count == 0 {
		t.Fatal("cache should have entries before invalidation")
	}

	// Invalidate all
	agent.InvalidatePromptCache()

	// Verify cache is empty
	count = 0
	agent.promptCache.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	if count != 0 {
		t.Errorf("cache should be empty after InvalidatePromptCache, got %d entries", count)
	}
}

func TestSetTools_InvalidatesCache(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()
	session := sessionWithModel("claude-sonnet-4-20250514")

	// Build initial prompt
	_, err := agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("BuildSystemPrompt failed: %v", err)
	}

	// Verify cache has entry
	cacheKey := agent.buildPromptCacheKey(session, true)
	if _, ok := agent.promptCache.Load(cacheKey); !ok {
		t.Fatal("cache should have entry after BuildSystemPrompt")
	}

	// Update tools (should invalidate cache)
	newTools := []ai.Tool{
		{Name: "NewTool", Description: "A new tool"},
	}
	agent.SetTools(newTools)

	// Verify cache is cleared
	if _, ok := agent.promptCache.Load(cacheKey); ok {
		t.Error("cache should be cleared after SetTools")
	}
}

func TestUpdateConfiguration_InvalidatesCache(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()
	session := sessionWithModel("claude-sonnet-4-20250514")

	// Build initial prompt
	_, err := agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("BuildSystemPrompt failed: %v", err)
	}

	// Verify cache has entry
	cacheKey := agent.buildPromptCacheKey(session, true)
	if _, ok := agent.promptCache.Load(cacheKey); !ok {
		t.Fatal("cache should have entry after BuildSystemPrompt")
	}

	// Update configuration (should invalidate cache)
	newCfg := AgentConfig{
		Name:        "updated-agent",
		Personality: "very helpful",
		Identity:    IdentityConfig{APIKeyIdentity: "You are an updated agent."},
	}
	agent.UpdateConfiguration(newCfg)

	// Verify cache is cleared
	if _, ok := agent.promptCache.Load(cacheKey); ok {
		t.Error("cache should be cleared after UpdateConfiguration")
	}
}

func TestBuildPromptCacheKey_IncludesAllFactors(t *testing.T) {
	agent := newTestAgent()

	session := &sessions.Session{
		Key:     "my-session",
		Context: map[string]string{"model": "claude-sonnet-4"},
	}

	// Test OAuth key
	keyOAuth := agent.buildPromptCacheKey(session, true)
	if keyOAuth != "my-session:claude-sonnet-4:oauth" {
		t.Errorf("unexpected OAuth cache key: %s", keyOAuth)
	}

	// Test API key
	keyAPI := agent.buildPromptCacheKey(session, false)
	if keyAPI != "my-session:claude-sonnet-4:api" {
		t.Errorf("unexpected API cache key: %s", keyAPI)
	}

	// Test nil session
	keyNil := agent.buildPromptCacheKey(nil, true)
	if keyNil != "::oauth" {
		t.Errorf("unexpected nil session cache key: %s", keyNil)
	}
}

func TestCopySystemBlocks_CreatesIndependentCopy(t *testing.T) {
	original := []ai.SystemBlock{
		{Type: "text", Text: "Hello"},
		{Type: "text", Text: "World"},
	}

	copied := copySystemBlocks(original)

	// Modify original
	original[0].Text = "Modified"

	// Verify copy is unchanged
	if copied[0].Text != "Hello" {
		t.Error("copy should be independent of original")
	}
}

func TestCopySystemBlocks_HandlesNil(t *testing.T) {
	copied := copySystemBlocks(nil)
	if copied != nil {
		t.Error("copySystemBlocks(nil) should return nil")
	}
}

func TestBuildSystemPrompt_TTLExpiration(t *testing.T) {
	agent := newTestAgent()
	// Set a very short TTL for testing
	agent.SetPromptCacheTTL(50 * time.Millisecond)

	ctx := context.Background()
	session := sessionWithModel("claude-sonnet-4-20250514")

	// Build initial prompt
	_, err := agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("first BuildSystemPrompt failed: %v", err)
	}

	// Verify cache has entry
	cacheKey := agent.buildPromptCacheKey(session, true)
	if _, ok := agent.promptCache.Load(cacheKey); !ok {
		t.Fatal("cache should have entry immediately after BuildSystemPrompt")
	}

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Build again - should rebuild due to TTL expiration
	_, err = agent.BuildSystemPrompt(ctx, session)
	if err != nil {
		t.Fatalf("second BuildSystemPrompt failed: %v", err)
	}

	// The entry should now have a new expiration time
	// We can't easily test this without exposing internals, but the test
	// verifies the code path works without error
}

func TestSetPromptCacheTTL(t *testing.T) {
	agent := newTestAgent()

	// Default TTL
	if agent.promptCacheTTL != DefaultPromptCacheTTL {
		t.Errorf("expected default TTL %v, got %v", DefaultPromptCacheTTL, agent.promptCacheTTL)
	}

	// Set custom TTL
	customTTL := 10 * time.Minute
	agent.SetPromptCacheTTL(customTTL)

	if agent.promptCacheTTL != customTTL {
		t.Errorf("expected custom TTL %v, got %v", customTTL, agent.promptCacheTTL)
	}
}

// --- Email Section Tests ---

func TestBuildEmailSection_Configured(t *testing.T) {
	pb := NewPromptBuilder(
		"Conduit", "helpful assistant",
		config.AgentEmail{
			Address:     "agent@example.com",
			Aliases:     []string{"assistant@example.com", "bot@example.com"},
			DisplayName: "Conduit Agent",
		},
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{},
		nil, nil, nil, nil, nil, nil, "", "",
	)

	section := pb.buildEmailSection()

	// Check address is present
	if !strings.Contains(section, "agent@example.com") {
		t.Error("email section should contain the email address")
	}

	// Check display name is present
	if !strings.Contains(section, "Conduit Agent") {
		t.Error("email section should contain the display name")
	}

	// Check aliases are present
	if !strings.Contains(section, "assistant@example.com") {
		t.Error("email section should contain aliases")
	}
	if !strings.Contains(section, "bot@example.com") {
		t.Error("email section should contain all aliases")
	}

	// Check header
	if !strings.Contains(section, "## Email") {
		t.Error("email section should have ## Email header")
	}
}

func TestBuildEmailSection_Empty(t *testing.T) {
	pb := NewPromptBuilder(
		"conduit", "helpful assistant",
		config.AgentEmail{}, // empty email config
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{},
		nil, nil, nil, nil, nil, nil, "", "",
	)

	section := pb.buildEmailSection()

	if section != "" {
		t.Errorf("email section should be empty when no address configured, got: %s", section)
	}
}

func TestBuildEmailSection_DisplayNameDefault(t *testing.T) {
	pb := NewPromptBuilder(
		"Conduit", "helpful assistant",
		config.AgentEmail{
			Address: "agent@example.com",
			// DisplayName is empty, should fall back to agent name
		},
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{},
		nil, nil, nil, nil, nil, nil, "", "",
	)

	section := pb.buildEmailSection()

	// Should use agent name as display name
	if !strings.Contains(section, "Display name: Conduit") {
		t.Error("email section should use agent name as default display name")
	}
}

func TestBuildEmailSection_NoAliases(t *testing.T) {
	pb := NewPromptBuilder(
		"Conduit", "helpful assistant",
		config.AgentEmail{
			Address:     "agent@example.com",
			DisplayName: "Conduit",
			// No aliases
		},
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{},
		nil, nil, nil, nil, nil, nil, "", "",
	)

	section := pb.buildEmailSection()

	// Should not contain "Aliases:" line
	if strings.Contains(section, "Aliases:") {
		t.Error("email section should not contain Aliases line when no aliases configured")
	}
}

func TestPromptBuilder_ConcurrentBuild(t *testing.T) {
	pb := newTestPromptBuilder()

	var wg sync.WaitGroup
	// Concurrently build prompts with different sessions.
	// This exercises the fix that Build() creates a local copy of sectionParams
	// instead of mutating the shared one.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			session := &sessions.Session{
				Key:       "test-session",
				ChannelID: "telegram-123",
				UserID:    "user1",
				Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
			}
			blocks, err := pb.Build(context.Background(), session, false)
			if err != nil {
				t.Errorf("Build() failed in goroutine %d: %v", id, err)
				return
			}
			if len(blocks) == 0 {
				t.Errorf("Build() returned empty blocks in goroutine %d", id)
			}
		}(i)
	}

	wg.Wait()
}

func TestConduitAgent_ConcurrentSetAndBuild(t *testing.T) {
	agent := newTestAgent()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Concurrently call SetTools
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tools := []ai.Tool{
				{Name: "TestTool", Description: "A test tool"},
			}
			agent.SetTools(tools)
		}(i)
	}

	// Concurrently call BuildSystemPrompt
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			session := &sessions.Session{
				Key:       "test-session",
				ChannelID: "telegram-123",
				UserID:    "user1",
				Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
			}
			_, _ = agent.BuildSystemPrompt(ctx, session)
		}(i)
	}

	// Concurrently call GetToolDefinitions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = agent.GetToolDefinitions(nil)
		}()
	}

	wg.Wait()

	// Verify agent is in a consistent state
	tools := agent.GetToolDefinitions(nil)
	if tools == nil {
		t.Error("GetToolDefinitions should not return nil")
	}
}
