package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

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

	// Sort tool names for deterministic ordering. GetAvailableTools returns
	// a Go map whose iteration order is randomized per call; without this,
	// the tools array reshuffles every request and invalidates provider
	// prompt caches (the cached prefix includes the tools block).
	names := make([]string, 0, len(availableTools))
	for name := range availableTools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		tool := availableTools[name]
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
				// Sort action names: docs is a map, and a random order here
				// changes the tools-array bytes across restarts, missing the
				// provider prompt cache for the tools prefix (conduit-oc3u).
				actions := make([]string, 0, len(docs))
				for action := range docs {
					actions = append(actions, action)
				}
				sort.Strings(actions)
				for _, action := range actions {
					doc := docs[action]
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
