package ai

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// --- Helper setup ---

// setupSmartRouter creates a Router with smart routing enabled, a mock
// provider, and a DefaultModelSelector for testing.
func setupSmartRouter(t *testing.T, budget float64) (*Router, *MockProvider) {
	t.Helper()

	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	mock := NewMockProvider("mock")
	router.RegisterProvider("mock", mock)

	// Set up smart routing components
	tracker := NewUsageTracker()
	router.usageTracker = tracker

	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		TrackUsage:      true,
		CostBudgetDaily: budget,
	}
	router.SetSmartRoutingConfig(smartCfg)

	aliases := map[string]string{
		"haiku":  "claude-haiku-4-5-20251001",
		"sonnet": "claude-sonnet-4-6",
		"opus":   "claude-opus-4-6",
	}
	selector := NewDefaultModelSelector(smartCfg, aliases, tracker)
	router.SetModelSelector(selector)
	router.SetComplexityAnalyzer(NewComplexityAnalyzer())

	return router, mock
}

func newTestSession() *sessions.Session {
	return &sessions.Session{
		Key:       "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// --- Tests ---

func TestGenerateResponseSmart_DisabledFallsBack(t *testing.T) {
	// When smart routing is not enabled, GenerateResponseSmart should
	// delegate to GenerateResponseWithTools.
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	mock := NewMockProvider("mock")
	mock.AddResponse("plain response", nil)
	router.RegisterProvider("mock", mock)

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "hello", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Result should be nil when smart routing is disabled
	if result != nil {
		t.Error("Expected nil SmartRoutingResult when smart routing is disabled")
	}

	if resp.GetContent() != "plain response" {
		t.Errorf("Expected 'plain response', got '%s'", resp.GetContent())
	}
}

func TestGenerateResponseSmart_SimpleMessage(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("simple answer", nil)

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "simple answer" {
		t.Errorf("Expected 'simple answer', got '%s'", resp.GetContent())
	}

	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	// Simple message should select haiku tier
	if result.Complexity.Level != ComplexitySimple {
		t.Errorf("Expected simple complexity, got %s", result.Complexity.Level)
	}

	if result.FallbacksAttempted != 0 {
		t.Errorf("Expected 0 fallbacks, got %d", result.FallbacksAttempted)
	}

	// TotalLatencyMs can be 0 if the operation completes within a millisecond
	if result.TotalLatencyMs < 0 {
		t.Errorf("Expected non-negative latency, got %d", result.TotalLatencyMs)
	}
}

func TestGenerateResponseSmart_ComplexMessage(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("detailed analysis", nil)

	session := newTestSession()
	msg := "Please refactor the entire authentication module to use OAuth2 with PKCE flow. " +
		"Analyze the existing codebase, implement the migration plan, and update all tests. " +
		"This involves multiple files across the architecture."

	resp, result, err := router.GenerateResponseSmart(context.Background(), session, msg, "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "detailed analysis" {
		t.Errorf("Expected 'detailed analysis', got '%s'", resp.GetContent())
	}

	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	// Complex message should select opus tier
	if result.Complexity.Level != ComplexityComplex {
		t.Errorf("Expected complex complexity, got %s (score=%d)", result.Complexity.Level, result.Complexity.Score)
	}

	if result.SelectedModel != "claude-opus-4-6" {
		t.Errorf("Expected opus model for complex task, got %s", result.SelectedModel)
	}
}

func TestGenerateResponseSmart_FallbackOnRateLimit(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)

	// First call: rate limit error, second call: success (fallback model)
	mock.SetResponses([]MockResponse{
		{Error: &RateLimitError{StatusCode: 429, Message: "rate limited"}},
		{Content: "fallback response", Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "fallback response" {
		t.Errorf("Expected 'fallback response', got '%s'", resp.GetContent())
	}

	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	if result.FallbacksAttempted < 1 {
		t.Errorf("Expected at least 1 fallback attempt, got %d", result.FallbacksAttempted)
	}
}

func TestGenerateResponseSmart_FallbackOnServerError(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)

	// First call: 500, second call: success
	mock.SetResponses([]MockResponse{
		{Error: fmt.Errorf("API error: 500 - internal server error")},
		{Content: "recovered", Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "recovered" {
		t.Errorf("Expected 'recovered', got '%s'", resp.GetContent())
	}

	if result.FallbacksAttempted < 1 {
		t.Errorf("Expected at least 1 fallback attempt, got %d", result.FallbacksAttempted)
	}
}

func TestGenerateResponseSmart_NoFallbackOnAuthError(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)

	// Auth errors should NOT trigger fallbacks
	mock.SetResponses([]MockResponse{
		{Error: fmt.Errorf("API error: 401 - unauthorized")},
	})

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	if err == nil {
		t.Fatal("Expected error for auth failure")
	}

	if result.FallbacksAttempted != 0 {
		t.Errorf("Expected 0 fallback attempts for auth error, got %d", result.FallbacksAttempted)
	}
}

func TestGenerateResponseSmart_AllModelsExhausted(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)

	// All calls return rate limit errors
	mock.SetResponses([]MockResponse{
		{Error: &RateLimitError{StatusCode: 429, Message: "rate limited"}},
		{Error: &RateLimitError{StatusCode: 429, Message: "rate limited"}},
		{Error: &RateLimitError{StatusCode: 429, Message: "rate limited"}},
	})

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	if err == nil {
		t.Fatal("Expected error when all models exhausted")
	}

	if result.FallbacksAttempted == 0 {
		t.Error("Expected fallback attempts when all models fail")
	}

	if result.TotalLatencyMs <= 0 {
		t.Error("Expected positive total latency even on failure")
	}
}

func TestGenerateResponseSmart_BudgetEnforcement(t *testing.T) {
	// Set a very low budget so it's already exceeded
	router, mock := setupSmartRouter(t, 0.001)

	// Pre-record some usage to exceed the budget
	router.usageTracker.RecordUsage("mock", "claude-opus-4-6", 100000, 50000, 1000)

	mock.AddResponse("budget response", nil)

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "do something complex", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "budget response" {
		t.Errorf("Expected 'budget response', got '%s'", resp.GetContent())
	}

	// When over budget, the selector should have chosen haiku
	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	if result.SelectedModel != "claude-haiku-4-5-20251001" {
		t.Errorf("Expected haiku model when over budget, got %s", result.SelectedModel)
	}
}

func TestGenerateResponseSmart_WithProgress(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("progress response", nil)

	var progressCalled bool
	onProgress := func(status string) {
		progressCalled = true
	}

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmartWithProgress(
		context.Background(), session, "hi", "mock", onProgress,
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "progress response" {
		t.Errorf("Expected 'progress response', got '%s'", resp.GetContent())
	}

	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	// Progress may or may not be called depending on tool calls in response
	_ = progressCalled
}

func TestGenerateResponseSmart_ModelInResult(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("result check", nil)

	session := newTestSession()

	// Standard complexity message should use sonnet
	msg := "Please implement a basic REST endpoint for user registration"
	_, result, err := router.GenerateResponseSmart(context.Background(), session, msg, "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil SmartRoutingResult")
	}

	// Verify the result contains meaningful metadata
	if result.SelectedModel == "" {
		t.Error("Expected non-empty selected model in result")
	}
	if result.SelectionReason == "" {
		t.Error("Expected non-empty selection reason in result")
	}
}

// --- Rate limit detection tests ---

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"RateLimitError type", &RateLimitError{StatusCode: 429, Message: "limited"}, true},
		{"429 in message", fmt.Errorf("API error: 429 - too many requests"), true},
		{"rate limit text", fmt.Errorf("rate limit exceeded"), true},
		{"too many requests", fmt.Errorf("too many requests"), true},
		{"overloaded", fmt.Errorf("API overloaded, please retry"), true},
		{"auth error", fmt.Errorf("API error: 401 - unauthorized"), false},
		{"bad request", fmt.Errorf("API error: 400 - bad request"), false},
		{"generic error", fmt.Errorf("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRateLimitError(tt.err)
			if result != tt.expected {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"rate limit", &RateLimitError{StatusCode: 429, Message: "limited"}, true},
		{"500 error", fmt.Errorf("API error: 500 - internal server error"), true},
		{"502 error", fmt.Errorf("API error: 502 - bad gateway"), true},
		{"503 error", fmt.Errorf("API error: 503 - service unavailable"), true},
		{"504 error", fmt.Errorf("API error: 504 - gateway timeout"), true},
		{"timeout", fmt.Errorf("request timeout"), true},
		{"overloaded", fmt.Errorf("server overloaded"), true},
		{"auth error", fmt.Errorf("API error: 401 - unauthorized"), false},
		{"bad request", fmt.Errorf("API error: 400 - bad request"), false},
		{"not found", fmt.Errorf("API error: 404 - not found"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRetryableError(tt.err)
			if result != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// --- Fallback chain tests ---

func TestBuildFallbackChain_FromOpus(t *testing.T) {
	router, _ := setupSmartRouter(t, 0)

	primary := SelectionResult{
		Model: "claude-opus-4-6",
		Tier:  TierOpus,
	}

	chain := router.buildFallbackChain(primary)

	if len(chain) == 0 {
		t.Fatal("Expected non-empty fallback chain from opus")
	}

	// Should try sonnet first, then haiku
	if chain[0].Tier != TierSonnet {
		t.Errorf("Expected first fallback to be sonnet, got %s", chain[0].Tier)
	}

	if len(chain) >= 2 && chain[1].Tier != TierHaiku {
		t.Errorf("Expected second fallback to be haiku, got %s", chain[1].Tier)
	}
}

func TestBuildFallbackChain_FromHaiku(t *testing.T) {
	router, _ := setupSmartRouter(t, 0)

	primary := SelectionResult{
		Model: "claude-haiku-4-5-20251001",
		Tier:  TierHaiku,
	}

	chain := router.buildFallbackChain(primary)

	if len(chain) == 0 {
		t.Fatal("Expected non-empty fallback chain from haiku")
	}

	// Should try sonnet first, then opus
	if chain[0].Tier != TierSonnet {
		t.Errorf("Expected first fallback to be sonnet, got %s", chain[0].Tier)
	}

	if len(chain) >= 2 && chain[1].Tier != TierOpus {
		t.Errorf("Expected second fallback to be opus, got %s", chain[1].Tier)
	}
}

func TestBuildFallbackChain_FromSonnet(t *testing.T) {
	router, _ := setupSmartRouter(t, 0)

	primary := SelectionResult{
		Model: "claude-sonnet-4-6",
		Tier:  TierSonnet,
	}

	chain := router.buildFallbackChain(primary)

	if len(chain) == 0 {
		t.Fatal("Expected non-empty fallback chain from sonnet")
	}

	// Should include haiku and opus (haiku first for cost savings)
	if chain[0].Tier != TierHaiku {
		t.Errorf("Expected first fallback from sonnet to be haiku, got %s", chain[0].Tier)
	}
}

func TestBuildFallbackChain_NonDefaultSelector(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Use a custom ModelSelector that is not *DefaultModelSelector
	router.modelSelector = &mockModelSelector{
		model: "custom-model",
	}
	router.smartRoutingCfg = &config.SmartRoutingConfig{Enabled: true}

	primary := SelectionResult{Model: "custom-model", Tier: TierSonnet}
	chain := router.buildFallbackChain(primary)

	// Non-default selectors should return no fallback chain
	if chain != nil {
		t.Errorf("Expected nil fallback chain for non-default selector, got %v", chain)
	}
}

// --- Estimate input tokens ---

func TestEstimateInputTokens(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	session := newTestSession()

	// Without session store, should still return estimate from message + overhead
	tokens := router.estimateInputTokens(session, "Hello, this is a test message")
	if tokens <= 0 {
		t.Errorf("Expected positive token estimate, got %d", tokens)
	}

	// System prompt overhead should be included
	if tokens < 2000 {
		t.Errorf("Expected at least 2000 tokens (system prompt overhead), got %d", tokens)
	}
}

// --- IsSmartRoutingEnabled ---

func TestIsSmartRoutingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.SmartRoutingConfig
		selector ModelSelector
		expected bool
	}{
		{
			name:     "nil config",
			cfg:      nil,
			selector: &mockModelSelector{},
			expected: false,
		},
		{
			name:     "disabled config",
			cfg:      &config.SmartRoutingConfig{Enabled: false},
			selector: &mockModelSelector{},
			expected: false,
		},
		{
			name:     "enabled but no selector",
			cfg:      &config.SmartRoutingConfig{Enabled: true},
			selector: nil,
			expected: false,
		},
		{
			name:     "enabled with selector",
			cfg:      &config.SmartRoutingConfig{Enabled: true},
			selector: &mockModelSelector{},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := &Router{
				providers:       make(map[string]Provider),
				smartRoutingCfg: tt.cfg,
				modelSelector:   tt.selector,
			}

			if got := router.IsSmartRoutingEnabled(); got != tt.expected {
				t.Errorf("IsSmartRoutingEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// --- RateLimitError ---

func TestRateLimitError_Error(t *testing.T) {
	err := &RateLimitError{StatusCode: 429, Message: "too many requests"}
	expected := "rate limited (HTTP 429): too many requests"
	if err.Error() != expected {
		t.Errorf("Expected '%s', got '%s'", expected, err.Error())
	}
}

// --- SmartRoutingResult metadata ---

func TestSmartRoutingResult_Populated(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("test", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hello world", "mock")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.SelectedModel == "" {
		t.Error("Expected SelectedModel to be populated")
	}
	if result.SelectionReason == "" {
		t.Error("Expected SelectionReason to be populated")
	}
	// TotalLatencyMs can be 0 if the operation completes within the same
	// millisecond; just verify it's non-negative.
	if result.TotalLatencyMs < 0 {
		t.Errorf("Expected non-negative TotalLatencyMs, got %d", result.TotalLatencyMs)
	}
}

// --- Compatibility: existing behavior preserved ---

func TestGenerateResponseWithTools_StillWorks(t *testing.T) {
	// Ensure adding smart routing fields does not break the existing method
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("legacy response", nil)

	session := newTestSession()
	resp, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "legacy response" {
		t.Errorf("Expected 'legacy response', got '%s'", resp.GetContent())
	}
}

func TestGenerateResponseWithTools_WithModelOverride(t *testing.T) {
	router, mock := setupSmartRouter(t, 0)
	mock.AddResponse("override response", nil)

	session := newTestSession()
	resp, err := router.GenerateResponseWithTools(context.Background(), session, "test", "mock", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.GetContent() != "override response" {
		t.Errorf("Expected 'override response', got '%s'", resp.GetContent())
	}

	// Verify the model override was passed through
	calls := mock.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("Expected 1 call, got %d", len(calls))
	}
	if calls[0].Request.Model != "claude-opus-4-6" {
		t.Errorf("Expected model override 'claude-opus-4-6', got '%s'", calls[0].Request.Model)
	}
}

// --- Mock model selector for testing ---

type mockModelSelector struct {
	model string
}

func (m *mockModelSelector) SelectModel(ctx *SelectionContext) SelectionResult {
	return SelectionResult{
		Model:  m.model,
		Tier:   TierSonnet,
		Reason: "mock selection",
	}
}
