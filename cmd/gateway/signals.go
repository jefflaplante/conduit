package main

import (
	"log"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Shutdown budget for SIGTERM/SIGINT (conduit-31jg.27).
//
// deploy/conduit.service sets TimeoutStopSec=30 with KillMode=mixed: systemd
// sends one SIGTERM, then SIGKILLs everything 30s later. The drain
// (signalDrainTimeout) plus the gateway's bounded stop sequence
// (gateway.gatewayStopTimeout, 10s) must finish under that. hardExitTimeout
// is a last-resort watchdog so we exit on our own terms (flushing logs)
// before systemd's SIGKILL, even if a drain started earlier by SIGHUP or the
// gateway tool is still running with its longer 30s timeout.
//
// conduit-31jg.77: the drain now also waits for in-flight scheduler jobs, but
// within the SAME budget (one shared deadline, not additive): a SIGTERM stop
// is still <= 15s drain + 10s stop = 25s < 27s watchdog < 30s systemd. A
// SIGTERM that joins a SIGHUP drain shortens it to 15s from that moment.
// conduit-25o4: after the drain, queued outgoing messages are flushed within
// what is left of the drain budget, but for at least 1s: worst case
// 15s + 1s + 10s = 26s < 27s.
// SIGHUP alone (install.sh deploy) is not a systemd stop: 30s drain + 10s
// stop + RestartSec 5s = 45s, inside install.sh's 90s wait.
const (
	signalDrainTimeout = 15 * time.Second
	hupDrainTimeout    = 30 * time.Second
	hardExitTimeout    = 27 * time.Second
)

// shutdownStarter is the subset of *gateway.ShutdownManager used by the
// signal handler; an interface so the logic is testable without sending real
// signals to the test process.
type shutdownStarter interface {
	BeginShutdown(reason string, timeout time.Duration) error
	SetOnShutdown(fn func())
	// ShortenDrain caps an in-progress drain at timeout from now
	// (conduit-31jg.77).
	ShortenDrain(timeout time.Duration)
}

// signalController maps process signals onto the gateway lifecycle:
//
//   - SIGHUP: graceful drain, then restart (re-exec on bare metal; exit 0
//     under systemd/containers so the supervisor restarts us).
//   - SIGTERM/SIGINT (first): graceful drain via ShutdownManager with
//     signalDrainTimeout, then exit 0. Joins a drain already in progress and
//     cancels any pending re-exec (a stop overrides a restart). Arms a hard
//     exit watchdog.
//   - SIGTERM/SIGINT (second): force exit immediately.
//
// Before the gateway is ready there is nothing to drain; any signal cancels
// the startup context.
type signalController struct {
	sm        shutdownStarter
	cancel    func()
	exit      func(code int)
	ready     *atomic.Bool
	canReExec func() bool
	reExec    func()
	afterFunc func(d time.Duration, f func())

	mu          sync.Mutex
	termSignals int
	hupInFlight bool
}

func newSignalController(sm shutdownStarter, cancel func(), ready *atomic.Bool) *signalController {
	return &signalController{
		sm:        sm,
		cancel:    cancel,
		exit:      os.Exit,
		ready:     ready,
		canReExec: func() bool { return !isContainer() && !isUnderSystemd() },
		reExec:    reExec,
		afterFunc: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	}
}

func (c *signalController) handle(sig os.Signal) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch sig {
	case syscall.SIGHUP:
		if !c.ready.Load() {
			log.Println("SIGHUP received before gateway ready, exiting")
			c.cancel()
			return
		}
		if c.termSignals > 0 {
			log.Println("SIGHUP received during stop, ignoring")
			return
		}
		if c.hupInFlight {
			log.Println("SIGHUP already in progress, ignoring")
			return
		}
		log.Println("SIGHUP received, initiating graceful restart")
		c.hupInFlight = true
		// Under systemd we drain and exit 0; systemd's Restart= policy brings
		// the unit back up. In-process re-exec under systemd breaks process
		// tracking (MainPID changes without systemd's knowledge) and previously
		// caused a self-kill loop when LLM actions triggered `conduit restart`.
		// In containers, orchestrators likewise own restart. Only re-exec on
		// bare-metal / dev.
		if c.canReExec() {
			c.sm.SetOnShutdown(c.reExec)
		}
		if err := c.sm.BeginShutdown("SIGHUP", hupDrainTimeout); err != nil {
			log.Printf("Failed to begin shutdown: %v", err)
			c.hupInFlight = false
		}

	case syscall.SIGINT, syscall.SIGTERM:
		c.termSignals++
		if !c.ready.Load() {
			log.Printf("Received %v before gateway ready, exiting", sig)
			c.cancel()
			return
		}
		if c.termSignals > 1 {
			log.Printf("Received %v again during drain, forcing exit", sig)
			c.exit(1)
			return
		}
		log.Printf("Received %v, draining in-flight work (up to %v; send again to force exit)", sig, signalDrainTimeout)
		// A stop overrides a pending SIGHUP re-exec.
		c.sm.SetOnShutdown(nil)
		if err := c.sm.BeginShutdown(sig.String(), signalDrainTimeout); err != nil {
			log.Printf("Shutdown already in progress, waiting for it: %v", err)
			// conduit-31jg.77: a SIGHUP drain has a 30s budget and now really
			// waits for scheduler jobs; cap it at the SIGTERM budget so drain
			// + gateway stop still finish before hardExitTimeout/systemd.
			c.sm.ShortenDrain(signalDrainTimeout)
		}
		c.afterFunc(hardExitTimeout, func() {
			log.Printf("Graceful stop exceeded %v, forcing exit", hardExitTimeout)
			c.exit(1)
		})
	}
}
