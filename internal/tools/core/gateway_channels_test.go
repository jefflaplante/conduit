package core

import (
	"context"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChannelsGateway implements only the channel parts of the gateway
// service; the embedded (nil) GatewayService is never reached by the
// channels/enable_channel/disable_channel actions.
type fakeChannelsGateway struct {
	types.GatewayService
	channels map[string]interface{}
	enabled  []string
	disabled []string
}

func (f *fakeChannelsGateway) GetChannelStatus() (map[string]interface{}, error) {
	return f.channels, nil
}

func (f *fakeChannelsGateway) EnableChannel(_ context.Context, channelID string) error {
	f.enabled = append(f.enabled, channelID)
	return nil
}

func (f *fakeChannelsGateway) DisableChannel(_ context.Context, channelID string) error {
	f.disabled = append(f.disabled, channelID)
	return nil
}

func TestGatewayChannels_ActionRegisteredAndDispatched(t *testing.T) {
	gw := &fakeChannelsGateway{channels: map[string]interface{}{
		"telegram": map[string]interface{}{"status": "connected", "message": "ok"},
	}}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw})

	// "channels" must be a registered enum value in the tool schema.
	props := tool.Parameters()["properties"].(map[string]interface{})
	enum := props["action"].(map[string]interface{})["enum"].([]string)
	require.Contains(t, enum, "channels")
	require.Contains(t, enum, "enable_channel")
	require.Contains(t, enum, "disable_channel")

	// ...and in the action docs registry.
	docs := tool.GetActionDocs()
	require.Contains(t, docs, "channels")
	assert.Equal(t, []string{"channelId"}, docs["enable_channel"].RequiredParams)
	assert.Equal(t, []string{"channelId"}, docs["disable_channel"].RequiredParams)

	// Dispatch must reach the channels handler (proven by the fake's data
	// flowing back out through formatChannelStatus).
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "channels"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Success, "channels should succeed, got: %s", res.Error)
	assert.Contains(t, res.Content, "telegram")
	assert.Contains(t, res.Content, "connected")
	assert.Contains(t, res.Content, "ok")
}

func TestGatewayChannels_EmptyChannelList(t *testing.T) {
	gw := &fakeChannelsGateway{channels: map[string]interface{}{}}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "channels"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Success)
	assert.Contains(t, res.Content, "No channels configured.")
}

func TestGatewayChannels_EnableDisableRequireChannelID(t *testing.T) {
	gw := &fakeChannelsGateway{}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw})

	for _, action := range []string{"enable_channel", "disable_channel"} {
		res, err := tool.Execute(context.Background(), map[string]interface{}{"action": action})
		require.NoError(t, err, action)
		require.NotNil(t, res, action)
		assert.False(t, res.Success, "%s without channelId must fail", action)
		assert.Contains(t, res.Error, "channelId", action)
	}
	assert.Empty(t, gw.enabled, "service must not be called without channelId")
	assert.Empty(t, gw.disabled, "service must not be called without channelId")
}

func TestGatewayChannels_EnableDisableWithChannelID(t *testing.T) {
	gw := &fakeChannelsGateway{}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw})

	res, err := tool.Execute(context.Background(),
		map[string]interface{}{"action": "enable_channel", "channelId": "telegram"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Success, "enable_channel should succeed, got: %s", res.Error)
	assert.Equal(t, []string{"telegram"}, gw.enabled)

	res, err = tool.Execute(context.Background(),
		map[string]interface{}{"action": "disable_channel", "channelId": "telegram"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Success, "disable_channel should succeed, got: %s", res.Error)
	assert.Equal(t, []string{"telegram"}, gw.disabled)
}
