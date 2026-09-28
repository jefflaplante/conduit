package ai

import (
	"encoding/base64"

	"conduit/internal/models"
)

// convertMessagesToAnthropic converts messages to Anthropic API format
// This handles the special case of tool results which must be sent as user messages
func (a *AnthropicProvider) convertMessagesToAnthropic(messages []ChatMessage) []models.Message {
	result := make([]models.Message, 0, len(messages))

	for _, msg := range messages {
		before := len(result)
		switch msg.Role {
		case "user":
			if len(msg.Attachments) > 0 {
				contentBlocks := make([]models.ContentBlock, 0, len(msg.Attachments)+1)
				for _, att := range msg.Attachments {
					if att.Type == "image" && len(att.Data) > 0 {
						contentBlocks = append(contentBlocks, models.ContentBlock{
							Type: models.BlockImage,
							Source: &models.ImageSource{
								Type:      "base64",
								MediaType: att.MediaType,
								Data:      base64.StdEncoding.EncodeToString(att.Data),
							},
						})
					}
				}
				if msg.Content != "" {
					contentBlocks = append(contentBlocks, models.TextBlock(msg.Content))
				}
				if len(contentBlocks) > 0 {
					result = append(result, models.Message{Role: "user", Blocks: contentBlocks})
				}
			} else {
				result = append(result, models.Message{Role: "user", Text: msg.Content})
			}
		case "assistant":
			// Build assistant message with potential tool_use blocks
			if len(msg.ToolCalls) > 0 {
				content := make([]models.ContentBlock, 0, len(msg.ToolCalls)+1)
				if msg.Content != "" {
					content = append(content, models.TextBlock(msg.Content))
				}
				for _, tc := range msg.ToolCalls {
					// Ensure tool input is always a valid JSON object for OAuth
					input := tc.Args
					if input == nil {
						input = make(map[string]interface{})
					}
					content = append(content, models.ContentBlock{
						Type:  models.BlockToolUse,
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				result = append(result, models.Message{Role: "assistant", Blocks: content})
			} else {
				result = append(result, models.Message{Role: "assistant", Text: msg.Content})
			}
		case "tool":
			// Tool results must be sent as user messages with tool_result content.
			// conduit-31jg.45: failed calls carry is_error, and all results of
			// one round share ONE user message (the API requires every
			// tool_result for an assistant turn in the next user turn; we no
			// longer rely on it merging consecutive user turns).
			block := models.ContentBlock{
				Type:      models.BlockToolResult,
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
				IsError:   msg.IsError,
			}
			if blocks := trailingToolResultBlocks(result); blocks != nil {
				result[len(result)-1].Blocks = append(blocks, block)
			} else {
				result = append(result, models.Message{Role: "user", Blocks: []models.ContentBlock{block}})
			}
		}

		// conduit-31jg.45: user text that follows a round's tool results
		// (loop guidance, refocus) joins that same user message after the
		// tool_result blocks instead of forming a second user turn.
		if msg.Role == "user" && before > 0 && len(result) == before+1 {
			if blocks := trailingToolResultBlocks(result[:len(result)-1]); blocks != nil {
				result[len(result)-2].Blocks = append(blocks, userContentBlocks(result[len(result)-1])...)
				result = result[:len(result)-1]
			}
		}
	}

	return result
}

// trailingToolResultBlocks returns the content blocks of the last converted
// message when it is a user turn carrying tool_result blocks, else nil.
// conduit-31jg.45.
func trailingToolResultBlocks(converted []models.Message) []models.ContentBlock {
	if len(converted) == 0 {
		return nil
	}
	last := converted[len(converted)-1]
	if last.Role != "user" || len(last.Blocks) == 0 || last.Blocks[0].Type != models.BlockToolResult {
		return nil
	}
	return last.Blocks
}

// userContentBlocks normalizes converted user content (string or blocks) to
// a block slice. conduit-31jg.45.
func userContentBlocks(msg models.Message) []models.ContentBlock {
	if msg.HasBlocks() {
		return msg.Blocks
	}
	if msg.Text == "" {
		return nil
	}
	return []models.ContentBlock{models.TextBlock(msg.Text)}
}

// Claude Code tool names that are known to work with OAuth tokens
var claudeCodeTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "Bash": true,
	"Grep": true, "Glob": true, "WebFetch": true, "WebSearch": true,
	"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true,
	"KillShell": true, "NotebookEdit": true, "Skill": true, "Task": true,
	"TaskOutput": true, "TodoWrite": true,
}

// convertToolsToAnthropic converts tool definitions to Anthropic format
// When using OAuth tokens, only Claude Code-compatible tools are included
func (a *AnthropicProvider) convertToolsToAnthropic(tools []Tool) []models.AnthropicTool {
	anthropicTools := make([]models.AnthropicTool, 0, len(tools))

	for _, tool := range tools {
		// For OAuth tokens, only include Claude Code-compatible tools
		if a.isOAuth && !claudeCodeTools[tool.Name] {
			continue
		}

		anthropicTools = append(anthropicTools, models.AnthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.Parameters,
		})
	}
	return anthropicTools
}
