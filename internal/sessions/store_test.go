package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Create temporary database file
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Verify the database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestGetOrCreateSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	userID := "user123"
	channelID := "channel456"

	// Create a session
	session, err := store.GetOrCreateSession(userID, channelID)
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	if session.Key == "" {
		t.Error("Expected session key to be set")
	}

	if session.UserID != userID {
		t.Errorf("Expected user ID %s, got %s", userID, session.UserID)
	}

	if session.ChannelID != channelID {
		t.Errorf("Expected channel ID %s, got %s", channelID, session.ChannelID)
	}

	// Retrieve the session
	retrievedSession, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrievedSession.Key != session.Key {
		t.Errorf("Expected session key %s, got %s", session.Key, retrievedSession.Key)
	}
}

func TestAddAndRetrieveMessage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session
	session, err := store.GetOrCreateSession("user123", "channel456")
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	// Add a message
	metadata := make(map[string]string)
	metadata["source"] = "test"

	message, err := store.AddMessage(session.Key, "user", "Hello, world!", metadata)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	if message.ID == "" {
		t.Error("Expected message ID to be set")
	}

	if message.Role != "user" {
		t.Errorf("Expected message role 'user', got %s", message.Role)
	}

	if message.Content != "Hello, world!" {
		t.Errorf("Expected message content 'Hello, world!', got %s", message.Content)
	}

	// Retrieve messages
	messages, err := store.GetMessages(session.Key, 0) // 0 = no limit
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Role != "user" {
		t.Errorf("Expected message role 'user', got %s", messages[0].Role)
	}

	if messages[0].Content != "Hello, world!" {
		t.Errorf("Expected message content 'Hello, world!', got %s", messages[0].Content)
	}
}

func TestGetNonExistentSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	_, err = store.GetSession("non-existent-session")
	if err == nil {
		t.Error("Expected error when getting non-existent session")
	}
}

func TestGetLatestSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	userID := "user123"
	channelID := "channel456"

	// Create first session
	session1, err := store.GetOrCreateSession(userID, channelID)
	if err != nil {
		t.Fatalf("Failed to create first session: %v", err)
	}

	// Add a message to create some activity
	_, err = store.AddMessage(session1.Key, "user", "First message", nil)
	if err != nil {
		t.Fatalf("Failed to add message to first session: %v", err)
	}

	time.Sleep(2 * time.Millisecond) // Ensure different timestamps

	// Create second session (this will be a new session since GetOrCreate
	// may return the same session. We need to manually create one)
	userID2 := "user456" // Use different user to force new session
	session2, err := store.GetOrCreateSession(userID2, channelID)
	if err != nil {
		t.Fatalf("Failed to create second session: %v", err)
	}

	// Add a message to the second session
	_, err = store.AddMessage(session2.Key, "user", "Second message", nil)
	if err != nil {
		t.Fatalf("Failed to add message to second session: %v", err)
	}

	// Get latest session for second user
	latestSession, err := store.GetLatestSession(userID2, channelID)
	if err != nil {
		t.Fatalf("Failed to get latest session: %v", err)
	}

	// Should return the session for user2
	if latestSession.Key != session2.Key {
		t.Errorf("Expected latest session key %s, got %s", session2.Key, latestSession.Key)
	}
}

func TestAddMultipleMessages(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("user123", "channel456")
	if err != nil {
		t.Fatalf("Failed to get or create session: %v", err)
	}

	testMessages := []struct {
		role    string
		content string
	}{
		{"user", "First message"},
		{"assistant", "Second message"},
		{"user", "Third message"},
	}

	// Add all messages
	for _, msg := range testMessages {
		_, err = store.AddMessage(session.Key, msg.role, msg.content, nil)
		if err != nil {
			t.Fatalf("Failed to add message '%s': %v", msg.content, err)
		}
	}

	// Retrieve messages
	retrievedMessages, err := store.GetMessages(session.Key, 0) // 0 = no limit
	if err != nil {
		t.Fatalf("Failed to get messages: %v", err)
	}

	if len(retrievedMessages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(retrievedMessages))
	}

	// Verify message content and order (messages should be in timestamp order)
	for i, expectedMsg := range testMessages {
		if retrievedMessages[i].Role != expectedMsg.role {
			t.Errorf("Message %d: expected role %s, got %s", i, expectedMsg.role, retrievedMessages[i].Role)
		}
		if retrievedMessages[i].Content != expectedMsg.content {
			t.Errorf("Message %d: expected content %s, got %s", i, expectedMsg.content, retrievedMessages[i].Content)
		}
	}

	// Test session message count was updated
	updatedSession, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get updated session: %v", err)
	}

	if updatedSession.MessageCount != 3 {
		t.Errorf("Expected session message count 3, got %d", updatedSession.MessageCount)
	}
}

func TestGetSessionByLabel(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session and set a label
	session, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	err = store.SetSessionContext(session.Key, "label", "my-agent")
	if err != nil {
		t.Fatalf("Failed to set label: %v", err)
	}

	// Lookup by label
	found, err := store.GetSessionByLabel("my-agent")
	if err != nil {
		t.Fatalf("Failed to get session by label: %v", err)
	}

	if found.Key != session.Key {
		t.Errorf("Expected session key %s, got %s", session.Key, found.Key)
	}

	// Lookup non-existent label
	_, err = store.GetSessionByLabel("nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent label")
	}

	// Empty label
	_, err = store.GetSessionByLabel("")
	if err == nil {
		t.Error("Expected error for empty label")
	}
}

func TestSearchMessages(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create sessions and add messages
	session1, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session1: %v", err)
	}

	session2, err := store.GetOrCreateSession("user2", "channel2")
	if err != nil {
		t.Fatalf("Failed to create session2: %v", err)
	}

	// Add messages with searchable content
	_, err = store.AddMessage(session1.Key, "user", "The weather today is sunny and warm", nil)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	_, err = store.AddMessage(session1.Key, "assistant", "I hope you enjoy the sunshine!", nil)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	_, err = store.AddMessage(session2.Key, "user", "Can you help me with database queries?", nil)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	// Test FTS search for "weather"
	results, err := store.SearchMessages("weather", 10)
	if err != nil {
		t.Fatalf("SearchMessages failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'weather', got %d", len(results))
	}

	if len(results) > 0 && results[0].SessionKey != session1.Key {
		t.Errorf("Expected result from session1, got %s", results[0].SessionKey)
	}

	// Test search for "database"
	results, err = store.SearchMessages("database", 10)
	if err != nil {
		t.Fatalf("SearchMessages failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'database', got %d", len(results))
	}

	if len(results) > 0 && results[0].SessionKey != session2.Key {
		t.Errorf("Expected result from session2, got %s", results[0].SessionKey)
	}

	// Test search for "sunshine" (in assistant message)
	results, err = store.SearchMessages("sunshine", 10)
	if err != nil {
		t.Fatalf("SearchMessages failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'sunshine', got %d", len(results))
	}

	// Test search for non-existent term
	results, err = store.SearchMessages("xyznonexistent", 10)
	if err != nil {
		t.Fatalf("SearchMessages failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected 0 results for non-existent term, got %d", len(results))
	}

	// Test empty query
	results, err = store.SearchMessages("", 10)
	if err != nil {
		t.Fatalf("SearchMessages with empty query failed: %v", err)
	}

	if results != nil && len(results) != 0 {
		t.Errorf("Expected nil or empty results for empty query, got %d", len(results))
	}

	// Test limit
	results, err = store.SearchMessages("the", 1)
	if err != nil {
		t.Fatalf("SearchMessages with limit failed: %v", err)
	}

	if len(results) > 1 {
		t.Errorf("Expected at most 1 result with limit=1, got %d", len(results))
	}
}

func TestSetSessionContext_ConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Limit to 1 open connection so the busy_timeout PRAGMA applies everywhere
	// and concurrent goroutines queue up instead of getting SQLITE_BUSY.
	store.db.SetMaxOpenConns(1)

	session, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Spawn N goroutines, each setting a unique key concurrently.
	// Because json_set is atomic per statement, no key should be lost.
	const N = 20
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			key := fmt.Sprintf("key_%d", idx)
			value := fmt.Sprintf("value_%d", idx)
			errs <- store.SetSessionContext(session.Key, key, value)
		}(i)
	}

	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("SetSessionContext goroutine %d failed: %v", i, err)
		}
	}

	// Verify all keys are present
	finalSession, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get final session: %v", err)
	}

	for i := 0; i < N; i++ {
		key := fmt.Sprintf("key_%d", i)
		expected := fmt.Sprintf("value_%d", i)
		if got, ok := finalSession.Context[key]; !ok {
			t.Errorf("Missing key %s in context", key)
		} else if got != expected {
			t.Errorf("Key %s: expected %s, got %s", key, expected, got)
		}
	}
}

func TestSetSessionContextBatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Batch set 5 keys
	batch := map[string]string{
		"last_prompt_tokens":     "100",
		"last_completion_tokens": "200",
		"last_total_tokens":      "300",
		"session_total_cost":     "0.005000",
		"session_request_count":  "1",
	}
	if err := store.SetSessionContextBatch(session.Key, batch); err != nil {
		t.Fatalf("SetSessionContextBatch failed: %v", err)
	}

	// Verify all keys are present
	updated, err := store.GetSession(session.Key)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}
	for k, want := range batch {
		if got := updated.Context[k]; got != want {
			t.Errorf("Key %s: got %q, want %q", k, got, want)
		}
	}
}

func TestSetSessionContextBatch_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Empty map should be a no-op
	if err := store.SetSessionContextBatch("any-key", map[string]string{}); err != nil {
		t.Fatalf("Expected no error for empty batch, got: %v", err)
	}
}

func TestIncrementMessageCount(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add 5 messages
	for i := 0; i < 5; i++ {
		_, err = store.AddMessage(session.Key, "user", fmt.Sprintf("msg %d", i), nil)
		if err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	s, _ := store.GetSession(session.Key)
	if s.MessageCount != 5 {
		t.Errorf("Expected 5 messages, got %d", s.MessageCount)
	}

	// Clear messages and add 2 more
	if err := store.ClearSessionMessages(session.Key); err != nil {
		t.Fatalf("Failed to clear messages: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err = store.AddMessage(session.Key, "user", fmt.Sprintf("new msg %d", i), nil)
		if err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	s, _ = store.GetSession(session.Key)
	if s.MessageCount != 2 {
		t.Errorf("After clear + 2 adds, expected 2 messages, got %d", s.MessageCount)
	}
}

func TestSearchMessagesFTSTriggers(t *testing.T) {
	// This test verifies that FTS triggers keep the index in sync
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	session, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Add a message - should be indexed via trigger
	_, err = store.AddMessage(session.Key, "user", "unique_test_keyword_alpha", nil)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	// Search should find it immediately (trigger should have indexed it)
	results, err := store.SearchMessages("unique_test_keyword_alpha", 10)
	if err != nil {
		t.Fatalf("SearchMessages failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result after INSERT trigger, got %d", len(results))
	}
}

func TestGetIdleSessions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create session with enough messages to be "substantive"
	sess1, err := store.GetOrCreateSession("user1", "channel1")
	if err != nil {
		t.Fatalf("Failed to create session 1: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := store.AddMessage(sess1.Key, "user", fmt.Sprintf("msg %d", i), nil); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Create session with too few messages (should not appear)
	sess2, err := store.GetOrCreateSession("user2", "channel2")
	if err != nil {
		t.Fatalf("Failed to create session 2: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.AddMessage(sess2.Key, "user", fmt.Sprintf("msg %d", i), nil); err != nil {
			t.Fatalf("Failed to add message: %v", err)
		}
	}

	// Both sessions were just updated, so a future cutoff should return nothing
	futureKeys, err := store.GetIdleSessions(time.Now().Add(-1*time.Hour), 5)
	if err != nil {
		t.Fatalf("GetIdleSessions failed: %v", err)
	}
	if len(futureKeys) != 0 {
		t.Errorf("Expected 0 idle sessions with past cutoff, got %d", len(futureKeys))
	}

	// A cutoff in the future should return the substantive session
	keys, err := store.GetIdleSessions(time.Now().Add(1*time.Hour), 5)
	if err != nil {
		t.Fatalf("GetIdleSessions failed: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("Expected 1 idle session, got %d", len(keys))
	}
	if keys[0] != sess1.Key {
		t.Errorf("Expected session key %s, got %s", sess1.Key, keys[0])
	}

	// Verify sess2 (3 messages, <= 5 threshold) is not returned
	allKeys, err := store.GetIdleSessions(time.Now().Add(1*time.Hour), 2)
	if err != nil {
		t.Fatalf("GetIdleSessions failed: %v", err)
	}
	// Both should appear with lower threshold
	if len(allKeys) != 2 {
		t.Errorf("Expected 2 sessions with minMessages=2, got %d", len(allKeys))
	}
}
