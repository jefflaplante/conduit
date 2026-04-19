package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"conduit/internal/sessions"
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

// RestartGateway initiates a graceful restart via the ShutdownManager.
func (g *Gateway) RestartGateway(ctx context.Context) error {
	if g.shutdownMgr == nil {
		return fmt.Errorf("shutdown manager not initialized")
	}
	return g.shutdownMgr.BeginShutdown("gateway tool restart", 30*time.Second)
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

// ReloadSkillTools rediscovers skills from the filesystem and re-registers them
// in the tool registry. Returns the count of skill tools now registered.
func (g *Gateway) ReloadSkillTools(ctx context.Context) (int, error) {
	if g.skillsManager == nil {
		return 0, fmt.Errorf("skills system not configured")
	}
	// Rediscover skills from filesystem
	if err := g.skillsManager.ReloadSkills(ctx); err != nil {
		return 0, fmt.Errorf("failed to reload skills: %w", err)
	}
	// Re-register skill tools in the registry
	count := g.tools.RefreshSkillTools()
	g.agentSystem.InvalidatePromptCache()
	slog.Info("Skills hot-reloaded", "skill_tools", count)
	return count, nil
}

// GetSystemPromptDebug returns debug info about the system prompt for a session.
func (g *Gateway) GetSystemPromptDebug(ctx context.Context, sessionKey string) (map[string]interface{}, error) {
	var session *sessions.Session
	if sessionKey != "" {
		s, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			return nil, fmt.Errorf("session %q not found: %w", sessionKey, err)
		}
		session = s
	}

	debug, err := g.agentSystem.BuildSystemPromptDebug(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to build prompt debug: %w", err)
	}

	sections := make([]map[string]interface{}, len(debug.Sections))
	for i, s := range debug.Sections {
		sections[i] = map[string]interface{}{
			"name":     s.Name,
			"priority": s.Priority,
			"chars":    s.Chars,
			"included": s.Included,
		}
	}

	return map[string]interface{}{
		"prompt_text":        debug.PromptText,
		"total_chars":        debug.TotalChars,
		"estimated_tokens":   debug.EstimatedTokens,
		"context_window":     debug.ContextWindow,
		"budget_chars":       debug.BudgetChars,
		"budget_constrained": debug.BudgetConstrained,
		"sections":           sections,
		"dropped_sections":   debug.DroppedSections,
	}, nil
}
