// Package auth provides OAuth token detection and tool mapping for Claude Code API compatibility.
package auth

import (
	"strings"
)

// OAuthTokenInfo contains information about an OAuth token
type OAuthTokenInfo struct {
	// IsOAuthToken indicates if the token is an OAuth token
	IsOAuthToken bool
	// Token is the raw token value
	Token string
	// RequiredHeaders are the headers required for OAuth requests
	RequiredHeaders map[string]string
}

// Claude Code OAuth tool names (exact casing required by Anthropic API)
// These are the ONLY tools that work with OAuth tokens
var OAuthCompatibleTools = map[string]string{
	// File system tools
	"read":  "Read",
	"write": "Write",
	"edit":  "Edit",
	"glob":  "Glob",
	"grep":  "Grep",

	// Shell tools
	"bash":       "Bash",
	"kill_shell": "KillShell",

	// Web tools
	"web_search":          "WebSearch",
	"web_search_20250305": "WebSearch",
	"web_fetch":           "WebFetch",
	"web_fetch_20250305":  "WebFetch",

	// Plan mode tools
	"ask_user_question": "AskUserQuestion",
	"enter_plan_mode":   "EnterPlanMode",
	"exit_plan_mode":    "ExitPlanMode",

	// Notebook tools
	"notebook_edit": "NotebookEdit",

	// Task management tools
	"skill":       "Skill",
	"task":        "Task",
	"task_output": "TaskOutput",
	"todo_write":  "TodoWrite",
}

// Tools that must be filtered OUT for OAuth requests
// These are Conduit-specific tools that Anthropic doesn't support
var OAuthForbiddenTools = map[string]bool{
	"message":   true,
	"cron":      true,
	"exec":      true,
	"process":   true,
	"browser":   true,
	"canvas":    true,
	"nodes":     true,
	"tts":       true,
	"image":     true,
	"heartbeat": true,
	"session":   true,
}

// isOAuthToken detects if a token is an OAuth token by checking for the "sk-ant-oat" prefix
func IsOAuthToken(token string) bool {
	if token == "" {
		return false
	}
	// OAuth tokens contain "sk-ant-oat" substring
	return strings.Contains(token, "sk-ant-oat")
}

// GetOAuthTokenInfo analyzes a token and returns OAuth-specific information
func GetOAuthTokenInfo(token string) OAuthTokenInfo {
	isOAuth := IsOAuthToken(token)

	info := OAuthTokenInfo{
		IsOAuthToken: isOAuth,
		Token:        token,
	}

	if isOAuth {
		info.RequiredHeaders = GetRequiredOAuthHeaders()
	}

	return info
}

// GetRequiredOAuthHeaders returns the headers required for OAuth requests
func GetRequiredOAuthHeaders() map[string]string {
	return map[string]string{
		"anthropic-beta": "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14",
		"anthropic-dangerous-direct-browser-access": "true",
		"user-agent":        "claude-cli/2.1.2 (external, cli)",
		"x-app":             "cli",
		"anthropic-version": "2023-06-01",
	}
}

// MapToolForOAuth maps an Conduit tool name to the OAuth-compatible Claude Code tool name
// Returns the mapped tool name and whether the tool is compatible with OAuth
func MapToolForOAuth(toolName string) (string, bool) {
	// Check if tool is explicitly forbidden for OAuth
	if OAuthForbiddenTools[toolName] {
		return "", false
	}

	// Check if we have a direct mapping
	mappedName, exists := OAuthCompatibleTools[toolName]
	if exists {
		return mappedName, true
	}

	// If no mapping found and not forbidden, it's incompatible
	return "", false
}

// IsValidOAuthTool checks if a tool name is a valid OAuth-compatible tool name
func IsValidOAuthTool(toolName string) bool {
	// Check if it's an OAuth tool name (in the values of the map)
	for _, oauthName := range OAuthCompatibleTools {
		if toolName == oauthName {
			return true
		}
	}
	return false
}

// FilterToolsForOAuth filters a list of tools to only include OAuth-compatible ones
// Returns the filtered tool names mapped to their OAuth equivalents
func FilterToolsForOAuth(toolNames []string) []string {
	filtered := make([]string, 0)

	for _, toolName := range toolNames {
		if mappedTool, compatible := MapToolForOAuth(toolName); compatible {
			filtered = append(filtered, mappedTool)
		}
	}

	return filtered
}

// ValidateOAuthRequest checks if a request is properly formatted for OAuth
type OAuthValidationError struct {
	Message string
	Field   string
}

func (e *OAuthValidationError) Error() string {
	return e.Message
}

// ValidateOAuthHeaders checks if all required OAuth headers are present
func ValidateOAuthHeaders(headers map[string]string) error {
	required := GetRequiredOAuthHeaders()

	for key, expectedValue := range required {
		if value, exists := headers[key]; !exists {
			return &OAuthValidationError{
				Message: "Missing required OAuth header: " + key,
				Field:   key,
			}
		} else if key == "anthropic-beta" && value != expectedValue {
			return &OAuthValidationError{
				Message: "Incorrect anthropic-beta header value",
				Field:   key,
			}
		}
	}

	return nil
}

// IsSystemPromptValidForOAuth checks if the system prompt is in the correct array format for OAuth
func IsSystemPromptValidForOAuth(systemPrompt interface{}) bool {
	// OAuth requires system prompt to be an array, not a string
	switch v := systemPrompt.(type) {
	case []interface{}:
		// Must have at least one element
		if len(v) == 0 {
			return false
		}
		// Each element should be a map with "type" and "text"
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if itemMap["type"] != "text" || itemMap["text"] == nil {
					return false
				}
			} else {
				return false
			}
		}
		return true
	case []map[string]interface{}:
		// Handle the specific case of []map[string]interface{}
		if len(v) == 0 {
			return false
		}
		// Each element should be a map with "type" and "text"
		for _, itemMap := range v {
			if itemMap["type"] != "text" || itemMap["text"] == nil {
				return false
			}
		}
		return true
	case string:
		// String format not allowed for OAuth
		return false
	default:
		return false
	}
}

// ConvertSystemPromptForOAuth converts a string system prompt to the required array format
func ConvertSystemPromptForOAuth(systemPrompt interface{}) interface{} {
	switch v := systemPrompt.(type) {
	case string:
		// Convert string to array format
		return []map[string]interface{}{
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			{"type": "text", "text": v},
		}
	case []interface{}:
		// Already in array format, return as-is
		return v
	default:
		// Unknown format, create default
		return []map[string]interface{}{
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		}
	}
}
