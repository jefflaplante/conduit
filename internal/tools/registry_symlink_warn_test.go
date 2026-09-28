package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/sandbox"
)

// conduit-31jg.73: the startup warning must say when the bounded symlink
// scan was truncated, and stay quiet about truncation otherwise.
func TestWarnEscapingSymlinks_ReportsTruncation(t *testing.T) {
	ws := t.TempDir()
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(ws, "shared")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(ws, fmt.Sprintf("f%02d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sb := sandbox.New(ws, nil)

	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	warnEscapingSymlinks(sb, sandbox.SymlinkScanOptions{}, logf)
	if len(lines) != 1 || !strings.Contains(lines[0], "shared") {
		t.Fatalf("full scan: want one link warning, got %q", lines)
	}

	lines = nil
	warnEscapingSymlinks(sb, sandbox.SymlinkScanOptions{MaxEntries: 3}, logf)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "truncated") || !strings.Contains(joined, "may be incomplete") {
		t.Fatalf("truncated scan not reported: %q", lines)
	}
}

// conduit-31jg.87: a dangling symlink is reported as dangling, not as
// resolving outside the sandbox, and allowed_paths is not suggested.
func TestWarnEscapingSymlinks_DanglingReportedDistinctly(t *testing.T) {
	ws := t.TempDir()
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(ws, "shared")); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(ws, "skills", "brain-spread-activation")
	if err := os.Symlink(missing, filepath.Join(ws, "brain_spread_activation")); err != nil {
		t.Fatal(err)
	}

	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	warnEscapingSymlinks(sandbox.New(ws, nil), sandbox.SymlinkScanOptions{}, logf)

	if len(lines) != 2 {
		t.Fatalf("want 2 warnings, got %q", lines)
	}
	var dangling, escaping string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "brain_spread_activation"):
			dangling = l
		case strings.Contains(l, "shared"):
			escaping = l
		}
	}
	if !strings.Contains(dangling, "dangling symlink (target missing)") ||
		strings.Contains(dangling, "resolves outside") || strings.Contains(dangling, "allowed_paths") {
		t.Errorf("dangling warning = %q", dangling)
	}
	if !strings.Contains(escaping, "resolves outside") || !strings.Contains(escaping, "allowed_paths") {
		t.Errorf("escaping warning = %q", escaping)
	}
}
