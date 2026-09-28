package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/sessions"
)

// GenerateResponseStreaming generates a streaming AI response.
// The onDelta callback is called with each text delta, and done=true when complete.
// Any provider implementing StreamingProvider will stream; others fall back to non-streaming.
// Acquires the per-session turn lock for the duration of the chain.
func (r *Router) GenerateResponseStreaming(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onDelta StreamCallback) (ConversationResponse, error) {
	unlock := r.lockSessionCtx(ctx, sessionKeyOf(session)) // conduit-31jg.35: no-op under an AcquireTurn lease
	defer unlock()

	chainStart := time.Now()
	log.Printf("[Router] >>> LLM CHAIN START (streaming)")
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
				log.Printf("[Router] Streaming: overriding provider %q → %q (from model %q)", providerName, resolved, modelOverride)
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
	log.Printf("[Router] Streaming: provider=%q model=%q context_window=%d", providerName, modelOverride, contextWindow)

	// Check if the provider supports streaming
	streamingProvider, canStream := provider.(StreamingProvider)
	if !canStream {
		// Fall back to non-streaming. We already hold the per-session turn lock,
		// so dispatch to the unlocked worker to avoid deadlock.
		return r.generateResponseWithToolsLocked(ctx, session, userMessage, providerName, modelOverride, nil)
	}
	ctx = r.withCallTurn(ctx, session) // conduit-2lzv

	// Build system prompt
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			chainErr = fmt.Errorf("failed to build system prompt: %w", err)
			return nil, chainErr
		}
		systemBlocks = blocks
	}

	// Build chat messages
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		chainErr = fmt.Errorf("failed to build messages: %w", err)
		return nil, chainErr
	}

	// Get tools
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
	// Call streaming API via the provider-agnostic interface.
	// conduit-31jg.18: recovery attempts carry (provider, model) as a pair,
	// and the stream tracker mutes retry deltas once text has reached the
	// client, so a replayed generation never duplicates streamed text.
	response, served, _, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: streamingProvider, model: req.Model},
		req, recoveryOpts{phase: "streaming", quotaFallbackNeedsModel: true, stream: newStreamTracker(onDelta)})
	if err != nil {
		// conduit-31jg.64: each failed attempt was recorded by the metering
		// hook (conduit-31jg.12 streaming parity preserved there).
		chainErr = err
		return nil, chainErr
	}
	// Keep the tool-loop continuation on the provider that actually served
	// the response (bd-27ud), re-trimming every later round (conduit-31jg.18(b)).
	provider = r.guardedProvider(served)

	// conduit-14qr: the streaming path never went through the empty guard —
	// only the non-streaming chain call sites wrap GuardEmptyResponse — so
	// raw-empty provider responses (z.ai HTTP-200 empty payloads, 2026-09-14
	// RCA) flowed straight to delivery and died as silent WARN suppressions
	// (5 dead deliveries on Sep 14 alone). Run the same retry → cross-model
	// failover → visible fallback machinery as the non-streaming path
	// (conduit-18vj/1z0g). No-op for non-empty responses, errors, tool-call
	// responses, and deliberate silence (NO_REPLY/HEARTBEAT_OK arrive here
	// non-empty and are blanked later by silent-pattern processing).
	// NOTE: deltas already streamed to the client before an empty FINAL are
	// unrecoverable — a recovered retry/failover renders as a fresh message.
	response, err = GuardEmptyResponse(ctx, provider, req, response, err, "streaming")
	if err != nil {
		chainErr = err
		return nil, chainErr
	}

	// conduit-31jg.11: length-truncated first reply → auto-continue
	// (non-streaming continuation; the gateway replaces the streamed text
	// with the final content).
	response = ContinueLengthTruncated(ctx, provider, req, response, "streaming")

	// Process response through agent system (same as non-streaming path)
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			chainErr = fmt.Errorf("failed to process streaming response: %w", err)
			return nil, chainErr
		}
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = ""
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	// Check if tool calls were detected during streaming
	if len(response.ToolCalls) > 0 && r.executionEngine != nil {
		convResponse, err := r.executionEngine.HandleToolCallFlow(ctx, provider, req, response)
		if err != nil {
			chainErr = err
			return nil, chainErr
		}
		// Post-process for silent response patterns (HEARTBEAT_OK, NO_REPLY)
		final := r.processSilentPatterns(convResponse)
		r.recordUsageToStore(session, final)
		return final, nil
	}

	// No tool calls - return simple streaming response
	simple := &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}
	r.recordUsageToStore(session, simple)
	return simple, nil
}
