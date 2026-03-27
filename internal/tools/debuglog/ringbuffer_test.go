package debuglog

import (
	"testing"
	"time"
)

func TestRingBuffer_AddAndLen(t *testing.T) {
	rb := NewRingBuffer(5)
	if rb.Len() != 0 {
		t.Fatalf("expected 0, got %d", rb.Len())
	}

	rb.Add(ToolStart("Read", nil))
	rb.Add(ToolStart("Write", nil))
	if rb.Len() != 2 {
		t.Fatalf("expected 2, got %d", rb.Len())
	}
}

func TestRingBuffer_Wrap(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Add(Entry{Type: EntryToolStart, ToolName: "A"})
	rb.Add(Entry{Type: EntryToolStart, ToolName: "B"})
	rb.Add(Entry{Type: EntryToolStart, ToolName: "C"})
	rb.Add(Entry{Type: EntryToolStart, ToolName: "D"}) // overwrites A

	if rb.Len() != 3 {
		t.Fatalf("expected 3, got %d", rb.Len())
	}

	entries := rb.Entries(nil)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.ToolName
	}
	expected := []string{"B", "C", "D"}
	for i, n := range names {
		if n != expected[i] {
			t.Errorf("entry %d: expected %q, got %q", i, expected[i], n)
		}
	}
}

func TestRingBuffer_Last(t *testing.T) {
	rb := NewRingBuffer(10)
	for i := 0; i < 7; i++ {
		rb.Add(Entry{Type: EntryToolStart, ToolName: string(rune('A' + i))})
	}
	last3 := rb.Last(3)
	if len(last3) != 3 {
		t.Fatalf("expected 3, got %d", len(last3))
	}
	if last3[0].ToolName != "E" || last3[2].ToolName != "G" {
		t.Errorf("unexpected last3: %v %v", last3[0].ToolName, last3[2].ToolName)
	}
}

func TestRingBuffer_Filter(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Add(ToolStart("Read", nil))
	rb.Add(ToolComplete("Read", 50*time.Millisecond, "ok"))
	rb.Add(ToolError("Write", 10*time.Millisecond, "permission denied"))

	errors := rb.Entries(func(e Entry) bool {
		return e.Type == EntryToolError
	})
	if len(errors) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(errors))
	}
	if errors[0].ToolName != "Write" {
		t.Errorf("expected Write, got %s", errors[0].ToolName)
	}
}

func TestRingBuffer_Clear(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Add(ToolStart("Read", nil))
	rb.Add(ToolStart("Write", nil))
	rb.Clear()
	if rb.Len() != 0 {
		t.Fatalf("expected 0 after clear, got %d", rb.Len())
	}
}

func TestConvenienceConstructors(t *testing.T) {
	e := ToolStart("Bash", map[string]interface{}{"command": "ls"})
	if e.Type != EntryToolStart || e.ToolName != "Bash" {
		t.Errorf("unexpected: %+v", e)
	}
	if e.Args["command"] != "ls" {
		t.Errorf("expected command=ls, got %v", e.Args)
	}

	e2 := ToolComplete("Bash", 100*time.Millisecond, "success")
	if e2.Type != EntryToolComplete || e2.Duration != 100*time.Millisecond {
		t.Errorf("unexpected: %+v", e2)
	}

	e3 := ToolError("Write", 5*time.Millisecond, "disk full")
	if e3.Type != EntryToolError || e3.Error != "disk full" {
		t.Errorf("unexpected: %+v", e3)
	}

	e4 := Thinking("Thinking...")
	if e4.Type != EntryThinking {
		t.Errorf("unexpected: %+v", e4)
	}

	e5 := LLMRequest("claude-sonnet-4-6", nil)
	if e5.Type != EntryLLMRequest {
		t.Errorf("unexpected: %+v", e5)
	}

	e6 := LLMResponse("claude-sonnet-4-6", "end_turn", 2*time.Second)
	if e6.Type != EntryLLMResponse || e6.Duration != 2*time.Second {
		t.Errorf("unexpected: %+v", e6)
	}
}
