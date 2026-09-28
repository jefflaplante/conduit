package gateway

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// TurnOutcome is how a turn in flight during a shutdown drain ended
// (conduit-31jg.88).
type TurnOutcome string

const (
	// TurnCompleted: finished within the drain budget.
	TurnCompleted TurnOutcome = "completed"
	// TurnForceCancelled: still running at the drain deadline.
	TurnForceCancelled TurnOutcome = "force_cancelled"
	// TurnDropped: queued behind the drain; never ran.
	TurnDropped TurnOutcome = "dropped"
	// TurnStopped: cancelled by /stop during the drain (no notice).
	TurnStopped TurnOutcome = "stopped"
)

// Turn kinds recorded in TurnSnapshot.Kind (conduit-31jg.88).
const (
	TurnKindInteractive = "interactive" // live human: Telegram/channel, WS, TUI
	TurnKindScheduled   = "scheduled"   // cron / heartbeat job
	TurnKindSubAgent    = "subagent"
	TurnKindWake        = "wake"
	TurnKindOther       = "other"
)

// TurnSnapshot describes one queued or running turn (conduit-31jg.88). It
// is also the restart breadcrumb's per-turn record.
type TurnSnapshot struct {
	SessionKey string `json:"session_key"`
	ChannelID  string `json:"channel_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	// Kind is one of the TurnKind* values; Source the entry point
	// ("telegram", "websocket", "tui", "cron", "wake:…").
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	// ScheduledJob is the owning scheduler job, if any.
	ScheduledJob string `json:"scheduled_job,omitempty"`
	// ParentSessionKey is the session that spawned this sub-agent turn
	// (conduit-38cz); "" for other turns.
	ParentSessionKey string `json:"parent_session_key,omitempty"`
	// UserMessageID is the persisted user row ("" while queued).
	UserMessageID string `json:"user_message_id,omitempty"`
	// Preview is the first line of the request, at most turnPreviewMax runes.
	Preview   string    `json:"preview,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"running"`
	// Outcome is set by DrainReport / the drain log.
	Outcome TurnOutcome `json:"outcome,omitempty"`
}

// Interactive reports whether the turn came from a live human.
func (s TurnSnapshot) Interactive() bool { return s.Kind == TurnKindInteractive }

// turnState is the runner's mutable record of one turn; guarded by r.mu.
type turnState struct{ snap TurnSnapshot }

const turnPreviewMax = 120

// turnPreview is the first non-empty line of text, at most turnPreviewMax
// runes (with an ellipsis when cut).
func turnPreview(text string) string {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if utf8.RuneCountInString(line) > turnPreviewMax {
		line = string([]rune(line)[:turnPreviewMax-1]) + "…"
	}
	return line
}

func newTurnState(req TurnRequest) *turnState {
	snap := TurnSnapshot{
		SessionKey:   req.Session.Key,
		ChannelID:    req.ChannelID,
		UserID:       req.UserID,
		ScheduledJob: req.ScheduledJob,
		StartedAt:    time.Now(),
		Source:       req.NonInteractiveSource,

		ParentSessionKey: req.ParentSessionKey,
	}
	text := req.Text
	if strings.TrimSpace(text) == "" {
		text = req.StoreText
	}
	snap.Preview = turnPreview(text)
	switch {
	case req.ScheduledJob != "":
		snap.Kind = TurnKindScheduled
	case req.Origin != nil:
		snap.Kind = TurnKindInteractive
		snap.Source = req.Origin.Source
	case req.NonInteractiveSource == "subagent":
		snap.Kind = TurnKindSubAgent
	case strings.HasPrefix(req.NonInteractiveSource, "wake"):
		snap.Kind = TurnKindWake
	default:
		snap.Kind = TurnKindOther
	}
	return &turnState{snap: snap}
}

// Snapshot returns every queued and running turn (Running tells which),
// oldest first. Lock order: takes r.mu only; callers must not hold
// ActiveRequestsMu (r.mu is always taken before it). conduit-31jg.88
func (r *TurnRunner) Snapshot() []TurnSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(false)
}

// DrainReport returns the turns seen by the shutdown drain with their
// outcome: those that ended while draining (completed / dropped / stopped)
// plus those still in flight, marked force_cancelled (running) or dropped
// (queued). The drain calls it at its deadline BEFORE cancelling anything,
// or once everything drained. Same lock order as Snapshot. conduit-31jg.88
func (r *TurnRunner) DrainReport() []TurnSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]TurnSnapshot(nil), r.drainLog...)
	out = append(out, r.snapshotLocked(true)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (r *TurnRunner) snapshotLocked(withOutcome bool) []TurnSnapshot {
	out := make([]TurnSnapshot, 0, len(r.inflight))
	for t := range r.inflight {
		snap := t.snap
		if withOutcome {
			snap.Outcome = TurnDropped
			if snap.Running {
				snap.Outcome = TurnForceCancelled
			}
		}
		out = append(out, snap)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// endTurnLocked removes t from the in-flight set and, while the gateway
// drains, records how it ended. Caller holds r.mu.
func (r *TurnRunner) endTurnLocked(t *turnState, outcome TurnOutcome) {
	delete(r.inflight, t)
	if r.draining != nil && r.draining() {
		snap := t.snap
		snap.Outcome = outcome
		r.drainLog = append(r.drainLog, snap)
	}
}
