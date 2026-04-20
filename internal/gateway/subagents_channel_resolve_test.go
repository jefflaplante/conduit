package gateway

import (
	"path/filepath"
	"testing"

	"conduit/internal/sessions"
)

// TestResolveAnnounceChannelID covers conduit-1qyk: the sub-agent announce path
// should read the parent's current ChannelID from the session store rather than
// relying on the channel captured at spawn time.
func TestResolveAnnounceChannelID(t *testing.T) {
	tmp := t.TempDir()
	store, err := sessions.NewStore(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	parent, err := store.GetOrCreateSession("user1", "tui_live")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	gw := &Gateway{sessions: store}

	t.Run("live session channel wins over stale capture", func(t *testing.T) {
		got := gw.resolveAnnounceChannelID(parent.Key, "tui_stale")
		if got != "tui_live" {
			t.Fatalf("resolveAnnounceChannelID = %q, want %q", got, "tui_live")
		}
	})

	t.Run("falls back to captured channel when session lookup fails", func(t *testing.T) {
		got := gw.resolveAnnounceChannelID("unknown-session-key", "tui_stale")
		if got != "tui_stale" {
			t.Fatalf("resolveAnnounceChannelID = %q, want fallback %q", got, "tui_stale")
		}
	})

	t.Run("empty session key falls through to captured", func(t *testing.T) {
		got := gw.resolveAnnounceChannelID("", "tui_stale")
		if got != "tui_stale" {
			t.Fatalf("resolveAnnounceChannelID = %q, want %q", got, "tui_stale")
		}
	})

	t.Run("no data anywhere returns empty", func(t *testing.T) {
		got := gw.resolveAnnounceChannelID("", "")
		if got != "" {
			t.Fatalf("resolveAnnounceChannelID = %q, want empty", got)
		}
	})
}
