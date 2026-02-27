package gateway

import (
	"context"
	"fmt"

	"conduit/internal/version"
)

// GetSessionStatus returns status for a session
func (g *Gateway) GetSessionStatus(ctx context.Context, sessionKey string) (map[string]interface{}, error) {
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"session_key":   session.Key,
		"user_id":       session.UserID,
		"channel_id":    session.ChannelID,
		"message_count": session.MessageCount,
		"created_at":    session.CreatedAt,
		"updated_at":    session.UpdatedAt,
		"context":       session.Context,
	}, nil
}

// GetGatewayStatus returns gateway status
func (g *Gateway) GetGatewayStatus() (map[string]interface{}, error) {
	return map[string]interface{}{
		"status":  "running",
		"version": version.Info(),
	}, nil
}

// RestartGateway restarts the gateway
func (g *Gateway) RestartGateway(ctx context.Context) error {
	// TODO: Implement proper restart
	return fmt.Errorf("restart not yet implemented")
}

// GetChannelStatus returns channel adapter status
func (g *Gateway) GetChannelStatus() (map[string]interface{}, error) {
	status := g.channelManager.GetStatus()
	result := make(map[string]interface{})
	for k, v := range status {
		result[k] = v
	}
	return result, nil
}

// EnableChannel enables a channel
func (g *Gateway) EnableChannel(ctx context.Context, channelID string) error {
	return fmt.Errorf("enable channel not yet implemented")
}

// DisableChannel disables a channel
func (g *Gateway) DisableChannel(ctx context.Context, channelID string) error {
	return fmt.Errorf("disable channel not yet implemented")
}

// GetConfiguration returns current configuration
func (g *Gateway) GetConfiguration() (map[string]interface{}, error) {
	return map[string]interface{}{
		"ai":        g.config.AI,
		"workspace": g.config.Workspace,
	}, nil
}

// UpdateConfiguration updates configuration
func (g *Gateway) UpdateConfiguration(ctx context.Context, config map[string]interface{}) error {
	return fmt.Errorf("configuration update not yet implemented")
}

// GetMetrics returns gateway metrics
func (g *Gateway) GetMetrics() (map[string]interface{}, error) {
	return map[string]interface{}{
		"uptime": "unknown",
	}, nil
}

// GetVersion returns the gateway version
func (g *Gateway) GetVersion() string {
	return version.Info()
}
