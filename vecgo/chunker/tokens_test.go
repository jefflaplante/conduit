package chunker

import "testing"

func TestCountTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello world", 2},
		{"one", 1},
		{"", 0},
		{"  spaced   out  ", 2},
		{"hello\nworld\ttab", 3},
	}

	for _, tt := range tests {
		got := CountTokens(tt.input)
		if got != tt.want {
			t.Errorf("CountTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestTruncateToTokens(t *testing.T) {
	text := "one two three four five"

	got := TruncateToTokens(text, 3)
	if got != "one two three" {
		t.Errorf("TruncateToTokens = %q, want 'one two three'", got)
	}

	// No truncation needed
	got2 := TruncateToTokens(text, 10)
	if got2 != text {
		t.Errorf("TruncateToTokens should not truncate: got %q", got2)
	}
}
