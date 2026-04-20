package gateway

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// channelStatusAdapter wraps the channel manager to implement
// schema.ChannelStatusGetter for the schema builder's channel-discovery
// provider.
type channelStatusAdapter struct {
	manager *channels.Manager
}

// GetStatus implements schema.ChannelStatusGetter.
func (a *channelStatusAdapter) GetStatus() map[string]interface{} {
	result := make(map[string]interface{})
	for id, status := range a.manager.GetStatus() {
		result[id] = map[string]interface{}{
			"status":  string(status.Status),
			"message": status.Message,
			"name":    id, // Use ID as name for now.
		}
	}
	return result
}

// deriveRuntimeChannel returns the first enabled channel name, or "websocket"
// as fallback. Used to seed the agent system's RuntimeChannel config.
func deriveRuntimeChannel(channels []config.ChannelConfig) string {
	for _, ch := range channels {
		if ch.Enabled {
			return ch.Type
		}
	}
	return "websocket"
}

// convertToolsToAIFormat converts tools registry tools to the AI layer's Tool
// format, applying schema hints, usage examples, and per-action docs from the
// optional tool interfaces.
func convertToolsToAIFormat(registry *tools.Registry) []ai.Tool {
	var aiTools []ai.Tool

	availableTools := registry.GetAvailableTools()
	for _, tool := range availableTools {
		description := tool.Description()
		params := tool.Parameters()

		// Apply schema hints from EnhancedSchemaProvider.
		if esp, ok := tool.(types.EnhancedSchemaProvider); ok {
			hints := esp.GetSchemaHints()
			if len(hints) > 0 {
				builder := schema.NewBuilder(nil)
				params = builder.EnhanceSchema(context.Background(), params, hints)
			}
		}

		// Append usage examples to description.
		if uep, ok := tool.(types.UsageExampleProvider); ok {
			examples := uep.GetUsageExamples()
			if len(examples) > 0 {
				description += "\n\nUsage examples:"
				for _, ex := range examples {
					description += fmt.Sprintf("\n- %s: %s", ex.Name, ex.Description)
				}
			}
		}

		// Append per-action documentation.
		if adp, ok := tool.(types.ActionDocProvider); ok {
			docs := adp.GetActionDocs()
			if len(docs) > 0 {
				description += "\n\nAction details:"
				for action, doc := range docs {
					description += fmt.Sprintf("\n[%s] %s", action, doc.Description)
					if len(doc.RequiredParams) > 0 {
						description += fmt.Sprintf(" Required: %s.", strings.Join(doc.RequiredParams, ", "))
					}
					if len(doc.OptionalParams) > 0 {
						description += fmt.Sprintf(" Optional: %s.", strings.Join(doc.OptionalParams, ", "))
					}
					if doc.Returns != "" {
						description += fmt.Sprintf(" Returns: %s.", doc.Returns)
					}
				}
			}
		}

		aiTool := ai.Tool{
			Name:        tool.Name(),
			Description: description,
			Parameters:  params,
		}
		aiTools = append(aiTools, aiTool)
	}

	return aiTools
}

// createSchemaBuilder creates a schema builder with discovery providers for
// enhanced tool schemas. A nil gateway yields a builder without channel
// discovery; other providers (workspace paths) are still attached.
func createSchemaBuilder(gw *Gateway, cfg *config.Config) *schema.Builder {
	providers := make(map[string]schema.DiscoveryProvider)

	// Add channel discovery provider.
	if gw != nil && gw.channelManager != nil {
		channelProvider := schema.NewChannelDiscoveryProvider(&channelStatusAdapter{manager: gw.channelManager})
		providers["channels"] = channelProvider
	}

	// Add workspace discovery provider.
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	allowedPaths := cfg.Tools.Sandbox.AllowedPaths
	workspaceProvider := schema.NewWorkspaceDiscoveryProvider(workspaceDir, allowedPaths)
	providers["workspace_paths"] = workspaceProvider

	return schema.NewBuilder(providers)
}
