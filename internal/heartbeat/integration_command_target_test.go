package heartbeat

import (
	"context"
	"testing"
	"time"

	"conduit/internal/scheduler"
)

// bd-conduit-alert-misroute-a0jr: maintenance replies become command actions
// with Target "system"; that marks a local action, never a chat destination.

func TestExecuteCommand_SystemTargetNotDelivered(t *testing.T) {
	cases := []struct {
		name      string
		jobTarget string
	}{
		{"no job target", ""},
		{"job has channel target", "telegram:123456789"},
		{"job has bare chat id", "123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingSender{}
			g := newTestIntegration(t, t.TempDir(), sender, time.Now())
			job := &scheduler.Job{ID: "agent_heartbeat_main", Target: tc.jobTarget}
			action := HeartbeatAction{
				Type:     ActionTypeCommand,
				Content:  "run cleanup to free disk space",
				Priority: TaskPriorityNormal,
				Target:   "system",
				Metadata: map[string]interface{}{"command": "cleanup"},
			}

			// resolveTarget keeps "system" verbatim, so the job target
			// fallback must not be what prevents the misroute.
			if got := g.resolveTarget(action.Target, job.Target); got != "system" {
				t.Fatalf("resolveTarget = %q, want %q", got, "system")
			}
			if err := g.executeCommand(context.Background(), action, job, deliverOnce); err != nil {
				t.Fatalf("executeCommand: %v", err)
			}
			if got := sender.sent(); len(got) != 0 {
				t.Fatalf("command action with target %q was sent to a channel: %v", action.Target, got)
			}
		})
	}
}

func TestExecuteCommand_ChannelTargetDelivered(t *testing.T) {
	sender := &recordingSender{}
	g := newTestIntegration(t, t.TempDir(), sender, time.Now())
	job := &scheduler.Job{ID: "agent_heartbeat_main", Target: "telegram:42"}
	action := HeartbeatAction{
		Type:     ActionTypeCommand,
		Content:  "restart the indexer",
		Priority: TaskPriorityNormal,
		Target:   "telegram:12345",
	}

	if err := g.executeCommand(context.Background(), action, job, deliverOnce); err != nil {
		t.Fatalf("executeCommand: %v", err)
	}
	got := sender.sent()
	want := "telegram:12345|🔧 Maintenance action: restart the indexer"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("sends = %v, want [%q]", got, want)
	}
}

func TestExecuteCommand_ControlAlertToTelegramStillDelivered(t *testing.T) {
	sender := &recordingSender{}
	g := newTestIntegration(t, t.TempDir(), sender, time.Now())
	job := &scheduler.Job{ID: "agent_heartbeat_main", Target: "telegram:42"}
	action := HeartbeatAction{
		Type:     ActionTypeAlert,
		Content:  "disk 95%",
		Priority: TaskPriorityNormal,
		Target:   "telegram",
	}

	if err := g.executeAction(context.Background(), action, job, deliverOnce); err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	got := sender.sent()
	want := "telegram:42|ℹ️ Alert: disk 95%"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("sends = %v, want [%q]", got, want)
	}
}

// Defense in depth: delivery actions can also carry non-chat targets
// (extractDeliveryTarget returns "email", "user", ...), so the channel
// deliverer refuses telegram user IDs the adapter could never parse.
func TestChannelSenderDeliverer_TelegramChatIDValidation(t *testing.T) {
	cases := []struct {
		target   string
		wantSent string // empty = must be rejected without sending
	}{
		{"system", ""},
		{"email", ""},
		{"telegram", ""},
		{"telegram:admin", ""},
		{"telegram:", ""},
		{"telegram:12345", "telegram:12345|hi"},
		{"12345", "telegram:12345|hi"},
		{"telegram:-1001234567890", "telegram:-1001234567890|hi"},
		{"tui:client-a", "tui:client-a|hi"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			sender := &recordingSender{}
			d := NewChannelSenderDeliverer(sender)
			tgt, err := channelTarget(tc.target)
			if tc.wantSent == "" {
				// Rejection may happen at target construction (channelTarget)
				// or at delivery; either way nothing may reach the channel
				// sender.
				if err == nil {
					err = d.Deliver(context.Background(), Alert{Message: "hi"}, tgt)
				}
				if err == nil {
					t.Fatalf("target %q was accepted end-to-end, want rejection", tc.target)
				}
				if len(sender.sent()) != 0 {
					t.Fatalf("target %q reached the channel sender: %v", tc.target, sender.sent())
				}
				return
			}
			if err != nil {
				t.Fatalf("channelTarget(%q): %v", tc.target, err)
			}
			if err := d.Deliver(context.Background(), Alert{Message: "hi"}, tgt); err != nil {
				t.Fatalf("Deliver(%q): %v", tc.target, err)
			}
			got := sender.sent()
			if len(got) != 1 || got[0] != tc.wantSent {
				t.Fatalf("sends = %v, want [%q]", got, tc.wantSent)
			}
		})
	}
}
