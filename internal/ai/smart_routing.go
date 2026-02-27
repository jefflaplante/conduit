package ai

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"conduit/internal/sessions"
)

// SmartRoutingResult captures metadata about the smart routing decision for
// logging and metrics. It is returned alongside the conversation response.
type SmartRoutingResult struct {
	// SelectedModel is the model that ultimately handled the request.
	SelectedModel string `json:"selected_model"`

	// SelectionReason explains why this model was chosen.
	SelectionReason string `json:"selection_reason"`

	// Tier is the model tier that was selected.
	Tier ModelTier `json:"tier"`

	// Complexity is the analyzed complexity of the request.
	Complexity ComplexityScore `json:"complexity"`

	// FallbacksAttempted is the number of fallback models tried before success.
	FallbacksAttempted int `json:"fallbacks_attempted"`

	// TotalLatencyMs is the total time spent including retries.
	TotalLatencyMs int64 `json:"total_latency_ms"`

	// ContextInfluenced indicates whether the context engine influenced the
	// model selection decision.
	ContextInfluenced bool `json:"context_influenced,omitempty"`

	// ContextSuggestedTier is the tier suggested by the context engine, or -1
	// if context was not available or returned no suggestion.
	ContextSuggestedTier ModelTier `json:"context_suggested_tier,omitempty"`

	// ContextSearchLatencyMs is the time the context engine spent retrieving
	// historical context. Zero if context engine was not used.
	ContextSearchLatencyMs int64 `json:"context_search_latency_ms,omitempty"`

	// ContextSource indicates which search backends contributed context
	// ("fts5", "vector", "hybrid", "none", or "" if no context engine).
	ContextSource string `json:"context_source,omitempty"`
}

// RateLimitError represents an error that indicates rate limiting from the
// provider. The router uses this to decide whether to attempt fallbacks.
type RateLimitError struct {
	StatusCode   int
	RetryAfterMs int64
	Message      string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (HTTP %d): %s", e.StatusCode, e.Message)
}

// isRateLimitError checks if an error indicates rate limiting.
// It looks for common rate limit indicators in the error message and checks
// for the RateLimitError type.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*RateLimitError); ok {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "overloaded")
}

// isRetryableError checks if an error is worth retrying (rate limits,
// transient server errors). Non-retryable errors (auth, bad request) should
// not trigger fallback attempts.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if isRateLimitError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "overloaded") ||
		strings.Contains(msg, "internal server error")
}

// GenerateResponseSmart is the intelligent routing entry point. It analyzes
// the message complexity, selects the best model via the ModelSelector,
// executes the request with fallback handling, and records usage metrics.
//
// If smart routing is not enabled (no ModelSelector configured), it falls
// back to GenerateResponseWithTools with no model override.
func (r *Router) GenerateResponseSmart(ctx context.Context, session *sessions.Session, userMessage string, providerName string) (ConversationResponse, *SmartRoutingResult, error) {
	return r.GenerateResponseSmartWithProgress(ctx, session, userMessage, providerName, nil)
}

// GenerateResponseSmartWithProgress is like GenerateResponseSmart but with
// progress callbacks for long operations.
func (r *Router) GenerateResponseSmartWithProgress(ctx context.Context, session *sessions.Session, userMessage string, providerName string, onProgress ProgressCallback) (ConversationResponse, *SmartRoutingResult, error) {
	totalStart := time.Now()

	// If smart routing is not enabled, delegate to existing method
	if !r.IsSmartRoutingEnabled() {
		resp, err := r.GenerateResponseWithToolsAndProgress(ctx, session, userMessage, providerName, "", onProgress)
		return resp, nil, err
	}

	// Step 1: Analyze message complexity
	analyzer := r.complexityAnalyzer
	if analyzer == nil {
		analyzer = NewComplexityAnalyzer()
	}
	msgComplexity := analyzer.AnalyzeMessage(userMessage)

	// Incorporate tool availability into complexity estimate
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions()
	}
	toolComplexity := analyzer.AnalyzeToolDefinitions(tools)
	combined := analyzer.CombineScores(msgComplexity, toolComplexity)

	log.Printf("[SmartRouting] Complexity analysis: level=%s score=%d reasons=%v",
		combined.Level, combined.Score, combined.Reasons)

	// Step 2: Query context engine for historical routing context (optional)
	var routingCtx *RoutingContext
	var contextSuggestedTier ModelTier = -1
	var contextInfluenced bool

	if r.contextEngine != nil {
		routingCtx = r.contextEngine.RetrieveContext(ctx, userMessage)
		if routingCtx != nil && !routingCtx.IsEmpty() {
			contextSuggestedTier = routingCtx.SuggestedTier()
			log.Printf("[SmartRouting] Context engine returned %d similar requests, %d hints (source=%s, latency=%dms, suggested_tier=%s)",
				len(routingCtx.SimilarRequests), len(routingCtx.Hints),
				routingCtx.Source, routingCtx.SearchLatencyMs, contextSuggestedTier)

			// Apply context-based tier adjustment if the context engine has
			// a strong suggestion that differs from the complexity-based tier.
			contextInfluenced = r.applyContextInfluence(&combined, routingCtx, contextSuggestedTier)
		}
	}

	// Step 3: Estimate input tokens from session context + user message
	estimatedTokens := r.estimateInputTokens(session, userMessage)

	// Step 4: Select model via strategy
	selectionCtx := &SelectionContext{
		Complexity:           combined,
		EstimatedInputTokens: estimatedTokens,
		AvailableTools:       tools,
		ProviderName:         providerName,
	}
	selection := r.modelSelector.SelectModel(selectionCtx)

	log.Printf("[SmartRouting] Model selected: %s (tier=%s, reason=%s, context_influenced=%v)",
		selection.Model, selection.Tier, selection.Reason, contextInfluenced)

	// Step 5: Execute with fallbacks
	result := &SmartRoutingResult{
		SelectedModel:        selection.Model,
		SelectionReason:      selection.Reason,
		Tier:                 selection.Tier,
		Complexity:           combined,
		ContextInfluenced:    contextInfluenced,
		ContextSuggestedTier: contextSuggestedTier,
	}

	// Populate context observability fields
	if routingCtx != nil {
		result.ContextSearchLatencyMs = routingCtx.SearchLatencyMs
		result.ContextSource = routingCtx.Source
	}

	resp, err := r.executeWithFallbacks(ctx, session, userMessage, providerName, selection, onProgress, result)

	result.TotalLatencyMs = time.Since(totalStart).Milliseconds()

	if err != nil {
		log.Printf("[SmartRouting] Request failed after %d fallback(s): %v", result.FallbacksAttempted, err)
		return nil, result, err
	}

	log.Printf("[SmartRouting] Request succeeded: model=%s fallbacks=%d latency=%dms",
		result.SelectedModel, result.FallbacksAttempted, result.TotalLatencyMs)

	return resp, result, nil
}

// executeWithFallbacks attempts the request with the selected model, then
// falls back to alternative models if the primary fails with a retryable
// error (rate limit, server error).
func (r *Router) executeWithFallbacks(ctx context.Context, session *sessions.Session, userMessage string, providerName string, primary SelectionResult, onProgress ProgressCallback, result *SmartRoutingResult) (ConversationResponse, error) {
	// Attempt primary model
	resp, err := r.GenerateResponseWithToolsAndProgress(ctx, session, userMessage, providerName, primary.Model, onProgress)
	if err == nil {
		return resp, nil
	}

	// Check if error is retryable
	if !isRetryableError(err) {
		log.Printf("[SmartRouting] Non-retryable error from %s: %v", primary.Model, err)
		return nil, err
	}

	log.Printf("[SmartRouting] Retryable error from %s: %v, attempting fallbacks", primary.Model, err)

	// If rate limited, handle with backoff before trying fallbacks
	if isRateLimitError(err) {
		r.handleRateLimit(primary.Model, err)
	}

	// Record error for the primary model
	if r.usageTracker != nil {
		r.usageTracker.RecordError(providerName, primary.Model)
	}

	// Build fallback chain: try other tiers in order of preference
	fallbacks := r.buildFallbackChain(primary)

	for _, fallback := range fallbacks {
		result.FallbacksAttempted++
		log.Printf("[SmartRouting] Trying fallback model: %s (tier=%s)", fallback.Model, fallback.Tier)

		resp, err = r.GenerateResponseWithToolsAndProgress(ctx, session, userMessage, providerName, fallback.Model, onProgress)
		if err == nil {
			result.SelectedModel = fallback.Model
			result.SelectionReason = fmt.Sprintf("fallback from %s: %s", primary.Model, fallback.Reason)
			result.Tier = fallback.Tier
			return resp, nil
		}

		if !isRetryableError(err) {
			log.Printf("[SmartRouting] Non-retryable error from fallback %s: %v", fallback.Model, err)
			return nil, err
		}

		log.Printf("[SmartRouting] Fallback %s also failed: %v", fallback.Model, err)

		if isRateLimitError(err) {
			r.handleRateLimit(fallback.Model, err)
		}

		if r.usageTracker != nil {
			r.usageTracker.RecordError(providerName, fallback.Model)
		}
	}

	// All models exhausted
	return nil, fmt.Errorf("all models exhausted after %d fallback(s): %w", result.FallbacksAttempted, err)
}

// buildFallbackChain constructs the ordered list of alternative models to try
// after the primary model fails. The chain excludes the primary model and
// orders alternatives by preference:
//   - If the primary was a high tier (opus), try lower tiers (sonnet, haiku)
//   - If the primary was a low tier (haiku), try higher tiers (sonnet, opus)
//   - If the primary was mid tier (sonnet), try both directions
func (r *Router) buildFallbackChain(primary SelectionResult) []SelectionResult {
	// Get all available models from the selector
	selector, ok := r.modelSelector.(*DefaultModelSelector)
	if !ok {
		// Non-default selector: no fallback chain available
		return nil
	}

	tiers := selector.GetTiers()
	var chain []SelectionResult

	switch primary.Tier {
	case TierOpus:
		// Opus failed: try sonnet, then haiku
		for i := len(tiers) - 2; i >= 0; i-- {
			if tiers[i].ModelID != primary.Model {
				chain = append(chain, SelectionResult{
					Model:  tiers[i].ModelID,
					Tier:   tiers[i].Tier,
					Reason: "fallback from higher tier",
				})
			}
		}
	case TierHaiku:
		// Haiku failed: try sonnet, then opus
		for i := 1; i < len(tiers); i++ {
			if tiers[i].ModelID != primary.Model {
				chain = append(chain, SelectionResult{
					Model:  tiers[i].ModelID,
					Tier:   tiers[i].Tier,
					Reason: "fallback from lower tier",
				})
			}
		}
	default:
		// Sonnet (mid-tier): try haiku first (cheaper), then opus
		for i := 0; i < len(tiers); i++ {
			if tiers[i].ModelID != primary.Model {
				chain = append(chain, SelectionResult{
					Model:  tiers[i].ModelID,
					Tier:   tiers[i].Tier,
					Reason: "fallback from mid tier",
				})
			}
		}
	}

	return chain
}

// handleRateLimit logs rate limit info and applies a short backoff delay.
// It uses exponential backoff based on how many errors have been recorded
// for the model. The delay is capped to avoid blocking too long.
func (r *Router) handleRateLimit(model string, err error) {
	// Check if we have specific retry-after information
	if rlErr, ok := err.(*RateLimitError); ok && rlErr.RetryAfterMs > 0 {
		delay := time.Duration(rlErr.RetryAfterMs) * time.Millisecond
		if delay > 10*time.Second {
			delay = 10 * time.Second // Cap at 10 seconds
		}
		log.Printf("[SmartRouting] Rate limited on %s, waiting %v (retry-after)", model, delay)
		time.Sleep(delay)
		return
	}

	// Exponential backoff based on error count for this model
	baseDelay := 500 * time.Millisecond
	maxDelay := 5 * time.Second

	errorCount := int64(1)
	if r.usageTracker != nil {
		if usage, ok := r.usageTracker.GetModelUsage(model); ok {
			errorCount = usage.ErrorCount
			if errorCount < 1 {
				errorCount = 1
			}
		}
	}

	// Exponential: 500ms, 1s, 2s, 4s, 5s (capped)
	delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(errorCount-1)))
	if delay > maxDelay {
		delay = maxDelay
	}

	log.Printf("[SmartRouting] Rate limited on %s, backing off %v (attempt %d)", model, delay, errorCount)
	time.Sleep(delay)
}

// contextInfluenceMinConfidence is the minimum average confidence across
// routing hints required for the context engine to influence model selection.
// Below this threshold, complexity-based selection is used unchanged.
const contextInfluenceMinConfidence = 0.5

// applyContextInfluence adjusts the complexity score based on the context
// engine's routing hints. If the context engine suggests a different tier
// with sufficient confidence, the complexity score is adjusted to steer the
// model selector toward the suggested tier.
//
// Returns true if the context actually changed the selection outcome.
func (r *Router) applyContextInfluence(combined *ComplexityScore, routingCtx *RoutingContext, suggestedTier ModelTier) bool {
	if suggestedTier < 0 {
		return false // No suggestion
	}

	// Compute average confidence of hints pointing to the suggested tier
	var totalConfidence float64
	var matchingHints int
	for _, h := range routingCtx.Hints {
		if h.SuggestedTier == suggestedTier {
			totalConfidence += h.Confidence
			matchingHints++
		}
	}
	if matchingHints == 0 {
		return false
	}
	avgConfidence := totalConfidence / float64(matchingHints)

	if avgConfidence < contextInfluenceMinConfidence {
		log.Printf("[SmartRouting] Context suggestion confidence (%.2f) below threshold (%.2f), ignoring",
			avgConfidence, contextInfluenceMinConfidence)
		return false
	}

	// Determine what the complexity-based tier would be
	complexityTier := complexityLevelToTier(combined.Level)

	// If the context suggests the same tier, no adjustment needed
	if suggestedTier == complexityTier {
		return false
	}

	// Adjust the complexity score to steer toward the context-suggested tier.
	// We modify the score/level so the existing ModelSelector picks the right
	// tier without needing any changes to strategy.go.
	var targetLevel ComplexityLevel
	switch suggestedTier {
	case TierHaiku:
		targetLevel = ComplexitySimple
	case TierSonnet:
		targetLevel = ComplexityStandard
	case TierOpus:
		targetLevel = ComplexityComplex
	default:
		return false
	}

	combined.Level = targetLevel
	combined.Reasons = append(combined.Reasons,
		fmt.Sprintf("context engine suggests %s tier (confidence=%.2f)", suggestedTier, avgConfidence))

	log.Printf("[SmartRouting] Context engine adjusted complexity from %s to %s (context suggests %s, confidence=%.2f)",
		complexityTier, suggestedTier, suggestedTier, avgConfidence)

	return true
}

// complexityLevelToTier maps a ComplexityLevel to the corresponding ModelTier.
func complexityLevelToTier(level ComplexityLevel) ModelTier {
	switch level {
	case ComplexitySimple:
		return TierHaiku
	case ComplexityStandard:
		return TierSonnet
	case ComplexityComplex:
		return TierOpus
	default:
		return TierSonnet
	}
}

// estimateInputTokens provides a rough estimate of the number of input tokens
// that will be sent in the request. This uses a simple heuristic of ~4 chars
// per token (common for English text).
func (r *Router) estimateInputTokens(session *sessions.Session, userMessage string) int {
	const charsPerToken = 4

	// Estimate from user message
	tokens := len(userMessage) / charsPerToken

	// Estimate from recent session history
	if r.sessionStore != nil {
		messages, err := r.sessionStore.GetMessages(session.Key, 20)
		if err == nil {
			for _, msg := range messages {
				tokens += len(msg.Content) / charsPerToken
			}
		}
	}

	// Add estimate for system prompt overhead (agent prompt is typically 1-3K tokens)
	tokens += 2000

	return tokens
}
