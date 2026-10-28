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
// downgrades spawn to queue, a missing Queuer downgrades queue to log.
type Deps struct {
	Spawner Spawner
	Queuer  Queuer
	History HistorySource
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
}

type counters struct {
	spawned, queued, logged, refused, dropped atomic.Uint64
	cooldown, budgetDowngrades, capDowngrades atomic.Uint64
	spawnErrors                               atomic.Uint64
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

// Start launches the dispatch worker. Spawns use a context derived from ctx.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.cancel()
		e.ctx, e.cancel = context.WithCancel(ctx)
		e.wg.Add(1)
		go e.worker()
	})
}

// Stop drains queued actions, stops the worker and the budget windows.
// Idempotent; safe without Start.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		close(e.quit)
		e.wg.Wait()
		e.cancel()
		e.perRule.Stop()
		e.global.Stop()
	})
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
		Spawned:           e.stats.spawned.Load(),
		Queued:            e.stats.queued.Load(),
		Logged:            e.stats.logged.Load(),
		Refused:           e.stats.refused.Load(),
		Dropped:           e.stats.dropped.Load(),
		CooldownSwallowed: e.stats.cooldown.Load(),
		BudgetDowngrades:  e.stats.budgetDowngrades.Load(),
		CapDowngrades:     e.stats.capDowngrades.Load(),
		SpawnErrors:       e.stats.spawnErrors.Load(),
		InFlight:          inFlight,
	}
}
