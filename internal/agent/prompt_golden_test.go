package agent

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

var updatePromptGolden = flag.Bool("update-prompt-golden", false, "rewrite testdata/prompt_golden.txt")

// TestPromptGolden pins the exact generated system prompt text (and section
// order) for a fixed set of inputs. The system prompt is part of the cached
// system block, so refactors of the prompt builder must leave it
// byte-identical. Host-specific runtime values are normalized so the golden
// file is portable. Regenerate intentionally with:
//
//	go test ./internal/agent/ -run TestPromptGolden -update-prompt-golden
func TestPromptGolden(t *testing.T) {
	now := time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC)

	newPB := func() *PromptBuilder {
		pb := newTestPromptBuilder()
		pb.email = config.AgentEmail{
			Address:     "agent@example.com",
			DisplayName: "Conduit Agent",
			Aliases:     []string{"bot@example.com"},
		}
		pb.SetClock(func() time.Time { return now })
		return pb
	}

	cases := []struct {
		name    string
		session *sessions.Session
		isOAuth bool
	}{
		{"large_context_api_key", sessionWithModel("claude-sonnet-4-6"), false},
		{"large_context_oauth", sessionWithModel("claude-sonnet-4-6"), true},
		{"small_context_mistral", sessionWithModel("mistral"), false},
		{"tiny_context_gemma2", sessionWithModel("gemma2"), false},
		{"cron_session", &sessions.Session{
			Key:       CronSessionKeyPrefix + "job1",
			ChannelID: "cron",
			UserID:    "user1",
			Context:   map[string]string{"model": "claude-sonnet-4-6"},
		}, false},
	}

	hostname, _ := os.Hostname()
	normalize := func(s string) string {
		if hostname != "" {
			s = strings.ReplaceAll(s, hostname, "<HOST>")
		}
		s = strings.ReplaceAll(s, fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH), "<OS>")
		s = strings.ReplaceAll(s, runtime.Version(), "<GOVERSION>")
		return s
	}

	var b strings.Builder
	for _, tc := range cases {
		blocks, err := newPB().Build(context.Background(), tc.session, tc.isOAuth)
		if err != nil {
			t.Fatalf("%s: Build: %v", tc.name, err)
		}
		for i, blk := range blocks {
			fmt.Fprintf(&b, "===== %s block %d dynamic=%v =====\n", tc.name, i, blk.Dynamic)
			b.WriteString(normalize(blk.Text))
			b.WriteString("\n")
		}
	}
	got := b.String()

	path := filepath.Join("testdata", "prompt_golden.txt")
	if *updatePromptGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-prompt-golden to create): %v", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("generated prompt differs from golden at line %d:\n got: %q\nwant: %q", i+1, g, w)
			}
		}
		t.Fatal("generated prompt differs from golden")
	}
}
