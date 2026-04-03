package constants

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSilentReplyToken_Value(t *testing.T) {
	assert.Equal(t, "NO_REPLY", SilentReplyToken)
}

func TestHeartbeatOKToken_Value(t *testing.T) {
	assert.Equal(t, "HEARTBEAT_OK", HeartbeatOKToken)
}

func TestSilentResponseTokens_Contains(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "contains SilentReplyToken",
			token:    SilentReplyToken,
			expected: true,
		},
		{
			name:     "contains HeartbeatOKToken",
			token:    HeartbeatOKToken,
			expected: true,
		},
		{
			name:     "does not contain arbitrary string",
			token:    "SOME_OTHER_TOKEN",
			expected: false,
		},
		{
			name:     "does not contain empty string",
			token:    "",
			expected: false,
		},
		{
			name:     "case sensitive NO_REPLY",
			token:    "no_reply",
			expected: false,
		},
		{
			name:     "case sensitive HEARTBEAT_OK",
			token:    "heartbeat_ok",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, silentToken := range SilentResponseTokens {
				if silentToken == tt.token {
					found = true
					break
				}
			}
			assert.Equal(t, tt.expected, found)
		})
	}
}

func TestSilentResponseTokens_Length(t *testing.T) {
	// Should contain exactly the two defined tokens
	require.Len(t, SilentResponseTokens, 2)
}

func TestSilentResponseTokens_Order(t *testing.T) {
	// Verify the order matches the constant definitions
	require.GreaterOrEqual(t, len(SilentResponseTokens), 2)
	assert.Equal(t, SilentReplyToken, SilentResponseTokens[0])
	assert.Equal(t, HeartbeatOKToken, SilentResponseTokens[1])
}

func TestSilentResponseTokens_NotEmpty(t *testing.T) {
	// Ensure no empty tokens in the slice
	for i, token := range SilentResponseTokens {
		assert.NotEmpty(t, token, "token at index %d should not be empty", i)
	}
}

func TestSupportedChannels_Contains(t *testing.T) {
	tests := []struct {
		name     string
		channel  string
		expected bool
	}{
		{
			name:     "contains telegram",
			channel:  "telegram",
			expected: true,
		},
		{
			name:     "contains whatsapp",
			channel:  "whatsapp",
			expected: true,
		},
		{
			name:     "contains discord",
			channel:  "discord",
			expected: true,
		},
		{
			name:     "contains googlechat",
			channel:  "googlechat",
			expected: true,
		},
		{
			name:     "contains slack",
			channel:  "slack",
			expected: true,
		},
		{
			name:     "contains signal",
			channel:  "signal",
			expected: true,
		},
		{
			name:     "contains imessage",
			channel:  "imessage",
			expected: true,
		},
		{
			name:     "does not contain unknown channel",
			channel:  "unknown",
			expected: false,
		},
		{
			name:     "does not contain empty string",
			channel:  "",
			expected: false,
		},
		{
			name:     "case sensitive Telegram",
			channel:  "Telegram",
			expected: false,
		},
		{
			name:     "case sensitive TELEGRAM",
			channel:  "TELEGRAM",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, ch := range SupportedChannels {
				if ch == tt.channel {
					found = true
					break
				}
			}
			assert.Equal(t, tt.expected, found)
		})
	}
}

func TestSupportedChannels_Length(t *testing.T) {
	// Should contain exactly 7 channels
	require.Len(t, SupportedChannels, 7)
}

func TestSupportedChannels_AllLowercase(t *testing.T) {
	// All channel names should be lowercase
	for _, channel := range SupportedChannels {
		assert.Equal(t, channel, stringToLower(channel),
			"channel %q should be lowercase", channel)
	}
}

func TestSupportedChannels_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	for _, channel := range SupportedChannels {
		assert.False(t, seen[channel], "duplicate channel found: %s", channel)
		seen[channel] = true
	}
}

func TestSupportedChannels_NotEmpty(t *testing.T) {
	// Ensure no empty channel names
	for i, channel := range SupportedChannels {
		assert.NotEmpty(t, channel, "channel at index %d should not be empty", i)
	}
}

func TestSupportedChannels_KnownChannels(t *testing.T) {
	// Verify all expected channels are present
	expectedChannels := []string{
		"telegram",
		"whatsapp",
		"discord",
		"googlechat",
		"slack",
		"signal",
		"imessage",
	}

	for _, expected := range expectedChannels {
		found := false
		for _, ch := range SupportedChannels {
			if ch == expected {
				found = true
				break
			}
		}
		assert.True(t, found, "expected channel %q not found in SupportedChannels", expected)
	}
}

// stringToLower is a simple helper to convert string to lowercase
// without importing strings package to keep test dependencies minimal
func stringToLower(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		result[i] = c
	}
	return string(result)
}

func TestConstants_Immutability(t *testing.T) {
	// Test that the constants have expected values
	// This serves as a regression test to catch accidental changes
	t.Run("SilentReplyToken is NO_REPLY", func(t *testing.T) {
		assert.Equal(t, "NO_REPLY", SilentReplyToken)
	})

	t.Run("HeartbeatOKToken is HEARTBEAT_OK", func(t *testing.T) {
		assert.Equal(t, "HEARTBEAT_OK", HeartbeatOKToken)
	})
}

func TestSilentResponseTokens_MatchesConstants(t *testing.T) {
	// Verify that SilentResponseTokens contains exactly the defined constants
	tokenMap := make(map[string]bool)
	for _, token := range SilentResponseTokens {
		tokenMap[token] = true
	}

	assert.True(t, tokenMap[SilentReplyToken],
		"SilentResponseTokens should contain SilentReplyToken")
	assert.True(t, tokenMap[HeartbeatOKToken],
		"SilentResponseTokens should contain HeartbeatOKToken")
	assert.Len(t, tokenMap, 2,
		"SilentResponseTokens should contain exactly 2 unique tokens")
}
