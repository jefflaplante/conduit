package communication

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.71: status Content lists each channel with its real status
// and no fabricated placeholders.
func TestMessageTool_StatusContent_RealShape(t *testing.T) {
	sender := newMockChannelSender()
	sender.channelStatus = map[string]string{"telegram": "online", "tui": "offline"}
	tool := NewMessageTool(newTestServices(sender))

	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "status"})
	require.NoError(t, err)
	require.True(t, res.Success)
	assert.Contains(t, res.Content, "Channel Status (2)")
	assert.Contains(t, res.Content, "- telegram: online")
	assert.Contains(t, res.Content, "- tui: offline")
	assert.NotContains(t, res.Content, "Last Activity", "no placeholder activity time")
	assert.Less(t, strings.Index(res.Content, "telegram"), strings.Index(res.Content, "tui"))
}

func TestMessageTool_BroadcastContentListsTargets(t *testing.T) {
	tool := NewMessageTool(newTestServices(newMockChannelSender()))
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "broadcast", "message": "hi", "targets": []interface{}{"telegram", "discord"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "2 targets: telegram, discord")
}

func TestMessageTool_SendContentIncludesOptions(t *testing.T) {
	tool := NewMessageTool(newTestServices(newMockChannelSender()))
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "send", "target": "telegram:123", "message": "hi", "silent": true,
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "Message sent successfully to telegram:123")
	assert.Contains(t, res.Content, "silent=true")
}

func TestMessageTool_DataNotDuplicatedForModel(t *testing.T) {
	assert.False(t, NewMessageTool(nil).IncludeDataInModelOutput())
}
