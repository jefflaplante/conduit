package nli

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewContextTracker(t *testing.T) {
	ct := NewContextTracker()
	if ct == nil {
		t.Fatal("NewContextTracker returned nil")
	}
	if ct.windowSize != defaultWindowSize {
		t.Errorf("expected window size %d, got %d", defaultWindowSize, ct.windowSize)
	}
	if ct.TurnCount() != 0 {
		t.Errorf("expected 0 turns, got %d", ct.TurnCount())
	}
}

func TestNewContextTrackerWithWindow(t *testing.T) {
	ct := NewContextTrackerWithWindow(5)
	if ct.windowSize != 5 {
		t.Errorf("expected window size 5, got %d", ct.windowSize)
	}

	// Test with invalid size.
	ct2 := NewContextTrackerWithWindow(0)
	if ct2.windowSize != 1 {
		t.Errorf("expected window size 1 for invalid input, got %d", ct2.windowSize)
	}
}

func TestUpdateContext_Basic(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("search for Go tutorials", "Here are some results...")

	if ct.TurnCount() != 1 {
		t.Errorf("expected 1 turn, got %d", ct.TurnCount())
	}
}

func TestUpdateContext_SlidingWindow(t *testing.T) {
	ct := NewContextTrackerWithWindow(3)

	for i := 0; i < 5; i++ {
		ct.UpdateContext(fmt.Sprintf("message %d", i), fmt.Sprintf("response %d", i))
	}

	if ct.TurnCount() != 3 {
		t.Errorf("expected 3 turns after window trim, got %d", ct.TurnCount())
	}

	// Verify that the oldest turns were dropped.
	turns := ct.GetRecentTurns(3)
	if turns[0].Message != "message 2" {
		t.Errorf("expected oldest turn to be 'message 2', got '%s'", turns[0].Message)
	}
}

func TestUpdateContext_EntityExtraction(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext(
		"fetch https://example.com/api",
		"The page at https://example.com/api returned data.",
	)

	entities := ct.GetActiveEntities()
	hasURL := false
	for _, e := range entities {
		if e.Type == EntityURL {
			hasURL = true
		}
	}
	if !hasURL {
		t.Error("expected URL entity from conversation turn")
	}
}

func TestResolveReference_FilePath(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("read /etc/hosts", "File contents: ...")

	resolved := ct.ResolveReference("the file")
	if resolved != "/etc/hosts" {
		t.Errorf("expected /etc/hosts, got %s", resolved)
	}
}

func TestResolveReference_URL(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("fetch https://example.com", "Page loaded successfully")

	resolved := ct.ResolveReference("the url")
	if resolved != "https://example.com" {
		t.Errorf("expected https://example.com, got %s", resolved)
	}
}

func TestResolveReference_GenericPronoun(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("check /var/log/syslog", "Found 100 entries")

	resolved := ct.ResolveReference("it")
	if resolved == "" {
		t.Error("expected generic pronoun 'it' to resolve to the most recent entity")
	}
}

func TestResolveReference_MostRecent(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("fetch https://old.example.com", "done")
	ct.UpdateContext("fetch https://new.example.com", "done")

	resolved := ct.ResolveReference("the url")
	if resolved != "https://new.example.com" {
		t.Errorf("expected most recent URL https://new.example.com, got %s", resolved)
	}
}

func TestResolveReference_NoMatch(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("hello world", "hi there")

	resolved := ct.ResolveReference("the model")
	if resolved != "" {
		t.Errorf("expected empty string for unresolvable reference, got %s", resolved)
	}
}

func TestResolveReference_Empty(t *testing.T) {
	ct := NewContextTracker()
	resolved := ct.ResolveReference("it")
	if resolved != "" {
		t.Errorf("expected empty string when no context, got %s", resolved)
	}
}

func TestGetActiveEntities_Deduplication(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("fetch https://example.com", "done")
	ct.UpdateContext("also check https://example.com", "done")

	entities := ct.GetActiveEntities()
	urlCount := 0
	for _, e := range entities {
		if e.Type == EntityURL && e.Value == "https://example.com" {
			urlCount++
		}
	}
	if urlCount != 1 {
		t.Errorf("expected 1 deduplicated URL entity, got %d", urlCount)
	}
}

func TestGetActiveEntities_MostRecentFirst(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("read /tmp/old.txt", "old content")
	ct.UpdateContext("read /tmp/new.txt", "new content")

	entities := ct.GetActiveEntities()
	if len(entities) < 2 {
		t.Fatal("expected at least 2 entities")
	}

	// Most recent entity should come first.
	if entities[0].Value != "/tmp/new.txt" {
		t.Errorf("expected most recent entity first, got %s", entities[0].Value)
	}
}

func TestGetRecentTurns(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("msg 1", "resp 1")
	ct.UpdateContext("msg 2", "resp 2")
	ct.UpdateContext("msg 3", "resp 3")

	turns := ct.GetRecentTurns(2)
	if len(turns) != 2 {
		t.Errorf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].Message != "msg 2" {
		t.Errorf("expected 'msg 2', got '%s'", turns[0].Message)
	}
	if turns[1].Message != "msg 3" {
		t.Errorf("expected 'msg 3', got '%s'", turns[1].Message)
	}

	// Request more turns than available.
	all := ct.GetRecentTurns(10)
	if len(all) != 3 {
		t.Errorf("expected 3 turns, got %d", len(all))
	}

	// Zero or negative.
	none := ct.GetRecentTurns(0)
	if none != nil {
		t.Error("expected nil for 0 turns")
	}
}

func TestClear(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("msg", "resp")
	ct.Clear()

	if ct.TurnCount() != 0 {
		t.Errorf("expected 0 turns after clear, got %d", ct.TurnCount())
	}
}

func TestContextTracker_ConcurrentAccess(t *testing.T) {
	ct := NewContextTracker()
	var wg sync.WaitGroup

	// Concurrent writes.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ct.UpdateContext(
				fmt.Sprintf("message %d with https://example%d.com", idx, idx),
				fmt.Sprintf("response %d", idx),
			)
		}(i)
	}

	// Concurrent reads.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ct.GetActiveEntities()
			_ = ct.ResolveReference("the url")
			_ = ct.TurnCount()
		}()
	}

	wg.Wait()

	// After all goroutines complete, the tracker should have at most windowSize turns.
	if ct.TurnCount() > ct.windowSize {
		t.Errorf("turn count %d exceeds window size %d", ct.TurnCount(), ct.windowSize)
	}
}

func TestResolveReference_SessionType(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("check session: my-test-session", "session is active")

	resolved := ct.ResolveReference("the session")
	if resolved != "my-test-session" {
		t.Errorf("expected 'my-test-session', got '%s'", resolved)
	}
}

func TestResolveReference_ToolName(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("run the web_search tool", "executed successfully")

	resolved := ct.ResolveReference("the tool")
	if resolved != "web_search" {
		t.Errorf("expected 'web_search', got '%s'", resolved)
	}
}

func TestResolveReference_ChannelType(t *testing.T) {
	ct := NewContextTracker()
	ct.UpdateContext("send to #general", "message sent")

	resolved := ct.ResolveReference("the channel")
	if resolved != "general" {
		t.Errorf("expected 'general', got '%s'", resolved)
	}
}

func TestReferenceTypeHints(t *testing.T) {
	tests := []struct {
		input    string
		expected EntityType
		isEmpty  bool
	}{
		{"the file", EntityFilePath, false},
		{"the url", EntityURL, false},
		{"the model", EntityModelName, false},
		{"the session", EntitySession, false},
		{"the tool", EntityToolName, false},
		{"the channel", EntityChannel, false},
		{"something random", EntityGeneric, true},
	}

	for _, tc := range tests {
		hints := referenceTypeHints(tc.input)
		if tc.isEmpty {
			if len(hints) != 0 {
				t.Errorf("expected empty hints for %q, got %v", tc.input, hints)
			}
		} else {
			if len(hints) == 0 {
				t.Errorf("expected non-empty hints for %q", tc.input)
			} else if hints[0] != tc.expected {
				t.Errorf("expected hint %s for %q, got %s", tc.expected, tc.input, hints[0])
			}
		}
	}
}

func TestIsGenericPronoun(t *testing.T) {
	generics := []string{"it", "that", "this", "them", "those", "the result", "the output", "the response"}
	for _, g := range generics {
		if !isGenericPronoun(g) {
			t.Errorf("expected %q to be a generic pronoun", g)
		}
	}

	nonGenerics := []string{"the file", "the url", "the model", "hello", ""}
	for _, ng := range nonGenerics {
		if isGenericPronoun(ng) {
			t.Errorf("expected %q not to be a generic pronoun", ng)
		}
	}
}
