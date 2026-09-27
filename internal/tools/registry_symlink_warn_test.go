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
