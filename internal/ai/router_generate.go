package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/constants"
	"conduit/internal/sessions"
)

func (r *Router) GenerateResponse(ctx context.Context, session *sessions.Session, userMessage string, providerName string) (*GenerateResponse, error) {
	// Use default provider if none specified
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.getProvider(providerName)
	if !exists {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	log.Printf("[Router] Generate: provider=%q", providerName)
	ctx = r.withCallTurn(ctx, session) // conduit-2lzv

	// Build system prompt using agent system
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("failed to build system prompt: %w", err)
		}
		systemBlocks = blocks
	}

	// Build chat messages from session history with agent system prompt
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build chat messages: %w", err)
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions(session)
	}

	req := &GenerateRequest{
		Messages:  messages,
		Tools:     tools,
		MaxTokens: r.chainMaxTokens(),
	}
	// conduit-31jg.18: (provider, model) travel together through the
	// quota-fallback and timeout retries; each attempt is trimmed to its
	// route's window (callWithRecovery).
	response, served, _, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: provider, model: req.Model},
		req, recoveryOpts{phase: "generate"})
	// conduit-31jg.64: every provider call (each recovery attempt here, and
	// every later call through the guarded provider) is recorded to the
	// usage tracker exactly once by the metering hook (route_call.go).
	if err != nil {
		return nil, err
	}
	provider = r.guardedProvider(served) // conduit-31jg.18(b): later calls re-trim

	// conduit-31jg.11: length-truncated reply → auto-continue (bd-1k3o parity).
	response = ContinueLengthTruncated(ctx, provider, req, response, "generate")

	// Process response through agent system
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			return nil, fmt.Errorf("failed to process response: %w", err)
		}

		// Update response based on agent processing
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = "" // Mark as silent
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	return response, nil
}

// ProgressCallback is called during long operations to provide status updates
type ProgressCallback func(status string)

// GenerateResponseWithTools generates an AI response with tool execution support
// modelOverride can be empty to use the default, or a specific model name/alias
func (r *Router) GenerateResponseWithTools(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string) (ConversationResponse, error) {
	return r.GenerateResponseWithToolsAndProgress(ctx, session, userMessage, providerName, modelOverride, nil)
}

// GenerateResponseWithToolsAndProgress is like GenerateResponseWithTools but with progress callbacks.
// Acquires the per-session turn lock for the duration of the chain.
func (r *Router) GenerateResponseWithToolsAndProgress(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onProgress ProgressCallback) (ConversationResponse, error) {
	unlock := r.lockSessionCtx(ctx, sessionKeyOf(session)) // conduit-31jg.35: no-op under an AcquireTurn lease
	defer unlock()
	return r.generateResponseWithToolsLocked(ctx, session, userMessage, providerName, modelOverride, onProgress)
}

// generateResponseWithToolsLocked is the turn-lock-holding variant. Callers must already
// hold the per-session turn lock (e.g., via lockSession). This avoids double-locking when
// GenerateResponseStreaming falls back to the non-streaming path.
func (r *Router) generateResponseWithToolsLocked(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onProgress ProgressCallback) (ConversationResponse, error) {
	chainStart := time.Now()
	log.Printf("[Router] >>> LLM CHAIN START")
	var chainErr error
	defer func() {
		if chainErr != nil {
			log.Printf("[Router] <<< LLM CHAIN END (%s) ERROR", time.Since(chainStart))
		} else {
			log.Printf("[Router] <<< LLM CHAIN END (%s)", time.Since(chainStart))
		}
	}()

	// Handle bare provider name used as model (e.g., model="ghost" where "ghost" is a provider)
	if modelOverride != "" && !strings.Contains(modelOverride, "/") {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" && strings.EqualFold(resolved, modelOverride) {
			log.Printf("[Router] Bare provider name %q used as model — routing to provider with its default model", modelOverride)
			providerName = resolved
			modelOverride = ""
		}
	}

	// Resolve provider from model only when:
	// 1. No provider explicitly specified, OR
	// 2. Model has explicit provider prefix (e.g., "ghost/model")
	if modelOverride != "" && (providerName == "" || strings.Contains(modelOverride, "/")) {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" {
			if providerName != "" && providerName != resolved {
				log.Printf("[Router] WithTools: overriding provider %q → %q (from model %q)", providerName, resolved, modelOverride)
			}
			providerName = resolved
		}
	}
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.getProvider(providerName)
	if !exists {
		chainErr = fmt.Errorf("provider not found: %s", providerName)
		return nil, chainErr
	}

	contextWindow := r.contextWindowForProvider(providerName)
	log.Printf("[Router] WithTools: provider=%q model=%q context_window=%d", providerName, modelOverride, contextWindow)
	ctx = r.withCallTurn(ctx, session) // conduit-2lzv

	// Build system prompt using agent system
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			chainErr = fmt.Errorf("failed to build system prompt: %w", err)
			return nil, chainErr
		}
		systemBlocks = blocks
	}

	// Build chat messages from session history with agent system prompt
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		chainErr = fmt.Errorf("failed to build chat messages: %w", err)
		return nil, chainErr
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions(session)
	}

	req := &GenerateRequest{
		Messages:  messages,
		Model:     modelOverride,
		Tools:     tools,
		MaxTokens: r.chainMaxTokens(),
	}
	// Get initial AI response. conduit-31jg.18: quota fallback and the
	// timeout retry carry (provider, model) as a pair — a timed-out fallback
	// call is retried on the FALLBACK provider, never the original.
	response, served, latencyMs, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: provider, model: req.Model},
		req, recoveryOpts{phase: "tool loop", quotaFallbackNeedsModel: true})
	if err != nil {
		// conduit-31jg.64: failed attempts were recorded by the metering hook.
		chainErr = fmt.Errorf("AI provider error: %w", err)
		return nil, chainErr
	}
	// The tool-loop continuation (HandleToolCallFlow) must stay on the
	// provider that served the response (bd-27ud: anthropic 404
	// not_found_error, 2026-09-04 sub-agent death), and every later round
	// is re-trimmed to that route's window (conduit-31jg.18(b)).
	provider = r.guardedProvider(served)
	// conduit-31jg.64: usage for this and every later call of the turn is
	// recorded per call by the metering hook, not here.

	// conduit-18vj: a raw-empty response (no content AND no tool calls) must
	// never complete a turn silently — retry once, then deliver a visible
	// fallback so every turn ends with SOMETHING.
	response, err = GuardEmptyResponse(ctx, provider, req, response, err, "initial")
	if err != nil {
		// Unreachable today (err is nil here and the guard only propagates
		// the incoming error), but mirror the streaming path so a future
		// guard error can never reach the response.Usage deref below.
		chainErr = fmt.Errorf("AI provider error: %w", err)
		return nil, chainErr
	}

	// conduit-31jg.11: honor FinishReason on the first round trip too — the
	// bd-1k3o length guard previously only ran after tool execution.
	response = ContinueLengthTruncated(ctx, provider, req, response, "initial")

	// conduit-1z6d: per-round-trip instrumentation — dead turns diagnosable
	// from the journal alone.
	log.Printf("[RoundTrip] phase=initial model=%q duration=%dms prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d",
		req.Model, latencyMs,
		response.Usage.PromptTokens, response.Usage.CompletionTokens,
		len(response.Content), len(response.ToolCalls))

	// Process response through agent system
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			chainErr = fmt.Errorf("failed to process response: %w", err)
			return nil, chainErr
		}

		// Update response based on agent processing
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = "" // Mark as silent
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	// Handle tool calls if present and execution engine is available
	if len(response.ToolCalls) > 0 && r.executionEngine != nil {
		// Send conversational progress for significant operations
		if onProgress != nil {
			msg := r.getConversationalProgress(response.ToolCalls)
			if msg != "" {
				onProgress(msg)
			}
		}
		convResponse, err := r.executionEngine.HandleToolCallFlow(ctx, provider, req, response)
		if err != nil {
			chainErr = err
			return nil, chainErr
		}
		// Post-process for silent response patterns (HEARTBEAT_OK, NO_REPLY)
		// This applies the same logic as ProcessResponse but after tool execution
		final := r.processSilentPatterns(convResponse)
		r.recordUsageToStore(session, final)
		return final, nil
	}

	// No tools called or no execution engine - return simple response
	simple := &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}
	r.recordUsageToStore(session, simple)
	return simple, nil
}

// recordUsageToStore persists token usage from a completed generation to the
// session store (bd-27hs). Called inside the per-session turn lock, after the
// chain has succeeded, so every path that generates a tool-using response —
// channel, direct, cron, wake, sub-agent, WS, HTTP — records usage uniformly
// without per-path glue. Best-effort: failures are logged, never fatal, and a
// nil store/session/usage is a no-op.
func (r *Router) recordUsageToStore(session *sessions.Session, response ConversationResponse) {
	if r.sessionStore == nil || session == nil || response == nil {
		return
	}
	usage := response.GetUsage()
	if usage == nil {
		return
	}
	// conduit-31jg.15: usage is the whole-turn sum; the last_* context keys
	// get the last round trip's prompt size (Context()), and the cache token
	// fields are recorded too.
	if err := r.sessionStore.RecordTurnUsage(session.Key, sessions.TurnUsage{
		ContextTokens:            usage.Context(),
		PromptTokens:             usage.PromptTokens,
		CompletionTokens:         usage.CompletionTokens,
		TotalTokens:              usage.TotalTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}); err != nil {
		log.Printf("[Router] token usage recording failed for session %s: %v", session.Key, err)
	}
}

// getConversationalProgress returns a friendly status message for tool calls
// Returns empty string for routine/quick operations to avoid spamming
func (r *Router) getConversationalProgress(toolCalls []ToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}

	// Check for significant operations worth mentioning
	for _, tc := range toolCalls {
		switch tc.Name {
		case "SessionsSpawn":
			return "Spinning up a sub-agent to help with this..."
		case "Bash":
			return "Running that command..."
		case "WebSearch":
			return "Searching the web..."
		case "WebFetch":
			return "Fetching that page..."
		case "MemorySearch":
			return "Checking my memory..."
		}
	}

	// For multiple tool calls, give a general update
	if len(toolCalls) > 2 {
		return "Working on a few things..."
	}

	// Skip progress for simple/quick operations like Read, Write, Glob
	return ""
}

// processSilentPatterns checks for HEARTBEAT_OK/NO_REPLY patterns in the response
// and returns an empty-content response if detected. Exact match after trimming,
// or contains-match only for short responses (≤40 chars) to tolerate minor LLM
// wrapping. Long responses that merely reference the token are not suppressed.
func (r *Router) processSilentPatterns(response ConversationResponse) ConversationResponse {
	upper := strings.ToUpper(strings.TrimSpace(response.GetContent()))

	silent := upper == constants.SilentReplyToken || upper == constants.HeartbeatOKToken
	if !silent && len(upper) <= 40 {
		silent = strings.Contains(upper, constants.SilentReplyToken) || strings.Contains(upper, constants.HeartbeatOKToken)
	}

	if silent {
		log.Printf("[Router] Silent response pattern detected (suppressing)")
		return &SimpleConversationResponse{
			Content: "",
			Usage:   response.GetUsage(),
			Steps:   response.GetSteps(),
		}
	}

	// Return original response if no silent patterns detected
	return response
}
