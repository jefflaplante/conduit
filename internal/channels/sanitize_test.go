package channels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeOutgoingText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text passthrough",
			input:    "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name:     "reply tags stripped",
			input:    "Hello [[reply_to_current]] world",
			expected: "Hello  world",
		},
		{
			name:     "reply tag with explicit ID stripped",
			input:    "Response [[reply_to:12345]] here",
			expected: "Response  here",
		},
		{
			name:     "MEDIA line stripped",
			input:    "Some text\nMEDIA: /tmp/audio.ogg\nMore text",
			expected: "Some text\n\nMore text",
		},
		{
			name:     "multiple MEDIA lines stripped",
			input:    "Start\nMEDIA: /a.ogg\nMiddle\nMEDIA: /b.mp3\nEnd",
			expected: "Start\n\nMiddle\n\nEnd",
		},
		{
			name:     "mixed content — reply tags and MEDIA",
			input:    "[[reply_to_current]]Response\nMEDIA: /tmp/file.ogg\nDone",
			expected: "Response\n\nDone",
		},
		{
			name:     "blank line collapse",
			input:    "First\n\n\n\n\nSecond",
			expected: "First\n\nSecond",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only trimmed",
			input:    "   \n\n  ",
			expected: "",
		},
		{
			name:     "MEDIA with extra spaces in prefix",
			input:    "MEDIA:   /path/to/file.ogg",
			expected: "",
		},
		{
			name:     "MEDIA line not at start of line is preserved",
			input:    "See this MEDIA: /path line",
			expected: "See this MEDIA: /path line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeOutgoingText(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsSilentResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "exact NO_REPLY",
			input:    "NO_REPLY",
			expected: true,
		},
		{
			name:     "exact HEARTBEAT_OK",
			input:    "HEARTBEAT_OK",
			expected: true,
		},
		{
			name:     "wrapped NO_REPLY",
			input:    "Sure, I'll stay quiet. NO_REPLY",
			expected: true,
		},
		{
			name:     "wrapped HEARTBEAT_OK",
			input:    "Everything looks good. HEARTBEAT_OK. Done.",
			expected: true,
		},
		{
			name:     "case insensitive no_reply",
			input:    "no_reply",
			expected: true,
		},
		{
			name:     "case insensitive heartbeat_ok",
			input:    "Heartbeat_Ok",
			expected: true,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "normal text",
			input:    "Hello, how can I help you today?",
			expected: false,
		},
		{
			name:     "partial match should not trigger",
			input:    "HEARTBEAT is running fine",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSilentResponse(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
