package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// resultRecorder lets Run return the result delivered to the sink without
// threading it through runLocked's many exits.
type resultRecorder struct {
	TurnSink
	res *TurnResult
}

func (rr *resultRecorder) Finish(ctx context.Context, res *TurnResult) {
	rr.res = res
	rr.TurnSink.Finish(ctx, res)
}

// userMsgStamp records the turn's user message ID on every result.
type userMsgStamp struct {
	TurnSink
	id string
}

func (u *userMsgStamp) Finish(ctx context.Context, res *TurnResult) {
	res.UserMessageID = u.id
	u.TurnSink.Finish(ctx, res)
}

// runLocked is the body of a turn; the caller holds the session turn lock.
// It returns an optional function to run after the lock is released.
func (r *TurnRunner) runLocked(ctx, parentCtx context.Context, req TurnRequest, sink TurnSink, ts *turnState) func() {
	session := req.Session
	key := session.Key

	// Refresh the session: while this turn was queued the previous one may
	// have updated model/cost/warning context.
	if fresh, err := r.sessions.GetSession(key); err == nil && fresh != nil {
		session = fresh
	}

	// 4a. Persist the user message inside the lock (conduit-31jg.22).
	userMsgID := req.PersistedUserMessageID
	if userMsgID == "" {
		storeText := req.StoreText
		if storeText == "" {
			storeText = req.Text
		}
		m, err := r.sessions.AddMessage(key, "user", storeText, req.StoreMetadata)
		if err != nil {
			r.logger.Error("turn: error saving user message", "session_key", key, "error", err)
			sink.Finish(parentCtx, &TurnResult{Err: fmt.Errorf("save user message: %w", err)})
			return nil
		}
		userMsgID = m.ID
	}
	r.mu.Lock()
	ts.snap.UserMessageID = userMsgID // conduit-31jg.88
	r.mu.Unlock()
	ctx = ai.WithCurrentUserMessageID(ctx, userMsgID)
	sink = &userMsgStamp{TurnSink: sink, id: userMsgID}

	// 5a. SPAR reflection injection (interactive turns only).
	messageForAI := req.Text
	isFarewell, isBudgetReflect := false, false
	if req.Origin != nil && r.hooks != nil && !req.SkipReflection {
		if r.hooks.IsFarewell(req.Text) {
			isFarewell = true
			if p := r.hooks.ReflectionPrompt(); p != "" {
				messageForAI = req.Text + "\n\n[System: " + p + "]"
				r.logger.Info("SPAR reflection: farewell detected, injecting reflection prompt",
					"session_key", key, "channel_id", req.ChannelID)
			}
		} else if session.Context["reflection_context_budget_triggered"] == "true" {
			if p := r.hooks.ReflectionPrompt(); p != "" {
				messageForAI = req.Text + "\n\n[System: " + p + "]"
				isBudgetReflect = true
				_ = r.sessions.SetSessionContextBatch(key, map[string]string{
					"reflection_context_budget_triggered": "",
				})
				r.logger.Info("SPAR reflection: context budget triggered, injecting reflection prompt",
					"session_key", key)
			}
		}
	}

	// Turn context.
	ctx = types.WithRequestContext(ctx, req.ChannelID, req.UserID, key)
	// conduit-31jg.88: tools cap their timeout once a drain begins.
	ctx = types.WithDrainDeadline(ctx, r.drainDeadline)
	if req.Origin != nil {
		o := *req.Origin
		o.SessionKey = key
		ctx = approval.WithInteractiveOrigin(ctx, o) // conduit-31jg.43
	} else {
		ctx = approval.WithNonInteractive(ctx, req.NonInteractiveSource) // conduit-31jg.43
	}
	if len(req.Attachments) > 0 {
		ctx = ai.WithAttachments(ctx, req.Attachments)
	}
	ctx = tools.WithToolEventCallback(ctx, func(ev tools.ToolEventInfo) { sink.ToolEvent(ctx, ev) })
	if req.Decorate != nil {
		ctx = req.Decorate(ctx)
	}
	// conduit-31jg.75: tool side calls (Image tool vision analysis) made
	// under this ctx are metered into this ledger and added to the turn cost.
	ctx, sideCalls := ai.WithSideCallLedger(ctx)

	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]

	// 4b. Generate.
	var conv ai.ConversationResponse
	var err error
	if onDelta := sink.Begin(ctx); onDelta != nil {
		conv, err = r.ai.GenerateResponseStreaming(ctx, session, messageForAI, providerOverride, modelOverride, onDelta)
	}
	if conv == nil && err == nil {
		conv, err = r.ai.GenerateResponseWithToolsAndProgress(ctx, session, messageForAI, providerOverride, modelOverride, sink.Progress)
	}

	res := &TurnResult{Response: conv, Model: modelOverride}
	if err != nil {
		if ctx.Err() == context.Canceled {
			r.logger.Debug("turn cancelled", "session_key", key)
			res.Cancelled = true
		}
		res.Err = err
		if !res.Cancelled {
			r.logger.Error("turn: error generating AI response", "session_key", key, "error", err)
		}
		sink.Finish(parentCtx, res)
		return nil
	}

	raw := conv.GetContent()
	res.Raw = raw
	res.Usage = conv.GetUsage()
	res.Empty = raw == ""
	res.Silent = !res.Empty && channels.IsSilentResponse(raw)
	content := raw

	// 5b. Path-independent accounting: context warning, session cost,
	// auto-compaction (conduit-31jg.49 — previously WS-only).
	if u := res.Usage; u != nil {
		// conduit-31jg.17: size the context window by the model actually
		// used — the override, else the configured default (never a
		// hardcoded literal).
		modelUsed := modelOverride
		if modelUsed == "" {
			modelUsed = r.ai.DefaultModel()
		}
		batch := map[string]string{}
		if !res.Silent && !res.Empty {
			if w := contextWarningIfNeeded(session, u.Context(), modelUsed); w.Text != "" { // conduit-31jg.15: last-call context, not the turn sum
				content += w.Text
				batch[w.Key] = "true"
				// SPAR: trigger reflection on next message when context budget >= 80%
				if w.Key == "context_warned_80" && r.hooks != nil && r.hooks.ReflectionEnabled() {
					batch["reflection_context_budget_triggered"] = "true"
				}
			}
		}
		// conduit-31jg.57: price through the gateway resolver (overrides,
		// provider-prefixed IDs, cache tokens). An unpriced model is counted
		// in session_unpriced_requests instead of silently adding $0.
		var priced bool
		res.RequestCost, priced = r.ai.TurnCost(providerOverride, modelOverride, *u)
		// conduit-31jg.75: plus side calls made by tools during the turn,
		// each already priced on its own provider + model by meterCall.
		if sc := sideCalls.Usage(); sc.PricedCalls+sc.UnpricedCalls > 0 {
			res.RequestCost += sc.CostUSD
			priced = priced && sc.UnpricedCalls == 0
		}
		prevCost, _ := strconv.ParseFloat(session.Context["session_total_cost"], 64)
		res.SessionCost = prevCost + res.RequestCost
		prevCount, _ := strconv.Atoi(session.Context["session_request_count"])
		batch["session_total_cost"] = fmt.Sprintf("%.6f", res.SessionCost)
		batch["session_request_count"] = strconv.Itoa(prevCount + 1)
		if !priced {
			prevUnpriced, _ := strconv.Atoi(session.Context["session_unpriced_requests"])
			batch["session_unpriced_requests"] = strconv.Itoa(prevUnpriced + 1)
		}
		_ = r.sessions.SetSessionContextBatch(key, batch)

		r.maybeCompact(session, u.Context(), modelUsed)
	}

	// 4c. Persist the reply inside the lock (conduit-31jg.22).
	if !res.Silent && !res.Empty {
		res.Content = content
		stored := content
		if req.SanitizeStored {
			stored = channels.SanitizeOutgoingText(stored)
		}
		if stored != "" {
			if _, aerr := r.sessions.AddMessage(key, "assistant", stored, nil); aerr != nil {
				r.logger.Error("turn: error saving AI message", "session_key", key, "error", aerr)
			}
		}
	} else if res.Silent {
		r.logger.Debug("silent response detected, suppressing", "session_key", key, "response_chars", len(raw))
	} else {
		r.logger.Warn("empty response content, not delivering", "session_key", key)
	}

	// 6. Deliver while still holding the lock.
	sink.Finish(parentCtx, res)

	if (isFarewell || isBudgetReflect) && res.Delivered() && r.hooks != nil {
		return func() {
			if updated, sErr := r.sessions.GetSession(key); sErr == nil {
				r.hooks.AfterReflection(parentCtx, updated)
			}
		}
	}
	return nil
}

// maybeCompact triggers asynchronous auto-compaction when the prompt is over
// the configured threshold. conduit-31jg.21 made Compact safe outside the
// turn lock (snapshotted IDs, one transaction, per-session in-flight guard).
func (r *TurnRunner) maybeCompact(session *sessions.Session, promptTokens int, model string) {
	if r.compactor == nil {
		return
	}
	// model is already resolved to the configured default by the caller;
	// "" means provider default (ai.DefaultContextWindow).
	if !r.compactor.ShouldCompact(promptTokens, model) {
		return
	}
	go func() {
		compactCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := r.compactor.Compact(compactCtx, session); errors.Is(err, ai.ErrCompactionInProgress) {
			r.logger.Info("auto-compact skipped: already in progress", "session_key", session.Key)
		} else if err != nil {
			r.logger.Warn("auto-compact failed", "session_key", session.Key, "error", err)
		}
	}()
}
