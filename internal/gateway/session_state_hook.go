package gateway

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/statehook"
)

// Session-end state hook (bd-8sib). Two paths, both gated on
// brain.session_state.enabled (default false):
//
//   - Path A (farewell, free): the state-distillation instruction rides the
//     farewell/budget reflection turn; after delivery the hook extracts the
//     state JSON from the final reply and writes it. Zero marginal LLM calls.
//   - Path B (idle sweeper, net): substantive main-type sessions that went
//     idle without a farewell get one bounded async distillation call.
//
// A failed state write never fails a turn or blocks a delivered reply:
// state is best-effort by design.

// stateHookDistillTimeout bounds the Path B one-shot distillation call
// (maybeCompact's 60s background-call pattern).
const stateHookDistillTimeout = 60 * time.Second

// stateHookTranscriptLines is the Path B transcript-tail size (role: content
// lines, newest last) fed to the distillation prompt.
const stateHookTranscriptLines = 40

// sessionStateHook is one gateway's state-file writer. It is built lazily
// per target path so tests (and multi-workspace deployments) can point it
// at temp files; the mutex serializes Path A and Path B writers within the
// process (statehook.Writer adds its own per-writer lock).
type sessionStateHook struct {
	path   string
	writer *statehook.Writer
	model  string // written_by stamp (the model that produced the content)
}

var sessionStateHookPaths sync.Map // target path -> *sessionStateHook

// sessionStateEnabled reports whether the hook is armed.
func (g *Gateway) sessionStateEnabled() bool {
	return g != nil && g.config != nil && g.config.Brain.SessionState.Enabled
}

// sessionStateFilePath resolves the hook's state-file path: relative to the
// workspace context dir; session_state.file > workspace.files.state_file >
// DefaultWorkspaceStateFile. Empty when no workspace dir is known.
func (g *Gateway) sessionStateFilePath() string {
	if g == nil {
		return ""
	}
	sc := g.config.Brain.SessionState
	rel := sc.FileEffectiveFor(g.config.Workspace.Files)
	base := ""
	if g.workspaceContext != nil {
		base = g.workspaceContext.GetWorkspaceDir()
	}
	if base == "" {
		base = g.config.Workspace.ContextDir
	}
	if base == "" {
		return rel // no workspace dir: CWD-relative
	}
	return filepath.Join(base, rel)
}

// stateHookFor returns the hook for a session, or nil when the hook is
// disabled or the session is not a main interactive one (cron/heartbeat/
// sub-agent keys are excluded — their state writes would be noise).
func (g *Gateway) stateHookFor(sessionKey string) *sessionStateHook {
	if !g.sessionStateEnabled() || !statehook.IsValidSessionKey(sessionKey) {
		return nil
	}
	path := g.sessionStateFilePath()
	if path == "" {
		return nil
	}
	if v, ok := sessionStateHookPaths.Load(path); ok {
		return v.(*sessionStateHook)
	}
	h := &sessionStateHook{path: path, writer: statehook.NewWriter(path)}
	actual, _ := sessionStateHookPaths.LoadOrStore(path, h)
	return actual.(*sessionStateHook)
}

// write validates, caps and atomically writes the state, then logs the
// receipt (state.py's RECEIPT parity in log form). softCapTokens <= 0 means
// the configured default.
func (h *sessionStateHook) write(log *slog.Logger, st *statehook.JulesState, softCapTokens int) error {
	if h == nil || h.writer == nil {
		return nil
	}
	st.ApplyCap(softCapTokens)
	st.WrittenBy = h.model
	if st.WrittenBy == "" {
		st.WrittenBy = "gateway-session-hook"
	}
	n, err := h.writer.Write(st)
	if err != nil {
		return err
	}
	if log != nil {
		log.Info("session state hook: wrote state file",
			"path", h.path, "bytes", n, "truncated", st.Truncated)
	}
	return nil
}

// maybeWriteSessionState (Path A) extracts the state JSON from a delivered
// farewell/budget-reflection reply and writes it. Best-effort: failures are
// logged and swallowed — they must never surface in (or delay) the reply.
func (g *Gateway) maybeWriteSessionState(ctx context.Context, sessionKey, reply, model string) {
	h := g.stateHookFor(sessionKey)
	if h == nil {
		return
	}
	st, err := statehook.ExtractStateJSON(reply)
	if err != nil {
		g.logger.Debug("session state hook: no state JSON in farewell reply",
			"session_key", sessionKey, "error", err)
		return
	}
	h.model = model
	if err := h.write(g.logger, st, g.config.Brain.SessionState.SoftCapTokensEffective()); err != nil {
		g.logger.Warn("session state hook: write failed", "session_key", sessionKey, "error", err)
	}
}

// distillIdleSessionStates (Path B) is the idle-sweeper net: one pass over
// idle, substantive, not-yet-distilled main sessions; for each, one bounded
// one-shot distillation call whose reply is extracted and written. It runs
// alongside reflectOnIdleSessions on the same cadence. Returns after one
// pass (the sweeper loop calls it per tick).
func (g *Gateway) distillIdleSessionStates(ctx context.Context, minMessages int, idleTimeout time.Duration) error {
	if !g.sessionStateEnabled() || !g.config.Brain.SessionState.IdleDistill {
		return nil
	}
	if g.ai == nil || g.sessions == nil {
		return nil
	}
	if minMessages <= 0 {
		minMessages = 5
	}
	cutoff := time.Now().Add(-idleTimeout)
	keys, err := g.sessions.GetIdleSessions(cutoff, minMessages)
	if err != nil {
		g.logger.Warn("session state hook: failed to query idle sessions", "error", err)
		return err
	}
	distilled := make(map[string]struct{})
	var wg sync.WaitGroup
	for _, key := range keys {
		if _, done := distilled[key]; done {
			continue
		}
		if !statehook.IsValidSessionKey(key) {
			continue
		}
		distilled[key] = struct{}{}
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			g.distillSessionState(ctx, key)
		}(key)
	}
	wg.Wait()
	return nil
}

// distillSessionState runs the bounded Path B call for one idle session.
// Substantive check (>5 messages), fresh-write rule (skip when the file's
// written_at is newer than the session's start), 60s timeout, logged
// failure — never blocks anything.
func (g *Gateway) distillSessionState(ctx context.Context, sessionKey string) {
	h := g.stateHookFor(sessionKey)
	if h == nil {
		return
	}
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		g.logger.Debug("session state hook: could not look up idle session", "session_key", sessionKey, "error", err)
		return
	}
	if session.MessageCount <= 5 { // substantive threshold (gateway_reflect_wiring parity)
		return
	}
	if g.stateFileFresh(h.path, session.CreatedAt) {
		g.logger.Debug("session state hook: state file fresher than session, skipping",
			"session_key", sessionKey)
		return
	}

	msgs, err := g.sessions.GetMessages(sessionKey, stateHookTranscriptLines)
	if err != nil {
		g.logger.Warn("session state hook: transcript read failed", "session_key", sessionKey, "error", err)
		return
	}
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, m.Role+": "+m.Content)
	}

	callCtx, cancel := context.WithTimeout(ctx, stateHookDistillTimeout)
	defer cancel()
	provider := g.ai.DefaultProviderName()
	model := g.ai.DefaultModel()
	h.model = model
	resp, err := g.ai.GenerateSideCall(callCtx, provider, &ai.GenerateRequest{
		Messages: statehook.BuildDistillMessages(lines),
		Model:    model,
	})
	if err != nil {
		g.logger.Warn("session state hook: distill call failed", "session_key", sessionKey, "error", err)
		return
	}
	st, err := statehook.ExtractStateJSON(resp.Content)
	if err != nil {
		g.logger.Warn("session state hook: distill reply had no state JSON", "session_key", sessionKey, "error", err)
		return
	}
	if err := h.write(g.logger, st, g.config.Brain.SessionState.SoftCapTokensEffective()); err != nil {
		g.logger.Warn("session state hook: write failed", "session_key", sessionKey, "error", err)
	}
}

// stateFileFresh reports whether the on-disk state's written_at is newer
// than the session's start (don't rewrite for a trivial session after a
// fresh write — memo §3.1). Unparseable/missing files are never fresh.
func (g *Gateway) stateFileFresh(path string, sessionStart time.Time) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	st, err := statehook.ParseJulesState(raw)
	if err != nil {
		return false
	}
	writtenAt, err := st.WrittenAtTime()
	if err != nil {
		return false
	}
	return writtenAt.After(sessionStart)
}

// sessionStateFarewellAddendum returns the Path A instruction to append to
// the farewell reflection prompt ("" when the hook is off).
func (g *Gateway) sessionStateFarewellAddendum() string {
	if !g.sessionStateEnabled() {
		return ""
	}
	return statehook.DistillPrompt()
}
