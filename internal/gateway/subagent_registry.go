package gateway

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Sub-agent registry (conduit-38cz part 2, conduit-31jg.84).
//
// Every sub-agent spawned in this gateway run is registered here, keyed by
// its session key, with its parent session and its spawn context's cancel
// func. It answers:
//
//   - lineage: children of a session, the ancestry chain (parent-only cancel
//     guard, SessionStatus "sub_agents");
//   - cancel: SessionsCancel cancels one sub-agent and its descendants;
//   - /stop cascade: /stop in a chat cancels every running sub-agent that
//     chat spawned, their children, and so on.
//
// The registry lives on the TurnRunner so every /stop entry point (channel
// commands, WebSocket, the in-process TUI client) reaches it. Lineage is also
// persisted in each sub-agent session's context (parent_session_key,
// subagent_status) so it survives a restart for inspection; the in-memory
// entries (and their cancel funcs) do not — a restart already ends every
// sub-agent (conduit-31jg.88 drain).
//
// Cancelling a sub-agent cancels its spawn context (so a turn that has not
// started yet, is queued or is running ends) AND TurnRunner.Stop(key) (so
// wake turns queued on the child session are dropped too).
//
// Lock order: r.mu (registry) is a leaf; it is never held while calling
// the TurnRunner or a cancel func.

// SubAgentStatus is a sub-agent's lifecycle state.
type SubAgentStatus string

const (
	SubAgentRunning   SubAgentStatus = "running"
	SubAgentCompleted SubAgentStatus = "completed"
	SubAgentFailed    SubAgentStatus = "failed"
	SubAgentCanceled  SubAgentStatus = "canceled"
)

// SubAgentInfo describes one registered sub-agent.
type SubAgentInfo struct {
	SessionKey       string         `json:"session_key"`
	ParentSessionKey string         `json:"parent_session_key,omitempty"`
	Label            string         `json:"label,omitempty"`
	Model            string         `json:"model,omitempty"`
	Task             string         `json:"task,omitempty"` // preview
	Status           SubAgentStatus `json:"status"`
	StartedAt        time.Time      `json:"started_at"`
	EndedAt          time.Time      `json:"ended_at,omitzero"`
	// CanceledBy is the session (or "/stop", "owner") that canceled it.
	CanceledBy string `json:"canceled_by,omitempty"`
}

type subAgentEntry struct {
	info   SubAgentInfo
	cancel context.CancelFunc
	// quiet suppresses the parent wake for a cancel the parent's human
	// already knows about (/stop cascade) or whose parent is itself being
	// canceled (descendants).
	quiet bool
}

// subAgentRetention is how long finished entries stay for lineage queries.
const subAgentRetention = time.Hour

type subAgentRegistry struct {
	mu      sync.Mutex
	entries map[string]*subAgentEntry
}

func newSubAgentRegistry() *subAgentRegistry {
	return &subAgentRegistry{entries: make(map[string]*subAgentEntry)}
}

func (r *subAgentRegistry) register(info SubAgentInfo, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(time.Now())
	info.Status = SubAgentRunning
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now()
	}
	r.entries[info.SessionKey] = &subAgentEntry{info: info, cancel: cancel}
}

// finish records how the sub-agent's turn ended. A sub-agent marked
// canceled stays canceled unless its turn completed anyway (cancel raced
// the final reply). It returns the final
// info and whether the parent wake is suppressed.
func (r *subAgentRegistry) finish(key string, status SubAgentStatus) (SubAgentInfo, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok {
		return SubAgentInfo{}, false, false
	}
	switch {
	case e.info.Status == SubAgentRunning:
		e.info.Status = status
	case e.info.Status == SubAgentCanceled && status == SubAgentCompleted:
		// The turn finished before the cancel reached it: its result is
		// real, deliver it as a completion.
		e.info.Status, e.info.CanceledBy, e.quiet = SubAgentCompleted, "", false
	}
	if e.info.EndedAt.IsZero() {
		e.info.EndedAt = time.Now()
	}
	e.cancel = nil
	return e.info, e.quiet, true
}

func (r *subAgentRegistry) get(key string) (SubAgentInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok {
		return SubAgentInfo{}, false
	}
	return e.info, true
}

// children returns the sub-agents parent spawned, oldest first.
func (r *subAgentRegistry) children(parent string) []SubAgentInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []SubAgentInfo
	for _, e := range r.entries {
		if e.info.ParentSessionKey == parent {
			out = append(out, e.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// isAncestor reports whether anc spawned key, directly or transitively.
func (r *subAgentRegistry) isAncestor(anc, key string) bool {
	if anc == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for cur := key; !seen[cur]; {
		seen[cur] = true
		e, ok := r.entries[cur]
		if !ok || e.info.ParentSessionKey == "" {
			return false
		}
		if e.info.ParentSessionKey == anc {
			return true
		}
		cur = e.info.ParentSessionKey
	}
	return false
}

// findByLabel returns the newest running sub-agent labelled label, preferring
// children of parent.
func (r *subAgentRegistry) findByLabel(parent, label string) (SubAgentInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *SubAgentInfo
	bestChild := false
	for _, e := range r.entries {
		if e.info.Label != label || e.info.Status != SubAgentRunning {
			continue
		}
		child := parent != "" && e.info.ParentSessionKey == parent
		if best == nil || (child && !bestChild) || (child == bestChild && e.info.StartedAt.After(best.StartedAt)) {
			info := e.info
			best, bestChild = &info, child
		}
	}
	if best == nil {
		return SubAgentInfo{}, false
	}
	return *best, true
}

// markTree marks root (when includeRoot) and every running descendant
// canceled by by, and returns their cancel funcs and session keys. The root
// gets quietRoot; descendants are always quiet (their parent is being
// canceled too).
func (r *subAgentRegistry) markTree(root string, includeRoot bool, by string, quietRoot bool) (keys []string, cancels []context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mark := func(e *subAgentEntry, quiet bool) {
		if e.info.Status != SubAgentRunning {
			return
		}
		e.info.Status = SubAgentCanceled
		e.info.CanceledBy = by
		e.quiet = quiet
		keys = append(keys, e.info.SessionKey)
		if e.cancel != nil {
			cancels = append(cancels, e.cancel)
		}
	}
	if includeRoot {
		if e, ok := r.entries[root]; ok {
			mark(e, quietRoot)
		}
	}
	// BFS over all entries (a finished child may still have running
	// grandchildren).
	queue, seen := []string{root}, map[string]bool{root: true}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for k, e := range r.entries {
			if e.info.ParentSessionKey != p || seen[k] {
				continue
			}
			seen[k] = true
			mark(e, true)
			queue = append(queue, k)
		}
	}
	return keys, cancels
}

// pruneLocked drops finished entries older than subAgentRetention unless a
// descendant is still running (lineage for the cascade must stay intact).
func (r *subAgentRegistry) pruneLocked(now time.Time) {
	running := map[string]bool{} // keys with a running descendant (or self)
	for _, e := range r.entries {
		if e.info.Status != SubAgentRunning {
			continue
		}
		seen := map[string]bool{}
		for cur := e.info.SessionKey; cur != "" && !seen[cur]; {
			seen[cur] = true
			running[cur] = true
			pe, ok := r.entries[cur]
			if !ok {
				break
			}
			cur = pe.info.ParentSessionKey
		}
	}
	for k, e := range r.entries {
		if e.info.Status != SubAgentRunning && !running[k] && now.Sub(e.info.EndedAt) > subAgentRetention {
			delete(r.entries, k)
		}
	}
}

// --- TurnRunner surface ---

// RegisterSubAgent records a sub-agent and its spawn-context cancel func.
func (r *TurnRunner) RegisterSubAgent(info SubAgentInfo, cancel context.CancelFunc) {
	r.subagents.register(info, cancel)
}

// SubAgents returns the sub-agents parent spawned (running and recently
// finished), oldest first.
func (r *TurnRunner) SubAgents(parent string) []SubAgentInfo {
	return r.subagents.children(parent)
}

// SubAgent returns the registered sub-agent key.
func (r *TurnRunner) SubAgent(key string) (SubAgentInfo, bool) {
	return r.subagents.get(key)
}

// finishSubAgent records a sub-agent's end; see subAgentRegistry.finish.
func (r *TurnRunner) finishSubAgent(key string, status SubAgentStatus) (SubAgentInfo, bool, bool) {
	return r.subagents.finish(key, status)
}

// cancelTree cancels the marked sub-agents: spawn ctx first (covers a turn
// not started yet), then Stop (running turn + wakes queued on the child).
func (r *TurnRunner) cancelTree(keys []string, cancels []context.CancelFunc) {
	for _, c := range cancels {
		c()
	}
	for _, k := range keys {
		r.Stop(k)
	}
}

// CancelSubAgent cancels the running sub-agent key and every running
// sub-agent below it. by names the canceller (recorded, and shown to the
// parent). quiet suppresses the parent wake. It returns how many sub-agents
// were canceled (0 when key was not running).
func (r *TurnRunner) CancelSubAgent(key, by string, quiet bool) int {
	keys, cancels := r.subagents.markTree(key, true, by, quiet)
	r.cancelTree(keys, cancels)
	return len(keys)
}
