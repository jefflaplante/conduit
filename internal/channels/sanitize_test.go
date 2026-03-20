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
		{
			name:     "trailing NO_REPLY stripped",
			input:    "Here is your cron job output. NO_REPLY",
			expected: "Here is your cron job output.",
		},
		{
			name:     "trailing HEARTBEAT_OK stripped",
			input:    "System status is normal. HEARTBEAT_OK",
			expected: "System status is normal.",
		},
		{
			name:     "trailing NO_REPLY with extra whitespace stripped",
			input:    "Done processing.   NO_REPLY   ",
			expected: "Done processing.",
		},
		{
			name:     "trailing NO_REPLY case insensitive",
			input:    "Task complete. no_reply",
			expected: "Task complete.",
		},
		{
			name:     "mid-text NO_REPLY preserved",
			input:    "The NO_REPLY token is used for silent responses.",
			expected: "The NO_REPLY token is used for silent responses.",
		},
		{
			name:     "mid-text HEARTBEAT_OK preserved",
			input:    "When status is good, respond with HEARTBEAT_OK instead of verbose output.",
			expected: "When status is good, respond with HEARTBEAT_OK instead of verbose output.",
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
			name:     "short wrapped NO_REPLY",
			input:    "OK. NO_REPLY",
			expected: true,
		},
		{
			name:     "short wrapped HEARTBEAT_OK",
			input:    "All good. HEARTBEAT_OK",
			expected: true,
		},
		{
			name:     "long response containing NO_REPLY not suppressed",
			input:    "Sure, I'll stay quiet. NO_REPLY is what I would normally say but here is more text instead.",
			expected: false,
		},
		{
			name:     "long response containing HEARTBEAT_OK not suppressed",
			input:    "Everything looks good. HEARTBEAT_OK. But let me tell you more about the status of things.",
			expected: false,
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
