// Package mqttevents is the MQTT event rule engine (bd-39mx): owner-authored
// rules evaluated in code on every inbound MQTT event that, when they fire,
// spawn a sub-agent, queue a notice for the heartbeat, or log. Conditions and
// all throttling are Go; an LLM only appears after a rule has fired.
//
// Evaluation runs inline on the MQTT callback goroutine and is O(rules for
// the topic) with no IO: rule topics (MQTT filters, wildcards allowed) are
// compiled into a trie once in NewEngine, so routing an event costs its topic
// depth, not the rule count. Actions run on one worker goroutine fed by a
// bounded channel. All state is in memory.
package mqttevents

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/config"
	"conduit/internal/mqtt"
	"conduit/internal/ratelimit"
	"conduit/internal/trie"
)

const (
	jobQueueDepth   = 64
	globalBudgetKey = "mqtt-events"
)

// Deps are the engine's collaborators. Any may be nil: a missing Spawner
// downgrades spawn to queue, a missing Queuer downgrades queue to log, and a
// missing Publisher disables publish/alert/heartbeat outputs (the events are
// still evaluated and counted).
type Deps struct {
	Spawner   Spawner
	Queuer    Queuer
	History   HistorySource
	Publisher MQTTEventPublisher
}

// MQTTEventPublisher is the MQTT publish path for consolidation outputs
// (bd-e9o8 phase 2). *mqtt.Service satisfies it.
type MQTTEventPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*mqtt.PublishResult, error)
}

// Stats is a snapshot of engine counters.
type Stats struct {
	Spawned           uint64
	Queued            uint64
	Logged            uint64
	Refused           uint64 // unknown action verbs
	Dropped           uint64 // worker queue overflow
	CooldownSwallowed uint64
	BudgetDowngrades  uint64
	CapDowngrades     uint64
	SpawnErrors       uint64
	InFlight          int

	// Consolidation counters (bd-e9o8 phase 2), observable per docs rule 3.
	ConsolidationPublished  uint64 // summaries + passthroughs + heartbeats
	ConsolidationAlerts     uint64 // alert publications
	ConsolidationSuppressed uint64 // alert fires swallowed by suppression
	ConsolidationDropped    uint64 // drop_and_count events
}

type counters struct {
	spawned, queued, logged, refused, dropped       atomic.Uint64
	cooldown, budgetDowngrades, capDowngrades       atomic.Uint64
	spawnErrors                                     atomic.Uint64
	consPub, consAlert, consSuppressed, consDropped atomic.Uint64
}

// Engine evaluates rules and dispatches their actions.
type Engine struct {
	deps Deps
	// routes, rules and byID are built in NewEngine and read-only afterwards, which
	// is what lets HandleEvent route without taking a lock.
	routes     *trie.Trie
	rules      []*Rule        // enabled rules, in config order
	byID       map[string]int // rule ID -> index into rules
	perRule    *ratelimit.SlidingWindow
	global     *ratelimit.SlidingWindow
	maxSpawns  int
	payloadMax int

	evalMu sync.Mutex // serializes HandleEvent (rule state)
	jobs   chan job

	// Consolidation sweep (bd-e9o8): flushes stalled windows and overdue
	// noise heartbeats on the receive clock; armed lazily by Start.
	sweepStop chan struct{}
	sweepOnce sync.Once
	started   bool // worker goroutine running (Start); tests may Stop without Start

	mu       sync.Mutex
	inFlight map[string]bool // spawn label -> running

	ctx       context.Context
	cancel    context.CancelFunc
	quit      chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
	stats     counters
}

// NewEngine compiles the rules. It applies config defaults itself so a
// config built in code behaves like a loaded one.
func NewEngine(cfg config.MQTTEventsConfig, deps Deps) (*Engine, error) {
	cfg.ApplyDefaults()
	e := &Engine{
		deps:       deps,
		routes:     trie.New(),
		byID:       make(map[string]int),
		maxSpawns:  cfg.MaxConcurrentSpawns,
		payloadMax: cfg.PayloadMaxBytes,
		jobs:       make(chan job, jobQueueDepth),
		inFlight:   make(map[string]bool),
		quit:       make(chan struct{}),
	}
	for _, rc := range cfg.Rules {
		r, err := newRule(rc)
		if err != nil {
			return nil, err
		}
		if !rc.IsEnabled() {
			continue
		}
		// Config validation already rejects duplicate IDs and invalid topic
		// filters; these guard configs built in code. A duplicate would
		// silently shadow a rule, since the trie routes by ID.
		if _, dup := e.byID[rc.ID]; dup {
			return nil, fmt.Errorf("rule %q: duplicate id", rc.ID)
		}
		if err := e.routes.Insert(rc.Topic, rc.ID); err != nil {
			return nil, fmt.Errorf("rule %q: %w", rc.ID, err)
		}
		e.byID[rc.ID] = len(e.rules)
		r.eng = e
		e.rules = append(e.rules, r)
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.perRule = newWindow(cfg.PerRuleBudget)
	e.global = newWindow(cfg.GlobalBudget)
	return e, nil
}

func newWindow(b *config.MQTTEventBudget) *ratelimit.SlidingWindow {
	w := time.Duration(b.WindowS) * time.Second
	return ratelimit.NewSlidingWindow(w, b.MaxActions, w)
}

// Start launches the dispatch worker and the consolidation sweep. Spawns
// use a context derived from ctx.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.cancel()
		e.ctx, e.cancel = context.WithCancel(ctx)
		e.wg.Add(1)
		go e.worker()
		e.started = true
		// Consolidation janitor (bd-e9o8): flush windows whose appends
		// stalled past window_timeout_ms and publish overdue noise-floor
		// heartbeats. Receive clock, by design (docs edge cases 5 and 6).
		e.sweepStop = make(chan struct{})
		e.wg.Add(1)
		go e.sweeper()
	})
}

// Stop drains queued actions, stops the worker, the sweep and the budget
// windows. Idempotent; safe without Start.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.sweepOnce.Do(func() {
			if e.sweepStop != nil {
				close(e.sweepStop)
			}
		})
		// Engines built by tests (or misconfigured producers) without Start
		// have no worker running; drain the channel synchronously so queued
		// actions and consolidation outputs still dispatch before close.
		if !e.started {
			e.drain()
		}
		close(e.quit)
		e.wg.Wait()
		e.cancel()
		e.perRule.Stop()
		e.global.Stop()
	})
}

// sweeper runs the consolidation janitor until Stop. A 1s tick is cheap
// (a few map reads under evalMu) and bounds staleness to one tick beyond
// window_timeout_ms; consolidation windows are typically 5s stalls / 60s
// windows.
func (e *Engine) sweeper() {
	defer e.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			e.SweepConsolidation(time.Now())
		case <-e.sweepStop:
			return
		}
	}
}

// SweepConsolidation flushes stalled windows and overdue noise heartbeats
// (receive clock). Exposed for tests and operational tooling.
func (e *Engine) SweepConsolidation(now time.Time) {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()
	for _, r := range e.rules {
		t := r.twin
		if t == nil {
			continue
		}
		switch r.cfg.Condition.Type {
		case "window_buffer":
			for _, k := range append([]string(nil), r.cons.windowsKeys()...) {
				if w := r.cons.windows[k]; w != nil && !w.entriesEmpty() && now.Sub(w.lastAppend) >= t.stall {
					r.flushWindow(k, t)
				}
			}
		case "catch_all":
			r.sweepDrops(now, t)
		}
		// Staged jobs from sweep flushes/heartbeats must leave r.pending
		// immediately: a burst-then-silence pattern otherwise strands its
		// summary until the next matching event arrives.
		e.drainPending(r)
	}
}

// FlushConsolidation manually flushes the oldest buffered window of one
// rule and returns whether a window was flushed; repeat calls drain the
// remaining windows one per call. Exposed for tests and operational
// tooling; uses the receive clock like the sweeper.
func (e *Engine) FlushConsolidation(ruleID string) bool {
	e.evalMu.Lock()
	defer e.evalMu.Unlock()
	i, ok := e.byID[ruleID]
	if !ok {
		return false
	}
	r := e.rules[i]
	t := r.twin
	if t == nil || r.cons == nil || r.cfg.Condition.Type != "window_buffer" {
		return false
	}
	// Drain-all: flush every live window (creation order). The invariant —
	// every buffered run published exactly once — is what matters; the
	// burst-summary test drives per-entity pops explicitly instead.
	flushed := false
	for _, k := range append([]string(nil), r.cons.keyOrder...) {
		if r.flushWindow(k, t) {
			flushed = true
		}
	}
	if flushed {
		e.drainPending(r)
	}
	return flushed
}

func (e *Engine) worker() {
	defer e.wg.Done()
	for {
		select {
		case j := <-e.jobs:
			e.dispatch(j)
		case <-e.quit:
			e.drain()
			return
		}
	}
}

// drain dispatches everything currently queued, synchronously.
func (e *Engine) drain() {
	for {
		select {
		case j := <-e.jobs:
			e.dispatch(j)
		default:
			return
		}
	}
}

// HandleEvent is the mqtt.Service observer. It evaluates every rule whose
// topic filter matches the event topic, each exactly once, and enqueues fired
// actions; it never blocks.
func (e *Engine) HandleEvent(ev mqtt.Event) {
	ids := e.routes.Match(ev.Topic)
	if len(ids) == 0 {
		return
	}
	// Match sorts by ID; config order is what decides which of several rules
	// on one topic gets the last of a shared budget.
	idx := make([]int, len(ids))
	for i, id := range ids {
		idx[i] = e.byID[id]
	}
	sort.Ints(idx)
	e.evalMu.Lock()
	defer e.evalMu.Unlock()
	for _, i := range idx {
		r := e.rules[i]
		if r.twin != nil {
			// Consolidation rule (bd-e9o8 phase 2): its condition owns
			// buffering/alerting/counting and stages any output jobs;
			// flushes are publish-at-close semantics, not per-event fires,
			// so the v1 budget/cooldown path does not apply (its counters
			// live in Stats.Consolidation*).
			r.evaluateConsolidation(ev, r.twin)
			e.drainPending(r)
			continue
		}
		out, value := r.evaluate(ev)
		switch out {
		case swallowed:
			e.stats.cooldown.Add(1)
			continue
		case noMatch:
			continue
		}
		j := job{rule: r, ev: ev, value: value, action: r.cfg.Action.Type}
		if j.action != actLog && !e.budgetAllows(r.ID()) {
			j.action = downgrade(j.action)
			j.reason = "action budget exhausted"
			e.stats.budgetDowngrades.Add(1)
		}
		select {
		case e.jobs <- j:
		default:
			e.stats.dropped.Add(1)
			log.Printf("[MQTTEvents] worker queue full (%d): dropped %s action for rule %s", jobQueueDepth, j.action, r.ID())
		}
	}
}

// drainPending hands a rule's staged consolidation jobs to the publish path
// (still under evalMu).
func (e *Engine) drainPending(r *Rule) {
	for _, cj := range r.pending {
		switch cj.kind {
		case "publish":
			e.stageConsPublish(cj.topic, cj.payload, cj.qos, cj.retained)
			e.stats.consPub.Add(1)
		case "alert":
			e.stageConsPublish(cj.topic, cj.payload, cj.qos, false)
			e.stats.consAlert.Add(1)
		case "notice":
			if e.deps.Queuer != nil {
				if err := e.deps.Queuer.Enqueue(*cj.notice); err != nil {
					log.Printf("[MQTTEvents] rule=%s: queueing alert notice failed: %v", cj.notice.RuleID, err)
				}
			}
		}
	}
	r.pending = r.pending[:0]
}

// stageConsPublish enqueues a consolidation output on the worker's job
// channel; the worker publishes off the eval path, and tests / Stop drain
// the channel synchronously so publications are deterministic. A missing
// Publisher disables the output path (evaluation and counters still run).
func (e *Engine) stageConsPublish(topic string, payload []byte, qos byte, retained bool) {
	if e.deps.Publisher == nil {
		return
	}
	select {
	case e.jobs <- job{pub: &consPublish{topic: topic, payload: payload, qos: qos, retained: retained}}:
	default:
		e.stats.dropped.Add(1)
		log.Printf("[MQTTEvents] worker queue full (%d): dropped consolidation publish to %s", jobQueueDepth, topic)
	}
}

// publishCons runs on the worker goroutine: hands one consolidation output
// to the MQTT publisher.
func (e *Engine) publishCons(p *consPublish) {
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if _, err := e.deps.Publisher.Publish(ctx, p.topic, p.payload, p.qos, p.retained); err != nil {
		log.Printf("[MQTTEvents] consolidation publish to %s failed: %v", p.topic, err)
	}
}

// budgetAllows charges the per-rule window, then the global one.
func (e *Engine) budgetAllows(ruleID string) bool {
	if ok, _, _, _ := e.perRule.Allow(ruleID); !ok {
		return false
	}
	ok, _, _, _ := e.global.Allow(globalBudgetKey)
	return ok
}

// Stats returns a snapshot of the engine counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	inFlight := len(e.inFlight)
	e.mu.Unlock()
	return Stats{
		Spawned:                 e.stats.spawned.Load(),
		Queued:                  e.stats.queued.Load(),
		Logged:                  e.stats.logged.Load(),
		Refused:                 e.stats.refused.Load(),
		Dropped:                 e.stats.dropped.Load(),
		CooldownSwallowed:       e.stats.cooldown.Load(),
		BudgetDowngrades:        e.stats.budgetDowngrades.Load(),
		CapDowngrades:           e.stats.capDowngrades.Load(),
		SpawnErrors:             e.stats.spawnErrors.Load(),
		InFlight:                inFlight,
		ConsolidationPublished:  e.stats.consPub.Load(),
		ConsolidationAlerts:     e.stats.consAlert.Load(),
		ConsolidationSuppressed: e.stats.consSuppressed.Load(),
		ConsolidationDropped:    e.stats.consDropped.Load(),
	}
}
