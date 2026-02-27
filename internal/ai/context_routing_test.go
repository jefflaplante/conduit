package ai

import (
	"context"
	"testing"

	"conduit/internal/config"
	"conduit/internal/fts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock context engine for testing ---

type mockContextEngine struct {
	ctx *RoutingContext
}

func (m *mockContextEngine) RetrieveContext(_ context.Context, _ string) *RoutingContext {
	return m.ctx
}

// --- Setup helpers ---

// setupSmartRouterWithContext creates a smart router with an optional context engine.
func setupSmartRouterWithContext(t *testing.T, engine ContextEngine) (*Router, *MockProvider) {
	t.Helper()

	router, mock := setupSmartRouter(t, 0)
	if engine != nil {
		router.SetContextEngine(engine)
	}
	return router, mock
}

// --- Tests: context engine integration in smart routing flow ---

func TestSmartRouting_ContextEngineQueried(t *testing.T) {
	// Verify the context engine is called during smart routing when set.
	var called bool
	engine := &trackingContextEngine{
		inner: &mockContextEngine{
			ctx: &RoutingContext{Source: "none"},
		},
		onRetrieve: func(request string) {
			called = true
			assert.Equal(t, "hello", request)
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("ok", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hello", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, called, "context engine should have been called")
}

// trackingContextEngine wraps a ContextEngine and calls onRetrieve before delegating.
type trackingContextEngine struct {
	inner      ContextEngine
	onRetrieve func(string)
}

func (t *trackingContextEngine) RetrieveContext(ctx context.Context, request string) *RoutingContext {
	if t.onRetrieve != nil {
		t.onRetrieve(request)
	}
	return t.inner.RetrieveContext(ctx, request)
}

// --- Tests: context hints influence model selection ---

func TestSmartRouting_ContextHintInfluencesSelection(t *testing.T) {
	// A simple message ("hi") would normally select haiku.
	// But if the context engine suggests opus with high confidence,
	// it should be upgraded to opus.
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: []SimilarRequest{
				{Content: "hello there", Score: 0.9, Role: "user", Source: "fts5"},
			},
			Hints: []RoutingHint{
				{SuggestedTier: TierOpus, Confidence: 0.7, Reason: "similar past requests were complex"},
			},
			SearchLatencyMs: 5,
			Source:          "fts5",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("upgraded response", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	// Context engine should have influenced the decision
	assert.True(t, result.ContextInfluenced)
	assert.Equal(t, TierOpus, result.ContextSuggestedTier)
	assert.Equal(t, TierOpus, result.Tier)
	assert.Equal(t, "claude-opus-4-6", result.SelectedModel)
}

func TestSmartRouting_ContextHintDowngradesSelection(t *testing.T) {
	// A complex message would normally select opus.
	// But if the context engine suggests haiku with high confidence,
	// it should be downgraded.
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: []SimilarRequest{
				{Content: "short answer", Score: 0.8, Role: "assistant", Source: "fts5"},
			},
			Hints: []RoutingHint{
				{SuggestedTier: TierHaiku, Confidence: 0.6, Reason: "similar requests had short responses"},
			},
			SearchLatencyMs: 3,
			Source:          "fts5",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("downgraded response", nil)

	session := newTestSession()
	msg := "Please refactor the entire authentication module to use OAuth2 with PKCE flow. " +
		"Analyze the existing codebase, implement the migration plan, and update all tests. " +
		"This involves multiple files across the architecture."

	_, result, err := router.GenerateResponseSmart(context.Background(), session, msg, "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.ContextInfluenced)
	assert.Equal(t, TierHaiku, result.ContextSuggestedTier)
	assert.Equal(t, TierHaiku, result.Tier)
	assert.Equal(t, "claude-haiku-4-5-20251001", result.SelectedModel)
}

func TestSmartRouting_ContextHintSameTierNoChange(t *testing.T) {
	// When the context engine suggests the same tier as complexity analysis,
	// the result should not be marked as context-influenced.
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: []SimilarRequest{
				{Content: "greeting", Score: 0.8, Role: "user", Source: "fts5"},
			},
			Hints: []RoutingHint{
				{SuggestedTier: TierHaiku, Confidence: 0.7, Reason: "same as complexity"},
			},
			SearchLatencyMs: 2,
			Source:          "fts5",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("ok", nil)

	session := newTestSession()
	// "hi" is a simple message -> haiku; context also says haiku
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.ContextInfluenced, "same-tier suggestion should not be marked as influenced")
	assert.Equal(t, TierHaiku, result.Tier)
}

// --- Tests: fallback when context engine returns empty ---

func TestSmartRouting_EmptyContextFallsBackToComplexity(t *testing.T) {
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: nil,
			Hints:           nil,
			Source:          "none",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("complexity-based", nil)

	session := newTestSession()
	// Simple message -> should select haiku via complexity analysis
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.ContextInfluenced)
	assert.Equal(t, ModelTier(-1), result.ContextSuggestedTier)
	assert.Equal(t, TierHaiku, result.Tier)
	assert.Equal(t, "none", result.ContextSource)
}

// --- Tests: fallback when context engine is nil ---

func TestSmartRouting_NilContextEngineWorksNormally(t *testing.T) {
	// No context engine set -> smart routing should work exactly as before
	router, mock := setupSmartRouterWithContext(t, nil)
	mock.AddResponse("no context", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.ContextInfluenced)
	assert.Equal(t, ModelTier(-1), result.ContextSuggestedTier)
	assert.Equal(t, int64(0), result.ContextSearchLatencyMs)
	assert.Equal(t, "", result.ContextSource)
}

// --- Tests: low confidence does not influence ---

func TestSmartRouting_LowConfidenceContextIgnored(t *testing.T) {
	// Context suggests opus, but with very low confidence (below threshold)
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: []SimilarRequest{
				{Content: "some match", Score: 0.3, Role: "user", Source: "fts5"},
			},
			Hints: []RoutingHint{
				{SuggestedTier: TierOpus, Confidence: 0.3, Reason: "weak signal"},
			},
			SearchLatencyMs: 4,
			Source:          "fts5",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("no influence", nil)

	session := newTestSession()
	// "hi" is simple -> should stay haiku despite context suggesting opus
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.ContextInfluenced)
	assert.Equal(t, TierOpus, result.ContextSuggestedTier) // suggested but not applied
	assert.Equal(t, TierHaiku, result.Tier)                // stayed at complexity-based tier
}

// --- Tests: observability metadata in SmartRoutingResult ---

func TestSmartRouting_ContextObservabilityFields(t *testing.T) {
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			SimilarRequests: []SimilarRequest{
				{Content: "previous interaction", Score: 0.95, Role: "assistant", Source: "vector"},
			},
			Hints: []RoutingHint{
				{SuggestedTier: TierSonnet, Confidence: 0.65, Reason: "historical pattern"},
			},
			SearchLatencyMs: 42,
			Source:          "hybrid",
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("result", nil)

	session := newTestSession()
	// "hi" is simple (haiku), context suggests sonnet with sufficient confidence
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify all observability fields
	assert.True(t, result.ContextInfluenced)
	assert.Equal(t, TierSonnet, result.ContextSuggestedTier)
	assert.Equal(t, int64(42), result.ContextSearchLatencyMs)
	assert.Equal(t, "hybrid", result.ContextSource)
	assert.Equal(t, TierSonnet, result.Tier)

	// The selection reason should still be populated
	assert.NotEmpty(t, result.SelectionReason)
	// The complexity reasons should include the context engine note
	assert.Contains(t, result.Complexity.Reasons, "context engine suggests sonnet tier (confidence=0.65)")
}

func TestSmartRouting_ContextObservabilityWhenEmpty(t *testing.T) {
	engine := &mockContextEngine{
		ctx: &RoutingContext{
			Source:          "none",
			SearchLatencyMs: 1,
		},
	}

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("result", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hello", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	// Empty context: observability fields should still be populated
	assert.False(t, result.ContextInfluenced)
	assert.Equal(t, ModelTier(-1), result.ContextSuggestedTier)
	assert.Equal(t, int64(1), result.ContextSearchLatencyMs)
	assert.Equal(t, "none", result.ContextSource)
}

// --- Tests: context engine with real DefaultContextEngine (integration) ---

func TestSmartRouting_WithDefaultContextEngine(t *testing.T) {
	// Use a real DefaultContextEngine with a mock search service
	searchSvc := &mockSearchService{
		messages: []fts.MessageResult{
			{Content: "This is a really long response that indicates a complex task was being handled previously. " +
				"The assistant provided detailed analysis and comprehensive output spanning many paragraphs of explanation.",
				Rank: -2.5, SessionKey: "session-1", Role: "assistant"},
			{Content: "Another long response here with detailed technical explanation " +
				"covering multiple aspects of the problem domain at hand.",
				Rank: -1.8, SessionKey: "session-2", Role: "assistant"},
		},
	}

	engine := NewContextEngine(
		WithSearchService(searchSvc),
		WithMaxResults(5),
	)

	router, mock := setupSmartRouterWithContext(t, engine)
	mock.AddResponse("with-real-engine", nil)

	session := newTestSession()
	_, result, err := router.GenerateResponseSmart(context.Background(), session, "hi", "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	// The real engine found similar results with long assistant content,
	// so it should suggest a higher tier. Verify observability works.
	assert.Equal(t, "fts5", result.ContextSource)
	assert.Greater(t, result.ContextSearchLatencyMs, int64(-1)) // non-negative
}

// --- Tests: SetContextEngine setter ---

func TestSetContextEngine(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	require.NoError(t, err)

	// Initially nil
	assert.Nil(t, router.contextEngine)

	// Set an engine
	engine := &mockContextEngine{ctx: &RoutingContext{Source: "test"}}
	router.SetContextEngine(engine)
	assert.NotNil(t, router.contextEngine)

	// Can set to nil
	router.SetContextEngine(nil)
	assert.Nil(t, router.contextEngine)
}

// --- Tests: applyContextInfluence directly ---

func TestApplyContextInfluence_NoSuggestion(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	result := router.applyContextInfluence(&combined, &RoutingContext{}, ModelTier(-1))
	assert.False(t, result)
	assert.Equal(t, ComplexitySimple, combined.Level) // unchanged
}

func TestApplyContextInfluence_HighConfidenceUpgrade(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierOpus, Confidence: 0.7},
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierOpus)
	assert.True(t, result)
	assert.Equal(t, ComplexityComplex, combined.Level) // upgraded
}

func TestApplyContextInfluence_HighConfidenceDowngrade(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexityComplex, Score: 60}

	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierHaiku, Confidence: 0.6},
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierHaiku)
	assert.True(t, result)
	assert.Equal(t, ComplexitySimple, combined.Level) // downgraded
}

func TestApplyContextInfluence_LowConfidenceNoChange(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierOpus, Confidence: 0.3},
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierOpus)
	assert.False(t, result)
	assert.Equal(t, ComplexitySimple, combined.Level) // unchanged
}

func TestApplyContextInfluence_SameTierNoChange(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierHaiku, Confidence: 0.9},
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierHaiku)
	assert.False(t, result)
	assert.Equal(t, ComplexitySimple, combined.Level) // unchanged (same tier)
}

func TestApplyContextInfluence_MultipleHintsAveraged(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	// Two hints for opus: one high confidence, one low. Average = 0.55, above threshold.
	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierOpus, Confidence: 0.7},
			{SuggestedTier: TierOpus, Confidence: 0.4},
			{SuggestedTier: TierSonnet, Confidence: 0.9}, // different tier, ignored
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierOpus)
	assert.True(t, result)
	assert.Equal(t, ComplexityComplex, combined.Level) // upgraded
}

func TestApplyContextInfluence_MultipleHintsBelowThreshold(t *testing.T) {
	router := &Router{}
	combined := ComplexityScore{Level: ComplexitySimple, Score: 5}

	// Two hints for opus: both low confidence. Average = 0.35, below threshold.
	ctx := &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierOpus, Confidence: 0.3},
			{SuggestedTier: TierOpus, Confidence: 0.4},
		},
	}

	result := router.applyContextInfluence(&combined, ctx, TierOpus)
	assert.False(t, result)
	assert.Equal(t, ComplexitySimple, combined.Level) // unchanged
}

// --- Tests: complexityLevelToTier ---

func TestComplexityLevelToTier(t *testing.T) {
	assert.Equal(t, TierHaiku, complexityLevelToTier(ComplexitySimple))
	assert.Equal(t, TierSonnet, complexityLevelToTier(ComplexityStandard))
	assert.Equal(t, TierOpus, complexityLevelToTier(ComplexityComplex))
	assert.Equal(t, TierSonnet, complexityLevelToTier(ComplexityLevel(99))) // default
}

// --- Tests: existing behavior preserved with context engine ---

func TestSmartRouting_ExistingBehaviorPreservedWithNilEngine(t *testing.T) {
	// Ensure the smart routing flow is identical when no context engine is set.
	// This duplicates TestGenerateResponseSmart_ComplexMessage but explicitly
	// verifies no context fields are populated.
	router, mock := setupSmartRouterWithContext(t, nil)
	mock.AddResponse("detailed analysis", nil)

	session := newTestSession()
	msg := "Please refactor the entire authentication module to use OAuth2 with PKCE flow. " +
		"Analyze the existing codebase, implement the migration plan, and update all tests. " +
		"This involves multiple files across the architecture."

	resp, result, err := router.GenerateResponseSmart(context.Background(), session, msg, "mock")
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "detailed analysis", resp.GetContent())
	assert.Equal(t, ComplexityComplex, result.Complexity.Level)
	assert.Equal(t, "claude-opus-4-6", result.SelectedModel)
	assert.Equal(t, TierOpus, result.Tier)

	// No context influence
	assert.False(t, result.ContextInfluenced)
	assert.Equal(t, ModelTier(-1), result.ContextSuggestedTier)
	assert.Equal(t, int64(0), result.ContextSearchLatencyMs)
	assert.Equal(t, "", result.ContextSource)
}

func TestSmartRouting_DisabledSmartRoutingIgnoresContextEngine(t *testing.T) {
	// When smart routing is disabled, the context engine should not be queried.
	var called bool
	engine := &trackingContextEngine{
		inner: &mockContextEngine{ctx: &RoutingContext{Source: "test"}},
		onRetrieve: func(_ string) {
			called = true
		},
	}

	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	require.NoError(t, err)

	mock := NewMockProvider("mock")
	mock.AddResponse("plain", nil)
	router.RegisterProvider("mock", mock)

	// Set context engine but do NOT enable smart routing
	router.SetContextEngine(engine)

	session := newTestSession()
	resp, result, err := router.GenerateResponseSmart(context.Background(), session, "hello", "mock")
	require.NoError(t, err)

	assert.Nil(t, result, "result should be nil when smart routing is disabled")
	assert.Equal(t, "plain", resp.GetContent())
	assert.False(t, called, "context engine should not be called when smart routing is disabled")
}
