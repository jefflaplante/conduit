package ai

import (
	"context"
	"sync"
	"sync/atomic"

	"conduit/internal/sessions"
)

// turnSem returns the capacity-1 semaphore guarding sessionKey's turns.
func (r *Router) turnSem(sessionKey string) chan struct{} {
	v, _ := r.turnLocks.LoadOrStore(sessionKey, make(chan struct{}, 1))
	return v.(chan struct{})
}

// lockSession acquires the per-session turn lock and returns an unlock function.
// Empty session keys are a no-op (returns a no-op unlock).
func (r *Router) lockSession(sessionKey string) func() {
	if sessionKey == "" {
		return func() {}
	}
	sem := r.turnSem(sessionKey)
	sem <- struct{}{}
	return func() { <-sem }
}

// turnLease marks a context as running inside a turn whose per-session lock
// was taken by AcquireTurn (conduit-31jg.35).
type turnLease struct {
	router *Router
	key    string
	held   atomic.Bool
}

type turnLeaseKey struct{}

// AcquireTurn takes the per-session turn lock on behalf of a caller that owns
// the whole turn (the gateway TurnRunner, conduit-31jg.35), which persists the
// user message and the reply while holding it so transcript order matches
// turn order (conduit-31jg.22).
//
// It blocks until the lock is free or ctx is done, so a queued turn cancelled
// by /stop stops waiting (conduit-31jg.23). The returned context carries a
// lease: GenerateResponseWithTools*/GenerateResponseStreaming called with
// it, or with a context derived from it, run UNLOCKED for that session instead of deadlocking on the lock the
// caller already holds. release is idempotent; after it, the lease no longer
// bypasses the lock, so goroutines that outlive the turn lock normally.
func (r *Router) AcquireTurn(ctx context.Context, sessionKey string) (context.Context, func(), error) {
	if sessionKey == "" {
		return ctx, func() {}, nil
	}
	sem := r.turnSem(sessionKey)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}
	if err := ctx.Err(); err != nil { // select picks randomly when both are ready
		<-sem
		return ctx, func() {}, err
	}
	lease := &turnLease{router: r, key: sessionKey}
	lease.held.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.held.Store(false)
			<-sem
		})
	}
	return context.WithValue(ctx, turnLeaseKey{}, lease), release, nil
}

// lockSessionCtx is lockSession unless ctx carries a live AcquireTurn lease
// for this router and session: the caller then already holds the lock and
// this is a no-op (conduit-31jg.35).
func (r *Router) lockSessionCtx(ctx context.Context, sessionKey string) func() {
	if sessionKey == "" {
		return func() {}
	}
	if ctx == nil { // some legacy callers/tests pass a nil context
		return r.lockSession(sessionKey)
	}
	if l, ok := ctx.Value(turnLeaseKey{}).(*turnLease); ok && l.router == r && l.key == sessionKey && l.held.Load() {
		return func() {}
	}
	return r.lockSession(sessionKey)
}

func sessionKeyOf(s *sessions.Session) string {
	if s == nil {
		return ""
	}
	return s.Key
}
