package agent

import (
	"strings"
	"testing"

	"conduit/internal/sessions"
)

// H3: Telegram puts the chat id in Session.UserID (ChannelID is the adapter
// id, e.g. "telegram"). Group and supergroup chat ids are negative; those
// chats are shared and must not get MEMORY.md or the other main-only files.
func TestWorkspaceContext_GroupChatsAreShared(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		userID    string
		main      bool
	}{
		{"telegram DM", "telegram", "123456789", true},
		{"telegram group", "telegram", "-123", false},
		{"telegram supergroup", "telegram", "-100123", false},
		{"custom telegram adapter id, supergroup", "tg-home", "-1001234567890", false},
		{"TUI", "tui_owner", "owner", true},
		{"WS new session", "tui_owner_1730000000000000000", "owner", true},
		{"group marker in channel id", "acme-group", "owner", false},
	}
	pb, _ := newProfilePromptBuilder(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &sessions.Session{
				Key:       tt.channelID + "_" + tt.userID + "_abcd1234",
				ChannelID: tt.channelID,
				UserID:    tt.userID,
				Context:   map[string]string{"model": "claude-sonnet-4-6"},
			}
			prompt := buildProfileStatic(t, pb, s)
			if !strings.Contains(prompt, "soul-marker") {
				t.Fatalf("SOUL.md missing; workspace context not loaded")
			}
			for _, mainOnly := range []string{"memory-marker", "bootstrap-marker"} {
				if got := strings.Contains(prompt, mainOnly); got != tt.main {
					t.Errorf("%s in prompt = %v, want %v (main=%v)", mainOnly, got, tt.main, tt.main)
				}
			}
		})
	}
}

func TestIsSharedChat(t *testing.T) {
	for _, tc := range []struct {
		channelID, userID string
		want              bool
	}{
		{"telegram", "123456789", false},
		{"telegram", "-123", true},
		{"telegram", "-100123", true},
		{"telegram", " -42", true},
		{"tui_owner", "owner", false},
		{"cron_job_a", "cron", false},
		{"", "", false},
		{"acme-group", "owner", true},
		{"x-1001", "owner", true},
	} {
		if got := isSharedChat(tc.channelID, tc.userID); got != tc.want {
			t.Errorf("isSharedChat(%q, %q) = %v, want %v", tc.channelID, tc.userID, got, tc.want)
		}
	}
}
