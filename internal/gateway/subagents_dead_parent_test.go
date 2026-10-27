package gateway

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// Dead-parent result loss: a sub-agent whose parent session was deleted
// (scheduler prompt-only cleanup, maintenance pruning) used to lose its
// result to a failed wake ("session not found by key") with one log line.
// finishSubAgent now detects the dead parent before the wake, WARNs with
// the result, and best-effort tells the captured channel/user.

// captureStdLog redirects the standard logger for the rest of the test
// (syncBuffer: config_reload_test.go).
func captureStdLog(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(b)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return b
}

// deadSchedulerSession creates a cron session and deletes it the way the
// scheduler's prompt-only cleanup does, returning its (now dangling) key.
func deadSchedulerSession(t *testing.T, store *sessions.Store, name string) string {
	t.Helper()
	s, err := store.GetOrCreateSession("cron", "cron_"+name+"_1")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteSessionIfPromptOnly(context.Background(), s.Key)
	if err != nil || !deleted {
		t.Fatalf("delete %s: deleted=%v err=%v", s.Key, deleted, err)
	}
	return s.Key
}

func TestSubAgentParentMissing(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	live, err := store.GetOrCreateSession(tgUser, tgChannel)
	if err != nil {
		t.Fatal(err)
	}
	dead := deadSchedulerSession(t, store, "gone")

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{"live parent", live.Key, false},
		{"deleted parent", dead, true},
		{"never existed", "telegram_nobody_123", true},
		{"no parent", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gw.subAgentParentMissing(tt.key); got != tt.want {
				t.Fatalf("subAgentParentMissing(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestFinishSubAgent_DeadParent(t *testing.T) {
	longResult := "line one\nline two " + strings.Repeat("x", 900) + "TAIL_MARKER"

	tests := []struct {
		name       string
		liveParent bool
		spawn      subAgentSpawn // parentSessionKey is filled in
		out        subAgentOutcome
		// wantWarn: substrings the WARN line must contain (nil = no WARN).
		wantWarn []string
		// wantAnnounce: substrings of the single expected announcement
		// (nil = nothing announced).
		wantAnnounce []string
		// wantWake: substring of the wake row in the parent transcript
		// ("" = parent transcript untouched).
		wantWake       string
		wantWakeSource string
	}{
		{
			name:     "dead parent, result, no channel captured: warn only",
			out:      subAgentOutcome{result: "the answer is 42"},
			wantWarn: []string{"completed", "the answer is 42"},
		},
		{
			name:         "dead parent, result, channel captured: warn + fallback announce",
			spawn:        subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser},
			out:          subAgentOutcome{result: "the answer is 42"},
			wantWarn:     []string{"completed", "the answer is 42"},
			wantAnnounce: []string{"(completed)", "parent session is gone", "the answer is 42"},
		},
		{
			name:         "dead parent, failure, channel captured: warn + fallback announce with error",
			spawn:        subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser},
			out:          subAgentOutcome{err: errors.New("tool exploded")},
			wantWarn:     []string{"failed", "tool exploded"},
			wantAnnounce: []string{"(failed)", "parent session is gone", "tool exploded"},
		},
		{
			name:     "dead parent, failure, no channel captured: warn only",
			out:      subAgentOutcome{err: errors.New("tool exploded")},
			wantWarn: []string{"failed", "tool exploded"},
		},
		{
			name:         "dead parent, announce requested: the normal announce carries the result, no duplicate",
			spawn:        subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser, announce: true},
			out:          subAgentOutcome{result: "the answer is 42"},
			wantWarn:     []string{"completed", "the answer is 42"},
			wantAnnounce: []string{"the answer is 42"},
		},
		{
			name:     "dead parent, silent result: nothing to deliver, flow unchanged",
			spawn:    subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser},
			out:      subAgentOutcome{result: "NO_REPLY"},
			wantWarn: nil,
		},
		{
			name:           "live parent, result: wake delivered, no warn, no announce",
			liveParent:     true,
			spawn:          subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser},
			out:            subAgentOutcome{result: "the answer is 42"},
			wantWake:       "the answer is 42",
			wantWakeSource: types.WakeSourceSubAgentSilent,
		},
		{
			name:           "live parent, failure: failure wake delivered, no warn",
			liveParent:     true,
			spawn:          subAgentSpawn{parentChannelID: tgChannel, parentUserID: tgUser},
			out:            subAgentOutcome{err: errors.New("tool exploded")},
			wantWake:       "Error: tool exploded",
			wantWakeSource: types.WakeSourceSubAgentFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, store := newTestGatewayWithSessions(t)
			rec := attachRecorder(t, gw)
			logs := captureStdLog(t)

			sub, err := store.GetOrCreateSession("parent", "subagent_dp_1")
			if err != nil {
				t.Fatal(err)
			}
			sp := tt.spawn
			sp.task = "compute"
			if tt.liveParent {
				p, err := store.GetOrCreateSession(tgUser, tgChannel)
				if err != nil {
					t.Fatal(err)
				}
				sp.parentSessionKey = p.Key
			} else {
				sp.parentSessionKey = deadSchedulerSession(t, store, "dp")
			}

			gw.finishSubAgent(sub.Key, sp, tt.out)
			gw.channelManager.Flush(5 * time.Second)

			// WARN line.
			var warn string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "[SubAgent] WARN") {
					warn = line
				}
			}
			if tt.wantWarn == nil {
				if warn != "" {
					t.Fatalf("unexpected WARN: %s", warn)
				}
			} else {
				if warn == "" {
					t.Fatalf("no [SubAgent] WARN line; logs:\n%s", logs.String())
				}
				for _, s := range append([]string{sub.Key, sp.parentSessionKey}, tt.wantWarn...) {
					if !strings.Contains(warn, s) {
						t.Errorf("WARN line missing %q: %s", s, warn)
					}
				}
				// The wake was skipped, not attempted and failed.
				if strings.Contains(logs.String(), "Failed to wake parent session") {
					t.Errorf("wake was attempted on a dead parent; logs:\n%s", logs.String())
				}
			}

			// Announcements.
			sent := rec.texts()
			if tt.wantAnnounce == nil {
				if len(sent) != 0 {
					t.Fatalf("unexpected announcements: %q", sent)
				}
			} else {
				if len(sent) != 1 {
					t.Fatalf("got %d announcements %q, want 1", len(sent), sent)
				}
				for _, s := range tt.wantAnnounce {
					if !strings.Contains(sent[0], s) {
						t.Errorf("announcement missing %q: %q", s, sent[0])
					}
				}
			}

			// Parent wake.
			if tt.liveParent {
				msgs, err := store.GetMessages(sp.parentSessionKey, 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(msgs) != 1 || !strings.Contains(msgs[0].Content, tt.wantWake) ||
					msgs[0].Metadata["wake_source"] != tt.wantWakeSource {
					t.Fatalf("parent transcript = %+v, want one %s wake containing %q", msgs, tt.wantWakeSource, tt.wantWake)
				}
			} else if _, err := store.GetSession(sp.parentSessionKey); err == nil {
				t.Fatal("dead parent session was resurrected")
			}
		})
	}

	t.Run("dead parent, long result: previews are truncated and single-line", func(t *testing.T) {
		gw, store := newTestGatewayWithSessions(t)
		rec := attachRecorder(t, gw)
		logs := captureStdLog(t)
		sub, err := store.GetOrCreateSession("parent", "subagent_dp_2")
		if err != nil {
			t.Fatal(err)
		}
		sp := subAgentSpawn{parentSessionKey: deadSchedulerSession(t, store, "long"),
			parentChannelID: tgChannel, parentUserID: tgUser}
		gw.finishSubAgent(sub.Key, sp, subAgentOutcome{result: longResult})
		gw.channelManager.Flush(5 * time.Second)

		if !strings.Contains(logs.String(), "[SubAgent] WARN") || !strings.Contains(logs.String(), "line two") {
			t.Fatalf("WARN missing result preview; logs:\n%s", logs.String())
		}
		if strings.Contains(logs.String(), "TAIL_MARKER") {
			t.Error("WARN preview not truncated")
		}
		sent := rec.texts()
		if len(sent) != 1 {
			t.Fatalf("got %d announcements, want 1", len(sent))
		}
		a := sent[0]
		if strings.Contains(a, "\n") {
			t.Errorf("announcement is not a single line: %q", a)
		}
		if !strings.Contains(a, "line one line two") || strings.Contains(a, "TAIL_MARKER") {
			t.Errorf("announcement preview wrong: %q", a)
		}
		if n := len([]rune(a)); n > 400 {
			t.Errorf("announcement too long (%d runes): %q", n, a)
		}
	})
}

// conduit-38cz semantics are preserved: a parent that is a canceled
// sub-agent is never woken, and the dead-parent fallback does not fire
// for it (even if its session is gone too).
func TestFinishSubAgent_CanceledSubAgentParentUnchanged(t *testing.T) {
	for _, parentDeleted := range []bool{false, true} {
		name := "canceled parent session present"
		if parentDeleted {
			name = "canceled parent session also deleted"
		}
		t.Run(name, func(t *testing.T) {
			gw, store := newTestGatewayWithSessions(t)
			rec := attachRecorder(t, gw)
			logs := captureStdLog(t)

			var parentKey string
			if parentDeleted {
				parentKey = deadSchedulerSession(t, store, "cxl")
			} else {
				p, err := store.GetOrCreateSession("root", "subagent_parent_1")
				if err != nil {
					t.Fatal(err)
				}
				parentKey = p.Key
			}
			gw.turns().RegisterSubAgent(SubAgentInfo{SessionKey: parentKey, Status: SubAgentRunning, StartedAt: time.Now()}, func() {})
			gw.turns().CancelSubAgent(parentKey, "owner", false)

			sub, err := store.GetOrCreateSession("parent", "subagent_child_1")
			if err != nil {
				t.Fatal(err)
			}
			sp := subAgentSpawn{parentSessionKey: parentKey, parentChannelID: tgChannel, parentUserID: tgUser}
			gw.finishSubAgent(sub.Key, sp, subAgentOutcome{result: "child result"})
			gw.channelManager.Flush(5 * time.Second)

			if !strings.Contains(logs.String(), "was canceled — not waking it") {
				t.Fatalf("existing canceled-parent early return not taken; logs:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), "[SubAgent] WARN") {
				t.Errorf("dead-parent WARN fired for a canceled sub-agent parent")
			}
			if sent := rec.texts(); len(sent) != 0 {
				t.Errorf("unexpected announcements: %q", sent)
			}
			if !parentDeleted {
				if msgs, _ := store.GetMessages(parentKey, 10); len(msgs) != 0 {
					t.Errorf("canceled parent was woken: %+v", msgs)
				}
			}
		})
	}
}
