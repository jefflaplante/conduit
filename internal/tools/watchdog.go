package tools

import (
	"strings"
	"time"

	"conduit/internal/ai"
)

// Turn watchdog (conduit-1z6d).
//
// Bounds the wall-clock time of a single agentic chain so a stuck turn can
// never spin silently: when the hard cap is breached between round trips, the
// chain stops and a USER-VISIBLE timeout message is returned as normal content
// (same philosophy as the empty-response guard — a turn always ends with
// something delivered).
//
// Adaptive extension (conduit-2026-09-04): a chain doing productive coding
// work past the base cap is granted bounded extensions instead of being
// killed mid-flight. Each extension is announced to the user via StatusUpdate
// (interrupt = /stop or any queued message; honored at the next boundary).
// Extensions require ALL of:
//   - the turn used coder-class tools (see turnBudget.markRound)
//   - at least one successful tool result in the recent window (progress)
//   - progress is fresh (no stale multi-minute gap since last success)
//   - extension budget not exhausted
//
// Token-level stall detection (120s with no tokens AND no tool events → one
// retry) is deferred: it requires provider-level streaming hooks and is
// tracked in conduit-1z6d.

const (
	// TurnStallAfter is the intended stall threshold for future token-level
	// detection (not yet enforced — see above).
	TurnStallAfter = 120 * time.Second

	// TurnHardCap is the maximum wall-clock time for one agentic chain
	// before extensions become eligible.
	TurnHardCap = 10 * time.Minute

	// TurnExtension is the additional wall-clock time granted per extension.
	TurnExtension = 10 * time.Minute

	// TurnMaxExtensions is the maximum number of extensions a single chain
	// may consume. Total ceiling = TurnHardCap + TurnMaxExtensions*TurnExtension.
	TurnMaxExtensions = 2

	// TurnProgressStaleAfter: if no successful tool result within this window,
	// the chain is NOT considered actively productive and gets no extension.
	TurnProgressStaleAfter = 5 * time.Minute
)

// turnHardCap is a var so tests can shrink the cap.
var turnHardCap = TurnHardCap

// turnMaxExtensions is a var so tests can shrink the extension budget.
var turnMaxExtensions = TurnMaxExtensions

// turnBudget carries the per-chain extension state through recursion.
type turnBudget struct {
	extensions     int       // consumed extensions
	lastProgress   time.Time // last round with >=1 successful tool result
	coderToolRounds int      // rounds that included at least one coder-class tool
	totalRounds    int
	started        time.Time
}

func newTurnBudget(now time.Time) *turnBudget {
	return &turnBudget{lastProgress: now, started: now}
}

// coderToolNames classifies tools that indicate hands-on engineering work.
// Matching is by exact tool name; unlisted tools are neutral (neither help
// nor hurt eligibility).
var coderToolNames = map[string]bool{
	"Read":          true,
	"Write":         true,
	"Edit":          true,
	"Glob":          true,
	"Grep":          true,
	"Bash":          true,
	"SessionsSpawn": true,
	"SessionsSend":  true,
	"SessionsList":  true,
	"SessionStatus": true,
	"Image":         true,
}

// markRound records the outcome of one tool round for eligibility.
func (tb *turnBudget) markRound(toolCalls []ai.ToolCall, anySuccess bool, now time.Time) {
	tb.totalRounds++
	hasCoder := false
	for _, tc := range toolCalls {
		if coderToolNames[tc.Name] {
			hasCoder = true
			break
		}
	}
	if hasCoder {
		tb.coderToolRounds++
	}
	if anySuccess {
		tb.lastProgress = now
	}
}

// watchdogDeadline returns the effective wall-clock deadline for the chain.
func watchdogDeadline(chainStart time.Time, extensions int) time.Time {
	return chainStart.Add(turnHardCap + time.Duration(extensions)*TurnExtension)
}

// watchdogTerminalMessage is the user-visible message returned when a turn is
// stopped for exceeding its wall-clock window without extension eligibility.
// reason explains why no extension was granted (stalled / budget / no coder work).
func watchdogTerminalMessage(reason string) string {
	switch {
	case strings.Contains(reason, "stalled"):
		return "This turn hit the " + TurnHardCap.String() + " processing cap and looked stalled " +
			"(no successful tool results recently), so I stopped it to keep things responsive. " +
			"Work completed so far is saved — send a follow-up and I'll pick up from there."
	case strings.Contains(reason, "budget"):
		return "This turn ran through its full time budget (processing cap " + TurnHardCap.String() + " + " +
			itoa(turnMaxExtensions) + " extensions) on productive work, so I stopped it there to stay bounded. " +
			"Work completed so far is saved — send a follow-up and I'll pick up from there."
	default:
		return "This turn hit the " + TurnHardCap.String() + " processing cap and was stopped to keep things responsive. " +
			"Work completed so far is saved — send a follow-up and I'll pick up from there."
	}
}

// watchdogTimeoutResponse reports whether the chain has passed its effective
// wall-clock deadline (base cap + extensions consumed so far). It is a pure
// "deadline passed" signal — the caller decides whether to grant another
// extension (if eligible) or terminate with the user-visible message.
func watchdogTimeoutResponse(chainStart time.Time, extensions int) (string, bool) {
	if elapsed := time.Since(chainStart); elapsed < turnHardCap+time.Duration(extensions)*TurnExtension {
		return "", false
	}
	return "This turn hit the " + TurnHardCap.String() + " processing cap and was stopped to keep things responsive. " +
		"Work completed so far is saved — send a follow-up and I'll pick up from there.", true
}

// extensionEligible reports whether the chain qualifies for another extension
// right now, with a reason string for the log/status line.
func (tb *turnBudget) extensionEligible(now time.Time) (bool, string) {
	if tb.extensions >= turnMaxExtensions {
		return false, "extension budget exhausted"
	}
	if tb.coderToolRounds == 0 {
		return false, "no coder-class tool activity this turn"
	}
	if now.Sub(tb.lastProgress) > TurnProgressStaleAfter {
		return false, "no successful tool result recently (stalled)"
	}
	return true, "productive coding work in flight"
}

// extensionStatusMessage is the user-visible announcement sent when an
// extension is granted.
func extensionStatusMessage(tb *turnBudget, now time.Time) string {
	remaining := turnMaxExtensions - tb.extensions - 1
	msg := "📍 Still working — this turn is doing productive coding work, so I'm extending the time cap " +
		"(extension " + itoa(tb.extensions+1) + "/" + itoa(turnMaxExtensions) + ", ~" +
		TurnExtension.String() + " more). Interrupt any time with /stop (immediate). " +
		"If you just send a message it'll be acked and queued — I'll handle it the moment this turn wraps."
	if remaining == 0 {
		msg = "📍 Still working — final extension granted (" + itoa(turnMaxExtensions) + "/" + itoa(turnMaxExtensions) +
			"). When this window ends the turn stops with a hand-off note; send a follow-up to continue. " +
			"Interrupt any time with /stop (immediate)."
	}
	return msg
}

// itoa is a tiny local helper to avoid importing strconv in the watchdog.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
