package tools

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
)

// turnState is the tool loop's per-turn state (conduit-31jg.37). One is built
// per HandleToolCallFlow call and advanced round by round; nothing in it
// outlives the turn. Before this bead every round was a recursive call that
// re-copied the whole history.
type turnState struct {
	provider ai.Provider
	// model, tools and maxTokens are copied into every round's request (the
	// loop never forwards any other GenerateRequest field).
	model     string
	tools     []ai.Tool
	maxTokens int

	// history is the turn's one conversation slice, appended to in place.
	// Every request gets a capacity-capped view of it (history[:n:n]), so
	// neither the loop's later appends nor anyone appending to a request's
	// Messages can write into a slot an earlier request still shows.
	history []ai.ChatMessage
	// resp is the reply whose tool calls the current round executes.
	resp       *ai.GenerateResponse
	depth      int
	chainStart time.Time
	budget     *turnBudget // trackers (budget.chain), usage, extensions, refocus one-shot
}

// request builds the round's provider request over the current history.
func (ts *turnState) request() *ai.GenerateRequest {
	n := len(ts.history)
	return &ai.GenerateRequest{
		Messages:  ts.history[:n:n],
		Model:     ts.model,
		Tools:     ts.tools,
		MaxTokens: ts.maxTokens,
	}
}

// advance carries the round's request history into the next round and makes
// resp the reply to execute. guidanceAt is the index of this round's
// ephemeral guidance message (-1 when none): it existed for this round trip
// only and is dropped (conduit-8ba7, conduit-31jg.13). The request itself is
// never modified: its pointer may already be recorded by mocks/telemetry.
func (ts *turnState) advance(req *ai.GenerateRequest, guidanceAt int, resp *ai.GenerateResponse) {
	msgs := req.Messages
	switch {
	case guidanceAt >= 0:
		// The capped prefix makes append copy, so the guidance slot the
		// provider saw is never overwritten.
		ts.history = append(msgs[:guidanceAt:guidanceAt], msgs[guidanceAt+1:]...)
	case len(msgs) != len(ts.history):
		// Auto-continue appended to req.Messages (a fresh array). Cap it:
		// a view the provider saw may extend past its current length.
		ts.history = msgs[:len(msgs):len(msgs)]
	default:
		// Same contents as ts.history; keep its spare capacity.
	}
	ts.resp = resp
	ts.depth++
}

// HandleToolCallFlow runs the tool loop for one turn: execute the reply's
// tool calls, send the results back, and repeat until the model answers
// without tool calls, the chain hits maxChains, or the turn window closes.
func (e *ExecutionEngine) HandleToolCallFlow(
	ctx context.Context,
	provider ai.Provider,
	initialReq *ai.GenerateRequest,
	initialResp *ai.GenerateResponse,
) (*ConversationResponse, error) {
	log.Printf("[ExecutionEngine] HandleToolCallFlow called with %d tool calls", len(initialResp.ToolCalls))
	for i, tc := range initialResp.ToolCalls {
		log.Printf("[ExecutionEngine] Tool call %d: %s", i, tc.Name)
	}
	// conduit-31jg.13: fresh trackers per turn, on the turn budget and on
	// ctx into executeSingle.
	tb := newTurnBudget(time.Now())
	tb.chain = e.newChainState(ctx)
	// conduit-31jg.15: the router's first round trip (with its own guard
	// retries / auto-continues folded in) opens the turn's usage.
	tb.usage.Add(initialResp.Usage)
	e.recordLLMResponse(provider, initialReq.Model, "initial", 0, initialResp, nil, 0) // conduit-3kgo

	// The caller's slice is copied once, with room to grow, so appends
	// never write into the caller's backing array.
	history := make([]ai.ChatMessage, len(initialReq.Messages), len(initialReq.Messages)+16)
	copy(history, initialReq.Messages)
	ts := &turnState{
		provider:   provider,
		model:      initialReq.Model,
		tools:      initialReq.Tools,
		maxTokens:  initialReq.MaxTokens,
		history:    history,
		resp:       initialResp,
		chainStart: time.Now(),
		budget:     tb,
	}
	return e.runToolLoop(withChainState(ctx, tb.chain), ts)
}

// runToolLoop executes rounds until the chain ends. conduit-31jg.37.
func (e *ExecutionEngine) runToolLoop(ctx context.Context, ts *turnState) (*ConversationResponse, error) {
	tb := ts.budget
	for {
		depth := ts.depth

		// conduit-1z6d + adaptive extension: enforce the turn window.
		// Productive coding chains get bounded extensions (each announced);
		// everything else stops with a user-visible timeout — never a silent
		// end.
		if stop := e.checkTurnWindow(ctx, ts.chainStart, tb, depth); stop != nil {
			if stop.Usage == nil {
				stop.Usage = tb.usageSnapshot() // conduit-31jg.15
			}
			return stop, nil
		}

		// Prevent infinite tool chains
		if depth >= e.maxChains {
			return e.chainLimitResponse(ts), nil
		}

		refocusMessage := e.refocusMessage(ts)

		// This round: the reply's tool calls, then their results.
		ts.history = append(ts.history, ai.ChatMessage{
			Role:      "assistant",
			Content:   ts.resp.Content,
			ToolCalls: ts.resp.ToolCalls,
		})
		toolResults, err := e.ExecuteToolCalls(ctx, ts.resp.ToolCalls)
		if err != nil {
			return nil, fmt.Errorf("tool execution failed: %w", err)
		}

		// Turn budget: record this round's activity for extension eligibility.
		anySuccess := false
		for _, r := range toolResults {
			if r != nil && r.Error == nil && r.Result != nil && r.Result.Success {
				anySuccess = true
				break
			}
		}
		tb.markRound(ts.resp.ToolCalls, anySuccess, time.Now())

		for _, result := range toolResults {
			ts.history = append(ts.history, ai.ChatMessage{
				Role:       "tool",
				Content:    e.formatToolResultForAI(result),
				ToolCallID: result.ToolCall.ID,
				// conduit-31jg.45: tell the model the call failed (tool_result.is_error).
				IsError: result.Error != nil || (result.Result != nil && !result.Result.Success),
			})
		}

		// conduit-31jg.13: failure-pivot and circular-pattern guidance, once
		// per trigger, from this turn's trackers only. Sent as a USER-role
		// message after the tool results — not system-role — so providers
		// that hoist system messages (anthropic.go, openai.go) don't rewrite
		// the system prefix and bust the prompt cache. The Anthropic
		// converter puts it in the same user message as the tool_results,
		// after them (conduit-31jg.45). conduit-31jg.14: the one-per-chain
		// conduit-8ba7 progress reminder rides in this same message. It is
		// dropped again before the next round (turnState.advance).
		guidanceAt := -1
		if guidance := tb.chain.takeGuidance(refocusMessage); guidance != "" {
			log.Printf("[ExecutionEngine] Injecting tool-loop guidance at depth %d (conduit-31jg.13)", depth)
			guidanceAt = len(ts.history)
			ts.history = append(ts.history, ai.ChatMessage{Role: "user", Content: guidance, Injected: true})
		}

		req := ts.request()
		resp, err := e.roundTrip(ctx, ts.provider, req, depth, tb)
		if err != nil {
			return nil, err
		}

		if len(resp.ToolCalls) == 0 {
			// conduit-31jg.15: usage is the whole turn, not the last two calls.
			return &ConversationResponse{
				Content:     resp.Content,
				Usage:       tb.usageSnapshot(),
				Steps:       2 + depth, // Initial + final + any chained steps
				ToolResults: toolResults,
				ChainDepth:  depth,
			}, nil
		}
		ts.advance(req, guidanceAt, resp)
	}
}

// roundTrip sends one round's request: the provider call, the EmptyGuard
// retry/failover and the length auto-continue. Every billed call's usage is
// added to tb exactly once (GuardEmptyResponse and ContinueLengthTruncated
// each return the sum of the calls they made).
func (e *ExecutionEngine) roundTrip(ctx context.Context, provider ai.Provider, req *ai.GenerateRequest, depth int, tb *turnBudget) (*ai.GenerateResponse, error) {
	stopThinking := startThinkingIndicator(ctx, depth)
	e.recordLLMRequest(provider, req, depth) // conduit-3kgo
	rtStart := time.Now()
	resp, err := provider.GenerateResponse(ctx, req)
	stopThinking()
	if err != nil {
		e.recordLLMResponse(provider, req.Model, "post_tools", depth, nil, err, time.Since(rtStart))
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// conduit-18vj: raw-empty round trips after tool execution were the proven
	// dead-turn mechanism (2026-09-03) — retry once, then a visible fallback.
	label := fmt.Sprintf("depth%d", depth)
	resp, err = ai.GuardEmptyResponse(ctx, provider, req, resp, err, label)
	if err != nil {
		e.recordLLMResponse(provider, req.Model, "post_tools", depth, nil, err, time.Since(rtStart))
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// conduit-1z6d: per-round-trip instrumentation — dead turns diagnosable
	// from the journal alone.
	log.Printf("[RoundTrip] phase=post-tools depth=%d model=%q duration=%s prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d",
		depth, req.Model, time.Since(rtStart).Round(time.Millisecond),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		len(resp.Content), len(resp.ToolCalls))

	// bd-1k3o / conduit-31jg.51: length-truncation guard, shared with the
	// router's first round trip. A max_tokens-severed fragment is continued
	// (at most twice) instead of delivered as the answer; req.Messages grows
	// by each fragment + "continue" pair. When the continuation ends in tool
	// calls, Content stays the last fragment only — the earlier ones are
	// already in req.Messages, which becomes the next round's history. The
	// helper folds every continuation's usage into resp.Usage.
	resp = ai.ContinueLengthTruncated(ctx, provider, req, resp, label)
	tb.usage.Add(resp.Usage)                                                                      // conduit-31jg.15: this round's calls, exactly once
	e.recordLLMResponse(provider, req.Model, "post_tools", depth, resp, nil, time.Since(rtStart)) // conduit-3kgo
	return resp, nil
}

// chainLimitResponse is the reply when the chain reaches maxChains.
func (e *ExecutionEngine) chainLimitResponse(ts *turnState) *ConversationResponse {
	depth := ts.depth
	log.Printf("Tool chain depth limit reached: %d/%d", depth, e.maxChains)
	limitMessage := fmt.Sprintf(
		"%s\n\n**Tool chain limit reached (%d steps).** "+
			"I've completed %d tool operations but reached the maximum allowed chain length. "+
			"This prevents runaway tool usage while still allowing complex workflows. "+
			"If you need to continue, you can:\n"+
			"- Ask me to pick up where I left off with a more focused approach\n"+
			"- Break the task into smaller steps\n"+
			"- Increase the `max_tool_chains` setting in config.json if this limit is too restrictive",
		ts.resp.Content, e.maxChains, depth,
	)
	return &ConversationResponse{
		Content:    limitMessage,
		Usage:      ts.budget.usageSnapshot(), // conduit-31jg.15
		Steps:      depth + 1,
		ChainDepth: depth,
	}
}

// refocusDepthThreshold is the depth of the one mid-chain progress reminder.
const refocusDepthThreshold = 20

// refocusMessage returns the conduit-8ba7 mid-chain progress reminder for
// this round, or "". The old every-10-depth verbatim goal reminder is gone;
// deep chains get exactly one progress-aware user-role guidance message at
// the first depth >= 20, and depth milestones 30/40/50 emit chain_depth
// telemetry (log only, no injection).
func (e *ExecutionEngine) refocusMessage(ts *turnState) string {
	depth, tb := ts.depth, ts.budget
	var msg string
	if depth >= refocusDepthThreshold && !tb.injected {
		if originalGoal := e.extractOriginalGoal(ts.history); originalGoal != "" {
			msg = fmt.Sprintf("%s%d of max %d. Original request: %s",
				progressReminderMarker, depth, e.maxChains, originalGoal)
			tb.injected = true
			log.Printf("[ExecutionEngine] operation=refocus_inject depth=%d max=%d goal=%q (conduit-8ba7)", depth, e.maxChains, originalGoal)
		}
	}
	switch depth {
	case 30, 40, 50:
		log.Printf("[ExecutionEngine] operation=chain_depth milestone=%d max=%d (conduit-8ba7)", depth, e.maxChains)
	}
	return msg
}

// maybeSendExtensionNotice delivers the extension announcement via the
// StatusUpdate tool so the user sees the turn is still alive. Best-effort:
// a failed notice never aborts the chain.
func (e *ExecutionEngine) maybeSendExtensionNotice(ctx context.Context, tb *turnBudget) {
	msg := extensionStatusMessage(tb, time.Now())
	if e.registry == nil {
		log.Printf("[TurnBudget] no registry; extension notice not sent: %s", msg)
		return
	}
	if _, err := e.registry.ExecuteTool(ctx, "StatusUpdate", map[string]interface{}{"message": msg}); err != nil {
		log.Printf("[TurnBudget] extension notice send failed (continuing): %v", err)
	}
}

// checkTurnWindow enforces the adaptive cap: grant an extension when eligible,
// otherwise terminate with the user-visible timeout message. Returns non-nil
// ConversationResponse when the chain must stop.
func (e *ExecutionEngine) checkTurnWindow(ctx context.Context, chainStart time.Time, tb *turnBudget, depth int) *ConversationResponse {
	now := time.Now()
	if _, pastWindow := watchdogTimeoutResponse(chainStart, tb.extensions); pastWindow {
		if ok, reason := tb.extensionEligible(now); ok {
			tb.extensions++
			log.Printf("[TurnBudget] extension %d/%d granted at depth %d (elapsed %s, %s)",
				tb.extensions, turnMaxExtensions, depth, now.Sub(chainStart).Round(time.Second), reason)
			e.maybeSendExtensionNotice(ctx, tb)
		} else {
			log.Printf("[TurnBudget] chain cap exceeded at depth %d (elapsed %s, extensions used %d/%d, %s) — aborting with visible timeout (conduit-1z6d)",
				depth, now.Sub(chainStart).Round(time.Second), tb.extensions, turnMaxExtensions, reason)
			return &ConversationResponse{
				Content:    watchdogTerminalMessage(reason),
				Steps:      depth + 1,
				ChainDepth: depth,
			}
		}
	}
	return nil
}

// extractOriginalGoal finds the original user goal from the message history.
// It looks for the last user message in the conversation, which typically
// contains the original request that initiated the tool chain. Messages the
// gateway injected mid-turn (length auto-continue "continue", tool-loop
// guidance) are skipped: they are not the user's request (conduit-31jg.87).
func (e *ExecutionEngine) extractOriginalGoal(messages []ai.ChatMessage) string {
	// Search backwards to find the most recent user-authored message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" && !messages[i].Injected {
			goal := messages[i].Content
			// Truncate long goals to keep the reminder concise
			const maxGoalLen = 200
			if len(goal) > maxGoalLen {
				goal = goal[:maxGoalLen] + "..."
			}
			return goal
		}
	}
	return ""
}

// progressReminderMarker is the stable prefix of the conduit-8ba7 mid-chain
// progress reminder (tests match on it).
const progressReminderMarker = "Turn progress: depth "
