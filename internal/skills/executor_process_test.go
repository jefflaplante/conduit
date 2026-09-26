//go:build unix

package skills

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// conduit-31jg.20: a timed-out skill whose command backgrounds a grandchild
// must return near the deadline, not when the grandchild exits.
func TestRunCommand_TimeoutKillsProcessGroup(t *testing.T) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "sleep 30 & sleep 30")
	start := time.Now()
	res, err := e.runCommand(ctx, cmd, Skill{Name: "t", Location: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("expected failure")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("runCommand took %s", d)
	}
	if !strings.Contains(res.Error, "timed out") {
		t.Fatalf("error = %q", res.Error)
	}
}

// conduit-31jg.20: skill output is capped.
func TestRunCommand_OutputCapped(t *testing.T) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 30})
	cmd := exec.CommandContext(context.Background(), "bash", "-c", "yes | head -c 20000000")
	res, err := e.runCommand(context.Background(), cmd, Skill{Name: "t", Location: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("unexpected failure: %s", res.Error)
	}
	if len(res.Output) > maxSkillOutputBytes+200 {
		t.Fatalf("output %d bytes exceeds cap", len(res.Output))
	}
	if !strings.Contains(res.Output, "bytes omitted") {
		t.Fatal("missing truncation marker")
	}
}
