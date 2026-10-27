package gateway

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// Mid-turn steering (conduit-5uw3).
//
// A steerable turn (TurnRequest.SteerScope set: a live human on a channel
// adapter, WS connection or TUI client) opens a steering box for its session
// while it runs. A later message with the same scope that would queue behind
// it is offered to the box instead of only waiting: the running turn's tool
// loop drains the box at its next round boundary (tools.SteeringSource), the
// message is persisted to the transcript at that point and becomes part of
// the running turn, and the queued turn ends as Steered without a turn of
// its own. A message the running turn never drains (it was already on its
// final provider call, or made no tool calls at all) stays queued and runs
// as a normal turn, so steering never loses a message.
//
// Order is preserved: once a non-steerable turn queues behind the running
// one (a photo, a sub-agent announce, another client), the box stops
// accepting offers, so no later message overtakes it.

// steerPrefix tells the model where a folded message came from.
const steerPrefix = "[The user sent this while you were working on the request above. " +
	"Take it into account before your next step; it may change or cancel the plan.]\n"

// steerAckText replaces busyAckText (channels) and queuedNoticeText (WS/TUI)
// when the queued message was offered to the running turn.
const steerAckText = "Got it — I'll take this into account at my next step (or right after, if I'm already wrapping up)."

// steeredNoticeText ends a WS/TUI request whose message was folded into the
// running turn: its own StreamStart/StreamEnd never come.
const steeredNoticeText = "↳ Folded into the current request; its reply answers this message."

// steerBox collects the messages offered to one running turn. Guarded by
// TurnRunner.mu.
type steerBox struct {
	scope   string
	pending []*steerEntry
	// closed: a non-steerable turn queued behind the running one; later
	// messages must not overtake it.
	closed bool
}

// steerEntry is one queued message offered to a running turn. Guarded by
// TurnRunner.mu.
type steerEntry struct {
	req TurnRequest
	// wake cancels the queued turn's lock wait once the message was folded.
	wake context.CancelFunc
	// consumed: the running turn took the message; the queued turn must end
	// as Steered and never run.
	consumed bool
	// msgID is the folded message's transcript row ("" until persisted, or
	// when persisting failed).
	msgID string
}

type steerOfferedKey struct{}

// withSteerOffered marks the Queued ctx of a message offered to the
// running turn, so the sink can pick steerAckText.
func withSteerOffered(ctx context.Context) context.Context {
	return context.WithValue(ctx, steerOfferedKey{}, true)
}

func steerOffered(ctx context.Context) bool {
	v, _ := ctx.Value(steerOfferedKey{}).(bool)
	return v
}

// canSteer reports whether req may be folded into a running turn: a plain
// human text message the runner itself persists, and not a farewell (which
// triggers SPAR reflection in its own turn).
func (r *TurnRunner) canSteer(req TurnRequest) bool {
	if req.SteerScope == "" || req.Origin == nil || len(req.Attachments) > 0 ||
		req.PersistedUserMessageID != "" || strings.TrimSpace(req.Text) == "" {
		return false
	}
	return r.hooks == nil || req.SkipReflection || !r.hooks.IsFarewell(req.Text)
}

// offerSteerLocked offers a turn that is about to queue behind a busy
// session to that session's running turn. It returns nil when the turn must
// simply wait. A non-steerable turn closes the box. Caller holds r.mu.
func (r *TurnRunner) offerSteerLocked(key string, req TurnRequest) *steerEntry {
	box := r.steer[key]
	if box == nil || box.closed {
		return nil
	}
	if !r.canSteer(req) || req.SteerScope != box.scope {
		box.closed = true
		return nil
	}
	e := &steerEntry{req: req}
	box.pending = append(box.pending, e)
	return e
}

// withdrawSteerLocked settles a queued turn's offer once its lock wait
// ended. true: the running turn consumed it. false: it was never taken and
// can no longer be (the offer is removed). Caller holds r.mu.
func (r *TurnRunner) withdrawSteerLocked(key string, e *steerEntry) bool {
	if e.consumed {
		return true
	}
	if box := r.steer[key]; box != nil {
		for i, p := range box.pending {
			if p == e {
				box.pending = append(box.pending[:i:i], box.pending[i+1:]...)
				break
			}
		}
	}
	return false
}

// openSteerLocked opens the steering box for a turn that just started
// running. Caller holds r.mu.
func (r *TurnRunner) openSteerLocked(key string, req TurnRequest) {
	if !r.canSteer(req) {
		return
	}
	r.steer[key] = &steerBox{scope: req.SteerScope}
}

// closeSteerLocked drops the running turn's box. Offers still pending stay
// queued and run as normal turns. Caller holds r.mu.
func (r *TurnRunner) closeSteerLocked(key string) {
	delete(r.steer, key)
}

// steeringSource is the running turn's tools.SteeringSource. It is bound to
// the session: a ctx for any other session (a nested turn that inherited
// this ctx) gets nothing.
func (r *TurnRunner) steeringSource(key, parentMsgID string) tools.SteeringSource {
	return func(ctx context.Context) []string {
		if types.RequestSessionKey(ctx) != key {
			return nil
		}
		r.mu.Lock()
		box := r.steer[key]
		if box == nil || len(box.pending) == 0 {
			r.mu.Unlock()
			return nil
		}
		taken := box.pending
		box.pending = nil
		for _, e := range taken {
			e.consumed = true
		}
		r.mu.Unlock()

		texts := make([]string, 0, len(taken))
		for _, e := range taken {
			id := r.persistSteered(key, parentMsgID, e.req)
			r.mu.Lock()
			e.msgID = id
			r.mu.Unlock()
			e.wake()
			texts = append(texts, steerPrefix+e.req.Text)
		}
		r.logger.Info("folded mid-turn user messages into the running turn",
			"session_key", key, "count", len(taken), "parent_message_id", parentMsgID)
		return texts
	}
}

// persistSteered stores a folded message in the transcript, inside the
// running turn's lock, so it reads before that turn's reply. A failure is
// logged; the model still sees the message.
func (r *TurnRunner) persistSteered(key, parentMsgID string, req TurnRequest) string {
	text := req.StoreText
	if text == "" {
		text = req.Text
	}
	md := make(map[string]string, len(req.StoreMetadata)+1)
	for k, v := range req.StoreMetadata {
		md[k] = v
	}
	md["steered_into"] = parentMsgID
	m, err := r.sessions.AddMessage(key, "user", text, md)
	if err != nil {
		r.logger.Error("steering: error saving folded user message", "session_key", key, "error", fmt.Errorf("save user message: %w", err))
		return ""
	}
	return m.ID
}
