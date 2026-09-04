package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
)

func TestWatchdogTimeoutResponse_BeforeCap(t *testing.T) {
	_, timedOut := watchdogTimeoutResponse(time.Now().Add(-time.Second), 0)
	if timedOut {
		t.Fatal("chain 1s old must not be considered timed out")
	}
}

func TestWatchdogTimeoutResponse_AfterAllExtensions(t *testing.T) {
	origCap := turnHardCap
	origMax := turnMaxExtensions
	turnHardCap = 50 * time.Millisecond
	turnMaxExtensions = 2
	defer func() { turnHardCap = origCap; turnMaxExtensions = origMax }()

	time.Sleep(60 * time.Millisecond)

	msg, timedOut := watchdogTimeoutResponse(time.Now().Add(-time.Hour), 2)
	if !timedOut {
		t.Fatal("chain past cap AND exhausted extensions must be flagged")
	}
	if msg == "" || !strings.Contains(msg, "processing cap") {
		t.Fatalf("timeout message must be user-visible and explanatory, got %q", msg)
	}
}

func TestWatchdogDeadline_ExtensionsExtendCeiling(t *testing.T) {
	origCap := turnHardCap
	turnHardCap = 50 * time.Millisecond
	defer func() { turnHardCap = origCap }()

	time.Sleep(60 * time.Millisecond)

	// Deadline is base cap + extensions consumed so far.
	start := time.Now().Add(-time.Hour)
	if _, past := watchdogTimeoutResponse(start, 0); !past {
		t.Fatal("past base cap must be reported (caller decides extend vs stop)")
	}
	if _, past := watchdogTimeoutResponse(start, 1); !past {
		t.Fatal("past base cap + 1 extension must still be reported")
	}
}

func TestCheckTurnWindow_GrantsExtensionForCoderWork(t *testing.T) {
	origCap := turnHardCap
	origMax := turnMaxExtensions
	turnHardCap = 50 * time.Millisecond
	turnMaxExtensions = 2
	defer func() { turnHardCap = origCap; turnMaxExtensions = origMax }()

	time.Sleep(60 * time.Millisecond)

	e := &ExecutionEngine{registry: nil}
	tb := newTurnBudget(time.Now().Add(-time.Hour))
	tb.markRound([]ai.ToolCall{{Name: "Bash"}}, true, time.Now())

	stop := e.checkTurnWindow(context.Background(), time.Now().Add(-time.Hour), tb, 3)
	if stop != nil {
		t.Fatalf("coder work past window must get an extension, got stop: %v", stop.Content)
	}
	if tb.extensions != 1 {
		t.Fatalf("extension must be recorded, got %d", tb.extensions)
	}
}

func TestCheckTurnWindow_StopsNonCoderWork(t *testing.T) {
	origCap := turnHardCap
	turnHardCap = 50 * time.Millisecond
	defer func() { turnHardCap = origCap }()

	time.Sleep(60 * time.Millisecond)

	e := &ExecutionEngine{registry: nil}
	tb := newTurnBudget(time.Now().Add(-time.Hour))
	tb.markRound([]ai.ToolCall{{Name: "Message"}}, true, time.Now())

	stop := e.checkTurnWindow(context.Background(), time.Now().Add(-time.Hour), tb, 3)
	if stop == nil {
		t.Fatal("non-coder work past window must stop")
	}
	if !strings.Contains(stop.Content, "processing cap") {
		t.Fatalf("stop content must be user-visible timeout, got %q", stop.Content)
	}
}

func TestTurnBudget_Eligible_CoderWork(t *testing.T) {
	now := time.Now()
	tb := newTurnBudget(now.Add(-11 * time.Minute))
	tb.markRound([]ai.ToolCall{{Name: "Read"}, {Name: "Bash"}}, true, now.Add(-time.Minute))

	ok, reason := tb.extensionEligible(now)
	if !ok {
		t.Fatalf("coder round with recent success must be eligible, reason: %s", reason)
	}
}

func TestTurnBudget_Ineligible_NoCoderTools(t *testing.T) {
	now := time.Now()
	tb := newTurnBudget(now.Add(-11 * time.Minute))
	tb.markRound([]ai.ToolCall{{Name: "WebSearch"}}, true, now.Add(-time.Minute))

	ok, reason := tb.extensionEligible(now)
	if ok {
		t.Fatal("non-coder work must not be eligible")
	}
	if !strings.Contains(reason, "coder-class") {
		t.Fatalf("reason should mention coder-class, got %q", reason)
	}
}

func TestTurnBudget_Ineligible_StaleProgress(t *testing.T) {
	now := time.Now()
	tb := newTurnBudget(now.Add(-11 * time.Minute))
	tb.markRound([]ai.ToolCall{{Name: "Bash"}}, true, now.Add(-6*time.Minute)) // success 6m ago

	ok, reason := tb.extensionEligible(now)
	if ok {
		t.Fatal("stale progress (>5m since last success) must not be eligible")
	}
	if !strings.Contains(reason, "stalled") {
		t.Fatalf("reason should mention stall, got %q", reason)
	}
}

func TestTurnBudget_Ineligible_BudgetExhausted(t *testing.T) {
	now := time.Now()
	tb := newTurnBudget(now.Add(-31 * time.Minute))
	tb.extensions = turnMaxExtensions

	ok, reason := tb.extensionEligible(now)
	if ok {
		t.Fatal("exhausted budget must not be eligible")
	}
	if !strings.Contains(reason, "budget") {
		t.Fatalf("reason should mention budget, got %q", reason)
	}
}

func TestExtensionStatusMessage_Formats(t *testing.T) {
	now := time.Now()
	tb := newTurnBudget(now)
	tb.extensions = 0

	msg := extensionStatusMessage(tb, now)
	if !strings.Contains(msg, "1/2") || !strings.Contains(msg, "/stop") {
		t.Fatalf("first extension message must show count and interrupt hint, got %q", msg)
	}

	tb.extensions = 1
	msg = extensionStatusMessage(tb, now)
	if !strings.Contains(msg, "final extension") {
		t.Fatalf("last extension message must signal finality, got %q", msg)
	}
}

func TestTurnHardCapConstants(t *testing.T) {
	if TurnHardCap != 10*time.Minute {
		t.Fatalf("hard cap should be 10m, got %s", TurnHardCap)
	}
	if TurnExtension != 10*time.Minute {
		t.Fatalf("extension should be 10m, got %s", TurnExtension)
	}
	if TurnMaxExtensions != 2 {
		t.Fatalf("max extensions should be 2, got %d", TurnMaxExtensions)
	}
	if TurnStallAfter != 120*time.Second {
		t.Fatalf("stall threshold should be 120s, got %s", TurnStallAfter)
	}
}

func TestWatchdogTerminalMessage_ReasonSpecific(t *testing.T) {
	stalled := watchdogTerminalMessage("no successful tool result recently (stalled)")
	if !strings.Contains(stalled, "stalled") {
		t.Fatalf("stalled reason must surface to user, got %q", stalled)
	}
	budget := watchdogTerminalMessage("extension budget exhausted")
	if !strings.Contains(budget, "processing cap") || !strings.Contains(budget, "extensions") {
		t.Fatalf("budget-exhausted message must stay user-visible and explain the cap stack, got %q", budget)
	}
	fallback := watchdogTerminalMessage("no coder-class tool activity this turn")
	if !strings.Contains(fallback, "processing cap") {
		t.Fatalf("fallback message must stay user-visible, got %q", fallback)
	}
}
