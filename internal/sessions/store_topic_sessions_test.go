package sessions

import (
	"strings"
	"testing"
	"time"
)

// conduit-43vk: a topic session shares (user_id, channel_id) with the chat's
// home session so wakes route back to the same chat, but it must never be
// picked up as the chat's home session.
func TestGetOrCreateSession_IgnoresTopicSessions(t *testing.T) {
	store := newTopicTestStore(t)

	home, err := store.GetOrCreateSession("42", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	topic, err := store.GetOrCreateSessionByKey(TopicSessionKeyPrefix+"abc", "42", "telegram")
	if err != nil {
		t.Fatalf("GetOrCreateSessionByKey: %v", err)
	}
	// Make the topic session the most recently updated one.
	if _, err := store.AddMessage(topic.Key, "user", "hi", nil); err != nil {
		t.Fatal(err)
	}

	again, err := store.GetOrCreateSession("42", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	if again.Key != home.Key {
		t.Fatalf("home resolution returned %q, want home %q (topic session leaked into home)", again.Key, home.Key)
	}
}

func TestGetOrCreateSessionByKey(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
	}{
		{"creates missing", false},
		{"returns existing", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTopicTestStore(t)
			key := TopicSessionKeyPrefix + "k1"
			if tt.existing {
				if _, err := store.GetOrCreateSessionByKey(key, "42", "telegram"); err != nil {
					t.Fatal(err)
				}
				if err := store.SetSessionContext(key, "marker", "kept"); err != nil {
					t.Fatal(err)
				}
			}
			s, err := store.GetOrCreateSessionByKey(key, "42", "telegram")
			if err != nil {
				t.Fatal(err)
			}
			if s.Key != key || s.UserID != "42" || s.ChannelID != "telegram" {
				t.Fatalf("session = %+v", s)
			}
			if tt.existing && s.Context["marker"] != "kept" {
				t.Fatalf("existing session was replaced: %+v", s.Context)
			}
			if !strings.HasPrefix(s.Key, "topic_") {
				t.Fatalf("key prefix = %q", s.Key)
			}
		})
	}
}

func newTopicTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
