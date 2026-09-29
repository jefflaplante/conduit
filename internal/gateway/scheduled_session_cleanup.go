package gateway

import (
	"context"
	"strings"
	"time"

	"conduit/internal/agent"
	"conduit/internal/heartbeat"
	"conduit/internal/sessions"
)

var _ heartbeat.SessionFinalizer = (*turnAIExecutor)(nil)

// End-of-run cleanup of scheduler sessions (conduit-385r).
//
// Every cron prompt and heartbeat run gets a fresh session. Since
// conduit-31jg.66 the run's turn goes through the TurnRunner, which stores
// the prompt as a user row and the reply only when there is one; a run whose
// turn was silent (HEARTBEAT_OK / NO_REPLY), empty, failed or stopped leaves
// a session holding nothing but its prompt. Before .66 such runs left
// sessions with no messages at all. Either way ~200 rows a day (plus a ~6 KB
// heartbeat prompt each since .66) accumulated until `conduit maintenance`
// pruned them.
//
// So the run owner releases its session when it is done: the session is
// deleted (sessions.Store.DeleteSessionIfPromptOnly) unless it
//
//   - is not a cron_/heartbeat_ session (never an interactive one);
//   - still has a turn running or queued (e.g. a wake);
//   - spawned sub-agents: a sub-agent reports back into its parent session
//     through a wake, minutes after the parent's run ended, so the parent
//     stays (and the wake's messages make it a kept session anyway);
//   - has pending approvals (none are expected: scheduler turns are
//     non-interactive and approval requests fail closed);
//   - holds anything besides plain prompts: a stored reply, an inter-session
//     delivery or wake, a compaction summary — checked inside the delete
//     transaction.
//
// Kept sessions are pruned by `conduit maintenance` after the retention
// window, as before. A failed cleanup is logged and never fails the job or
// touches the scheduler's failure streak.

// scheduledSessionPrefixes are the key prefixes of scheduler-run sessions
// the cleanup may delete. Heartbeat keys are "heartbeat_<nanos>_heartbeat_<id>"
// (heartbeat.ExecutorConfig.SessionPrefix is always "heartbeat").
var scheduledSessionPrefixes = []string{agent.CronSessionKeyPrefix, "heartbeat_"}

// scheduledSessionCleanupTimeout bounds the delete; it runs on its own
// context so a job cancelled by /stop or the shutdown drain still cleans up.
const scheduledSessionCleanupTimeout = 30 * time.Second

func isScheduledSessionKey(key string) bool {
	for _, p := range scheduledSessionPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// releaseScheduledSession deletes the session a finished cron/heartbeat run
// used when the run left nothing worth keeping in it (see above). source is
// "cron" or "heartbeat", for logs.
func (g *Gateway) releaseScheduledSession(key, source string) {
	if g == nil || g.sessions == nil || key == "" {
		return
	}
	log := g.logger.With("session_key", key, "source", source)
	if !isScheduledSessionKey(key) {
		log.Warn("scheduled session cleanup: refusing non-scheduler session")
		return
	}
	if reason := g.scheduledSessionInUse(key); reason != "" {
		log.Debug("scheduled session kept", "reason", reason)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), scheduledSessionCleanupTimeout)
	defer cancel()
	deleted, err := g.sessions.DeleteSessionIfPromptOnly(ctx, key)
	switch {
	case err != nil:
		log.Warn("scheduled session cleanup failed; maintenance will prune it", "error", err)
	case deleted:
		log.Debug("scheduled session deleted: run left no reply")
	default:
		log.Debug("scheduled session kept", "reason", "has transcript")
	}
}

// scheduledSessionInUse returns why key must be kept regardless of its
// transcript, or "".
func (g *Gateway) scheduledSessionInUse(key string) string {
	r := g.turns()
	if r.Busy(key) {
		return "turn running or queued"
	}
	if len(r.SubAgents(key)) > 0 {
		return "spawned sub-agents"
	}
	if g.approvals != nil && len(g.approvals.Pending(key)) > 0 {
		return "pending approvals"
	}
	return ""
}

// FinalizeSession implements heartbeat.SessionFinalizer: the heartbeat run is
// done with session (after its last attempt, whatever the outcome).
func (e *turnAIExecutor) FinalizeSession(session *sessions.Session) {
	if session == nil {
		return
	}
	e.mu.Lock()
	delete(e.stored, session.Key) // a failed last attempt left its prompt ID here
	e.mu.Unlock()
	e.g.releaseScheduledSession(session.Key, "heartbeat")
}
