package tools

import (
	"strings"
	"testing"
	"time"
)

func TestWatchdogTimeoutResponse_BeforeCap(t *testing.T) {
	_, timedOut := watchdogTimeoutResponse(time.Now().Add(-time.Second))
	if timedOut {
		t.Fatal("chain 1s old must not be considered timed out")
	}
}

func TestWatchdogTimeoutResponse_AfterCap(t *testing.T) {
	orig := turnHardCap
	turnHardCap = 50 * time.Millisecond
	defer func() { turnHardCap = orig }()

	time.Sleep(60 * time.Millisecond)

	msg, timedOut := watchdogTimeoutResponse(time.Now().Add(-time.Hour))
	if !timedOut {
		t.Fatal("chain past hard cap must be flagged")
	}
	if msg == "" || !strings.Contains(msg, "processing cap") {
		t.Fatalf("timeout message must be user-visible and explanatory, got %q", msg)
	}
}

func TestTurnHardCapConstant(t *testing.T) {
	if TurnHardCap != 10*time.Minute {
		t.Fatalf("hard cap should be 10m, got %s", TurnHardCap)
	}
	if TurnStallAfter != 120*time.Second {
		t.Fatalf("stall threshold should be 120s, got %s", TurnStallAfter)
	}
}
