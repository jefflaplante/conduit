package communication

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.73: send failures must carry their suggestions in
// ErrorDetails (rendered for failed results), not only in Data (never shown
// on failure).
func TestSendErrors_SuggestionsInErrorDetails(t *testing.T) {
	failing := newMockChannelSender()
	failing.sendError = errors.New("chat not found")

	cases := []struct {
		name     string
		tool     *MessageTool
		args     map[string]interface{}
		wantType string
	}{
		{"missing target", NewMessageTool(newTestServices(newMockChannelSender())),
			map[string]interface{}{"action": "send", "message": "hi"}, "missing_parameter"},
		{"missing message", NewMessageTool(newTestServices(newMockChannelSender())),
			map[string]interface{}{"action": "send", "target": "telegram"}, "missing_parameter"},
		{"no sender", NewMessageTool(nil),
			map[string]interface{}{"action": "send", "target": "telegram", "message": "hi"}, "service_unavailable"},
		{"send failed", NewMessageTool(newTestServices(failing)),
			map[string]interface{}{"action": "send", "target": "telegram", "message": "hi"}, "invalid_parameter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.tool.Execute(context.Background(), c.args)
			require.NoError(t, err)
			require.False(t, res.Success)
			require.NotNil(t, res.ErrorDetails, "failed send must populate ErrorDetails")
			assert.Equal(t, c.wantType, res.ErrorDetails.Type)
			assert.NotEmpty(t, res.ErrorDetails.Suggestions)
			// Data kept for compatibility.
			assert.Equal(t, c.wantType, res.Data["error_type"])
		})
	}
}
