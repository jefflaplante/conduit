package ai

import (
	"context"
	"time"
)

// Deadline budgeting for recovery attempts (conduit-10ip, conduit-1w48).
//
// A turn's retries share ONE deadline: sub-agent chains run under
// context.WithTimeout(gatewayCtx, timeoutSeconds) (gateway/subagents.go,
// default 300s), heartbeats under their own. Before this, a same-model retry
// ran on the full remaining budget; when it hung until the chain deadline,
// the cross-model failover that followed started on a dead context and died
// in ~2ms (journal Sep 25 17:11:34 / Sep 26 08:56:14: "retry completed in
// 1m57s ... failover completed in 2ms: context deadline exceeded").
//
// Design: when a later failover stage exists, the same-model retry runs on a
// child context whose deadline stops failoverReserve short of the parent's,
// so the failover is guaranteed at least that slice. When even that leaves
// less than minRetry for the retry, the retry is skipped outright — the
// failover (a different backend) has the better chance. Attempts are never
// given MORE time than the parent deadline: timeoutSeconds is a contract
// with the caller, and detaching (context.WithoutCancel) would also let a
// retry outlive gateway shutdown. Without a deadline nothing changes.
//
// Package variables so tests can shrink them.
var (
	// recoveryMinAttempt is the least ctx time worth starting a provider
	// call with — mirrors defaultAnthropicRetryPolicy.minAttemptBudget.
	recoveryMinAttempt = 10 * time.Second
	// recoveryFailoverReserve is the slice kept back for a failover stage.
	// Healthy fallback calls (Anthropic, z.ai glm-5.3) complete well
	// inside a minute; the sick-backend stalls this guards against run
	// 2-4.5 minutes, so a larger reserve would mostly shorten useful retries.
	recoveryFailoverReserve = 60 * time.Second
	// recoveryMinRetry is the least time a same-model retry must get after
	// the reserve; below it the retry is skipped in favour of failover.
	recoveryMinRetry = 30 * time.Second
)

// timeLeft returns the time until ctx's deadline; ok=false when ctx has none.
func timeLeft(ctx context.Context) (time.Duration, bool) {
	d, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	return time.Until(d), true
}

// hasAttemptBudget reports whether ctx is alive and has at least
// recoveryMinAttempt left (always true without a deadline).
func hasAttemptBudget(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	left, ok := timeLeft(ctx)
	return !ok || left >= recoveryMinAttempt
}

// retryBudget returns the context for a same-route retry that must leave
// reserve of ctx's deadline for a later stage (reserve 0 = no later stage).
// ok=false means skip the retry: ctx is done, or the retry would get less
// than recoveryMinRetry (with a reserve) / recoveryMinAttempt (without).
// The returned cancel must always be called.
func retryBudget(ctx context.Context, reserve time.Duration) (context.Context, context.CancelFunc, bool) {
	noop := func() {}
	if ctx.Err() != nil {
		return ctx, noop, false
	}
	left, hasDeadline := timeLeft(ctx)
	if !hasDeadline {
		return ctx, noop, true
	}
	if reserve <= 0 {
		return ctx, noop, left >= recoveryMinAttempt
	}
	if left-reserve < recoveryMinRetry {
		return ctx, noop, false
	}
	deadline, _ := ctx.Deadline()
	rctx, cancel := context.WithDeadline(ctx, deadline.Add(-reserve))
	return rctx, cancel, true
}
