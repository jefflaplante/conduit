package schema

import (
	"context"
	"fmt"
	"time"
)

// ChannelDiscoveryProvider wraps the channel manager for discovery.
type ChannelDiscoveryProvider struct {
	statusGetter ChannelStatusGetter
}

// NewChannelDiscoveryProvider creates a new channel discovery provider.
func NewChannelDiscoveryProvider(getter ChannelStatusGetter) *ChannelDiscoveryProvider {
	return &ChannelDiscoveryProvider{statusGetter: getter}
}

// GetDiscoveryData returns discovered channel information.
func (p *ChannelDiscoveryProvider) GetDiscoveryData(ctx context.Context, discoveryType string) (*DynamicValues, error) {
	if discoveryType != "channels" {
		return nil, nil
	}

	status := p.statusGetter.GetStatus()
	values := make([]DynamicValue, 0, len(status))

	for channelID, channelStatus := range status {
		statusMap, ok := channelStatus.(map[string]interface{})
		if !ok {
			continue
		}

		// Extract status code and convert to human-readable form
		statusDisplay := "unknown"
		if s, ok := statusMap["status"].(string); ok {
			switch s {
			case "online":
				statusDisplay = "✅ online"
			case "offline":
				statusDisplay = "❌ offline"
			case "error":
				statusDisplay = "⚠️ error"
			case "reconnecting":
				statusDisplay = "🔄 reconnecting"
			case "initializing":
				statusDisplay = "⏳ initializing"
			default:
				statusDisplay = fmt.Sprintf("❔ %s", s)
			}
		}

		label := channelID
		if name, ok := statusMap["name"].(string); ok && name != "" {
			label = fmt.Sprintf("%s (%s)", name, channelID)
		}

		description := ""
		if details, ok := statusMap["details"].(map[string]interface{}); ok {
			if msgCount, ok := details["message_count"].(int64); ok {
				description = fmt.Sprintf("%d messages sent", msgCount)
			}
		}

		values = append(values, DynamicValue{
			Value:       channelID,
			Label:       label,
			Status:      statusDisplay,
			Description: description,
		})
	}

	return &DynamicValues{
		Source:      "channels",
		Values:      values,
		LastUpdated: time.Now(),
	}, nil
}

// WorkspaceDiscoveryProvider provides workspace path examples.
type WorkspaceDiscoveryProvider struct {
	workspaceDir string
	allowedPaths []string
}

// NewWorkspaceDiscoveryProvider creates a new workspace discovery provider.
func NewWorkspaceDiscoveryProvider(workspaceDir string, allowedPaths []string) *WorkspaceDiscoveryProvider {
	return &WorkspaceDiscoveryProvider{
		workspaceDir: workspaceDir,
		allowedPaths: allowedPaths,
	}
}

// GetDiscoveryData returns discovered workspace path information.
func (p *WorkspaceDiscoveryProvider) GetDiscoveryData(ctx context.Context, discoveryType string) (*DynamicValues, error) {
	if discoveryType != "workspace_paths" {
		return nil, nil
	}

	values := []DynamicValue{
		{
			Value:       p.workspaceDir,
			Label:       "Workspace root",
			Status:      "📁 directory",
			Description: "Primary working directory",
		},
	}

	for _, path := range p.allowedPaths {
		if path != p.workspaceDir { // Avoid duplicating workspace root
			values = append(values, DynamicValue{
				Value:       path,
				Label:       "Allowed path",
				Status:      "📂 sandbox",
				Description: "Path within sandbox",
			})
		}
	}

	// Add common examples for file operations
	commonExamples := []DynamicValue{
		{
			Value:       "README.md",
			Label:       "Project documentation",
			Status:      "📄 file",
			Description: "Common documentation file",
		},
		{
			Value:       "src/",
			Label:       "Source directory",
			Status:      "📁 directory",
			Description: "Common source code location",
		},
		{
			Value:       "config.json",
			Label:       "Configuration file",
			Status:      "⚙️ config",
			Description: "JSON configuration file",
		},
	}
	values = append(values, commonExamples...)

	return &DynamicValues{
		Source:      "workspace_paths",
		Values:      values,
		LastUpdated: time.Now(),
	}, nil
}

// StatusProviderAdapter wraps a channel status provider to implement ChannelStatusGetter.
// This adapter allows us to use channel managers with different status structures.
type StatusProviderAdapter struct {
	getStatusFunc func() map[string]interface{}
}

// NewStatusProviderAdapter creates a new adapter for channel status providers.
func NewStatusProviderAdapter(fn func() map[string]interface{}) *StatusProviderAdapter {
	return &StatusProviderAdapter{getStatusFunc: fn}
}

// GetStatus implements ChannelStatusGetter interface.
func (a *StatusProviderAdapter) GetStatus() map[string]interface{} {
	if a.getStatusFunc == nil {
		return make(map[string]interface{})
	}
	return a.getStatusFunc()
}
