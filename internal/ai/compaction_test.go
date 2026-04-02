package ai

import (
	"context"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompactionEngine_ShouldCompact(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		threshold    float64
		promptTokens int
		model        string
		expected     bool
	}{
		{
			name:         "disabled always returns false",
			enabled:      false,
			threshold:    0.70,
			promptTokens: 150000,
			model:        "claude-sonnet-4-20250514",
			expected:     false,
		},
		{
			name:         "below threshold returns false",
			enabled:      true,
			threshold:    0.70,
			promptTokens: 100000, // 50% of 200k
			model:        "claude-sonnet-4-20250514",
			expected:     false,
		},
		{
			name:         "at threshold returns true",
			enabled:      true,
			threshold:    0.70,
			promptTokens: 140000, // 70% of 200k
			model:        "claude-sonnet-4-20250514",
			expected:     true,
		},
		{
			name:         "above threshold returns true",
			enabled:      true,
			threshold:    0.70,
			promptTokens: 180000, // 90% of 200k
			model:        "claude-sonnet-4-20250514",
			expected:     true,
		},
		{
			name:         "default threshold 70% when not set",
			enabled:      true,
			threshold:    0, // should default to 0.70
			promptTokens: 150000,
			model:        "claude-sonnet-4-20250514",
			expected:     true,
		},
		{
			name:         "smaller model context window",
			enabled:      true,
			threshold:    0.70,
			promptTokens: 80000, // ~62% of 128k
			model:        "gpt-4-turbo",
			expected:     false,
		},
		{
			name:         "smaller model over threshold",
			enabled:      true,
			threshold:    0.70,
			promptTokens: 100000, // ~78% of 128k
			model:        "gpt-4-turbo",
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.CompactionConfig{
				Enabled:   tt.enabled,
				Threshold: tt.threshold,
			}
			ce := NewCompactionEngine(nil, nil, cfg)

			result := ce.ShouldCompact(tt.promptTokens, tt.model)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCompactionConfig_Defaults(t *testing.T) {
	cfg := config.DefaultCompactionConfig()

	assert.False(t, cfg.Enabled, "default should be disabled")
	assert.Equal(t, 0.70, cfg.Threshold, "default threshold should be 70%")
	assert.Equal(t, "claude-haiku-4-5-20251001", cfg.Model, "default model should be haiku")
	assert.Equal(t, 10, cfg.RecentMessagesToKeep, "default recent messages should be 10")
}

func TestCompactionEngine_Compact_NotEnoughMessages(t *testing.T) {
	// Create a temporary database
	store, err := sessions.NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Create a session with few messages
	session, err := store.GetOrCreateSession("user1", "channel1")
	require.NoError(t, err)

	// Add only 5 messages (below the default 10 threshold)
	for i := 0; i < 5; i++ {
		_, err := store.AddMessage(session.Key, "user", "test message", nil)
		require.NoError(t, err)
	}

	cfg := config.CompactionConfig{
		Enabled:              true,
		Threshold:            0.70,
		RecentMessagesToKeep: 10,
	}
	ce := NewCompactionEngine(nil, store, cfg)

	// Should return nil (no compaction needed)
	result, err := ce.Compact(context.Background(), session)
	assert.NoError(t, err)
	assert.Nil(t, result, "should return nil when not enough messages")
}

func TestCompactionEngine_Compact_MessageReplacement(t *testing.T) {
	// This test verifies the message replacement logic without actually calling the AI
	// We test that the store operations work correctly

	store, err := sessions.NewStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Create a session
	session, err := store.GetOrCreateSession("user1", "channel1")
	require.NoError(t, err)

	// Add 15 messages
	for i := 0; i < 15; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_, err := store.AddMessage(session.Key, role, "message content "+string(rune('A'+i)), nil)
		require.NoError(t, err)
	}

	// Verify we have 15 messages
	messages, err := store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Len(t, messages, 15)

	// Test that ClearSessionMessages works
	err = store.ClearSessionMessages(session.Key)
	require.NoError(t, err)

	messages, err = store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Len(t, messages, 0)

	// Test that we can add new messages after clearing
	_, err = store.AddMessage(session.Key, "assistant", "[Summary]", nil)
	require.NoError(t, err)

	_, err = store.AddMessage(session.Key, "user", "recent message", nil)
	require.NoError(t, err)

	messages, err = store.GetMessages(session.Key, 100)
	require.NoError(t, err)
	assert.Len(t, messages, 2)
	assert.Equal(t, "[Summary]", messages[0].Content)
	assert.Equal(t, "recent message", messages[1].Content)
}
