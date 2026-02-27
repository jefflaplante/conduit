package channels

import (
	"testing"

	"conduit/pkg/protocol"

	"github.com/stretchr/testify/assert"
)

func TestProcessReplyTags_NoTag(t *testing.T) {
	msg := &protocol.OutgoingMessage{Text: "Hello, world!"}
	processReplyTags(msg)
	assert.Equal(t, "Hello, world!", msg.Text)
	assert.Nil(t, msg.Metadata)
}

func TestProcessReplyTags_ReplyToCurrent(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Here is my reply [[reply_to_current]]",
		Metadata: map[string]string{
			"source_message_id": "42",
		},
	}
	processReplyTags(msg)
	assert.Equal(t, "Here is my reply", msg.Text)
	assert.Equal(t, "42", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_ReplyToCurrentWithWhitespace(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Reply [[ reply_to_current ]]",
		Metadata: map[string]string{
			"source_message_id": "99",
		},
	}
	processReplyTags(msg)
	assert.Equal(t, "Reply", msg.Text)
	assert.Equal(t, "99", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_ReplyToCurrentNoSource(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Reply [[reply_to_current]]",
	}
	processReplyTags(msg)
	assert.Equal(t, "Reply", msg.Text)
	// No source_message_id available, so reply_to_message_id should not be set
	assert.Empty(t, msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_ReplyToExplicitID(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Replying to that [[reply_to:123]]",
	}
	processReplyTags(msg)
	assert.Equal(t, "Replying to that", msg.Text)
	assert.Equal(t, "123", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_ReplyToExplicitIDWithWhitespace(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Test [[ reply_to: 456 ]]",
	}
	processReplyTags(msg)
	assert.Equal(t, "Test", msg.Text)
	assert.Equal(t, "456", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_TagAtStart(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "[[reply_to_current]] Here is my reply",
		Metadata: map[string]string{
			"source_message_id": "10",
		},
	}
	processReplyTags(msg)
	assert.Equal(t, "Here is my reply", msg.Text)
	assert.Equal(t, "10", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_TagInMiddle(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Before [[reply_to:789]] after",
	}
	processReplyTags(msg)
	assert.Equal(t, "Before  after", msg.Text)
	assert.Equal(t, "789", msg.Metadata["reply_to_message_id"])
}

func TestProcessReplyTags_PreservesExistingMetadata(t *testing.T) {
	msg := &protocol.OutgoingMessage{
		Text: "Reply [[reply_to:55]]",
		Metadata: map[string]string{
			"some_key": "some_value",
		},
	}
	processReplyTags(msg)
	assert.Equal(t, "Reply", msg.Text)
	assert.Equal(t, "55", msg.Metadata["reply_to_message_id"])
	assert.Equal(t, "some_value", msg.Metadata["some_key"])
}

func TestStripReplyTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no tags", "Hello world", "Hello world"},
		{"reply_to_current at end", "Here is my reply [[reply_to_current]]", "Here is my reply"},
		{"reply_to_current at start", "[[reply_to_current]] Here is my reply", "Here is my reply"},
		{"reply_to with ID", "Text [[reply_to:123]]", "Text"},
		{"with whitespace", "Text [[ reply_to_current ]]", "Text"},
		{"empty after strip", "[[reply_to_current]]", ""},
		{"multiple tags", "[[reply_to_current]] text [[reply_to:456]]", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, StripReplyTags(tt.input))
		})
	}
}
