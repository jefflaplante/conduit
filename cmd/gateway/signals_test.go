package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeShutdown records calls instead of draining a real gateway.
type fakeShutdown struct {
	mu         sync.Mutex
	begins     []string
	timeouts   []time.Duration
	onShutdown func()
	inProgress bool
	shortens   []time.Duration
}

func (f *fakeShutdown) ShortenDrain(timeout time.Duration) {
	f.mu.Lock()
	f.shortens = append(f.shortens, timeout)
	f.mu.Unlock()
}

func (f *fakeShutdown) BeginShutdown(reason string, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, reason)
	f.timeouts = append(f.timeouts, timeout)
	if f.inProgress {
		return errors.New("shutdown already in progress")
	}
	f.inProgress = true
	return nil
}

func (f *fakeShutdown) SetOnShutdown(fn func()) {
	f.mu.Lock()
	f.onShutdown = fn
	f.mu.Unlock()
}

type sigHarness struct {
	ctl       *signalController
	sm        *fakeShutdown
	cancels   int
	exits     []int
	watchdogs []time.Duration
	fireWatch []func()
	reExecs   int
}

func newSigHarness(ready, canReExec bool) *sigHarness {
	h := &sigHarness{sm: &fakeShutdown{}}
	var r atomic.Bool
	r.Store(ready)
	h.ctl = &signalController{
		sm:        h.sm,
		cancel:    func() { h.cancels++ },
		exit:      func(code int) { h.exits = append(h.exits, code) },
		ready:     &r,
		canReExec: func() bool { return canReExec },
		reExec:    func() { h.reExecs++ },
		afterFunc: func(d time.Duration, f func()) {
			h.watchdogs = append(h.watchdogs, d)
			h.fireWatch = append(h.fireWatch, f)
		},
	}
	return h
}

// conduit-31jg.27: SIGTERM (systemctl stop/restart) must drain via
// ShutdownManager rather than cancel the gateway context immediately.
func TestSignal_SIGTERMDrainsViaShutdownManager(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		h := newSigHarness(true, true)
		h.ctl.handle(sig)

		if h.cancels != 0 {
			t.Errorf("%v: context cancelled directly; want drain", sig)
		}
		if len(h.sm.begins) != 1 || h.sm.timeouts[0] != signalDrainTimeout {
			t.Fatalf("%v: BeginShutdown calls = %v %v", sig, h.sm.begins, h.sm.timeouts)
		}
		if h.sm.onShutdown != nil {
			t.Errorf("%v: stop must not re-exec", sig)
		}
		if len(h.sm.shortens) != 0 { // conduit-31jg.77
			t.Errorf("%v: fresh stop must not shorten: %v", sig, h.sm.shortens)
		}
		if len(h.watchdogs) != 1 || h.watchdogs[0] != hardExitTimeout {
			t.Errorf("%v: hard-exit watchdog = %v", sig, h.watchdogs)
		}
		if len(h.exits) != 0 {
			t.Errorf("%v: exited early: %v", sig, h.exits)
		}
		// Watchdog fires -> exit(1).
		h.fireWatch[0]()
		if len(h.exits) != 1 || h.exits[0] != 1 {
			t.Errorf("%v: watchdog exit = %v", sig, h.exits)
		}
	}
}

func TestSignal_SecondTermForcesExit(t *testing.T) {
	h := newSigHarness(true, false)
	h.ctl.handle(syscall.SIGTERM)
	h.ctl.handle(syscall.SIGINT)
	if len(h.exits) != 1 || h.exits[0] != 1 {
		t.Fatalf("exits = %v, want [1]", h.exits)
	}
	if len(h.sm.begins) != 1 {
		t.Fatalf("second signal should not start another drain: %v", h.sm.begins)
	}
}

// systemctl restart during a SIGHUP drain: join the drain, drop the re-exec.
func TestSignal_TermDuringHUPDrainCancelsReExec(t *testing.T) {
	h := newSigHarness(true, true)
	h.ctl.handle(syscall.SIGHUP)
	if h.sm.onShutdown == nil {
		t.Fatal("SIGHUP on bare metal should arm re-exec")
	}
	if h.sm.timeouts[0] != hupDrainTimeout {
		t.Fatalf("SIGHUP drain timeout = %v", h.sm.timeouts[0])
	}
	h.ctl.handle(syscall.SIGTERM)
	if h.sm.onShutdown != nil {
		t.Fatal("SIGTERM must clear the pending re-exec")
	}
	if len(h.exits) != 0 || h.cancels != 0 {
		t.Fatalf("first SIGTERM should join drain, got exits=%v cancels=%d", h.exits, h.cancels)
	}
	if len(h.watchdogs) != 1 {
		t.Fatal("watchdog not armed")
	}
	// conduit-31jg.77: the 30s HUP drain is capped at the SIGTERM budget.
	if len(h.sm.shortens) != 1 || h.sm.shortens[0] != signalDrainTimeout {
		t.Fatalf("ShortenDrain calls = %v, want [%v]", h.sm.shortens, signalDrainTimeout)
	}
	// Later SIGHUP is ignored while stopping.
	h.ctl.handle(syscall.SIGHUP)
	if len(h.sm.begins) != 2 {
		t.Fatalf("unexpected BeginShutdown calls: %v", h.sm.begins)
	}
}

func TestSignal_HUPUnderSupervisorDoesNotReExec(t *testing.T) {
	h := newSigHarness(true, false)
	h.ctl.handle(syscall.SIGHUP)
	h.ctl.handle(syscall.SIGHUP) // duplicate ignored
	if h.sm.onShutdown != nil {
		t.Fatal("re-exec armed under systemd/container")
	}
	if len(h.sm.begins) != 1 {
		t.Fatalf("BeginShutdown calls = %v", h.sm.begins)
	}
}

func TestSignal_BeforeReadyCancels(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		h := newSigHarness(false, true)
		h.ctl.handle(sig)
		if h.cancels != 1 || len(h.sm.begins) != 0 {
			t.Errorf("%v before ready: cancels=%d begins=%v", sig, h.cancels, h.sm.begins)
		}
	}
}

// The budget must fit systemd's TimeoutStopSec=30 (deploy/conduit.service).
func TestSignal_BudgetUnderSystemdStopTimeout(t *testing.T) {
	const systemdStop = 30 * time.Second
	if hardExitTimeout >= systemdStop {
		t.Fatalf("hardExitTimeout %v >= TimeoutStopSec %v", hardExitTimeout, systemdStop)
	}
	if signalDrainTimeout+10*time.Second > hardExitTimeout {
		t.Fatalf("drain %v + gateway stop 10s exceeds hard exit %v", signalDrainTimeout, hardExitTimeout)
	}
}
