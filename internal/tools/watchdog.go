package tools

import (
	"time"
)

// Turn watchdog (conduit-1z6d).
//
// Bounds the wall-clock time of a single agentic chain so a stuck turn can
// never spin silently: when the hard cap is breached between round trips, the
// chain stops and a USER-VISIBLE timeout message is returned as normal content
// (same philosophy as the empty-response guard — a turn always ends with
// something delivered).
//
// Token-level stall detection (120s with no tokens AND no tool events → one
// retry) is deferred: it requires provider-level streaming hooks and is
// tracked in conduit-1z6d.

const (
	// TurnStallAfter is the intended stall threshold for future token-level
	// detection (not yet enforced — see above).
	TurnStallAfter = 120 * time.Second

	// TurnHardCap is the maximum wall-clock time for one agentic chain.
	TurnHardCap = 10 * time.Minute
)

// turnHardCap is a var so tests can shrink the cap.
var turnHardCap = TurnHardCap

// watchdogTimeoutResponse reports whether the chain has exceeded the hard cap,
// and if so returns the user-visible terminal message to deliver.
func watchdogTimeoutResponse(chainStart time.Time) (string, bool) {
	if time.Since(chainStart) < turnHardCap {
		return "", false
	}
	return "This turn hit the " + TurnHardCap.String() + " processing cap and was stopped to keep things responsive. " +
		"Work completed so far is saved — send a follow-up and I'll pick up from there.", true
}
