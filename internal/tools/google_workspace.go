package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// GoogleWorkspaceTool wraps gws CLI for Gmail and Calendar operations
type GoogleWorkspaceTool struct {
	registry *Registry
}

func (t *GoogleWorkspaceTool) Name() string {
	return "google_workspace"
}

func (t *GoogleWorkspaceTool) Description() string {
	return "Access Google Workspace (Gmail, Calendar) via gws CLI. Requires gws to be installed: npm install -g @googleworkspace/cli"
}

func (t *GoogleWorkspaceTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"email_search", "email_read", "email_send", "email_trash",
					"calendar_list", "calendar_create", "calendar_delete",
				},
				"description": "Action to perform",
			},
			// Email args
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Gmail search query, e.g. 'is:unread' or 'from:someone@example.com' (for email_search)",
			},
			"message_id": map[string]interface{}{
				"type":        "string",
				"description": "Email message ID (for email_read, email_trash)",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "Recipient email address (for email_send)",
			},
			"cc": map[string]interface{}{
				"type":        "string",
				"description": "CC recipients, comma-separated (for email_send)",
			},
			"bcc": map[string]interface{}{
				"type":        "string",
				"description": "BCC recipients, comma-separated (for email_send)",
			},
			"subject": map[string]interface{}{
				"type":        "string",
				"description": "Email subject (for email_send)",
			},
			"body": map[string]interface{}{
				"type":        "string",
				"description": "Email body (for email_send)",
			},
			"from_alias": map[string]interface{}{
				"type":        "string",
				"description": "Send from alias instead of primary address (for email_send)",
			},
			// Calendar args
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Event title/summary (for calendar_create)",
			},
			"start": map[string]interface{}{
				"type":        "string",
				"description": "Event start time ISO8601, e.g. '2024-03-15T10:00:00-07:00' (for calendar_create)",
			},
			"end": map[string]interface{}{
				"type":        "string",
				"description": "Event end time ISO8601 (for calendar_create)",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Event description (for calendar_create)",
			},
			"location": map[string]interface{}{
				"type":        "string",
				"description": "Event location (for calendar_create)",
			},
			"event_id": map[string]interface{}{
				"type":        "string",
				"description": "Calendar event ID (for calendar_delete)",
			},
			"calendar_id": map[string]interface{}{
				"type":        "string",
				"description": "Calendar ID, defaults to 'primary' (for calendar_list, calendar_create, calendar_delete)",
			},
			// Common args
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum results to return (default 10)",
			},
			"days": map[string]interface{}{
				"type":        "integer",
				"description": "Number of days to look ahead for calendar_list (default 7)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GoogleWorkspaceTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check if gws is available
	if err := t.checkGwsAvailable(); err != nil {
		return types.NewErrorResult("gws_not_available", err.Error()).
			WithSuggestions([]string{
				"Install gws: npm install -g @googleworkspace/cli",
				"Authenticate: gws auth login",
				"See: https://github.com/googleworkspace/cli",
			}), nil
	}

	action := toolargs.GetString(args, "action", "")

	switch action {
	case "email_search":
		return t.emailSearch(ctx, args)
	case "email_read":
		return t.emailRead(ctx, args)
	case "email_send":
		return t.emailSend(ctx, args)
	case "email_trash":
		return t.emailTrash(ctx, args)
	case "calendar_list":
		return t.calendarList(ctx, args)
	case "calendar_create":
		return t.calendarCreate(ctx, args)
	case "calendar_delete":
		return t.calendarDelete(ctx, args)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)), nil
	}
}

// checkGwsAvailable verifies gws CLI is installed
func (t *GoogleWorkspaceTool) checkGwsAvailable() error {
	gwsPath := t.getGwsPath()
	cmd := exec.Command(gwsPath, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gws CLI not found at '%s'", gwsPath)
	}
	return nil
}

// getGwsPath returns the configured gws binary path
func (t *GoogleWorkspaceTool) getGwsPath() string {
	services := t.registry.GetServices()
	if services != nil && services.ConfigMgr != nil {
		if cfg := services.ConfigMgr.Tools.Services["google_workspace"]; cfg != nil {
			if path, ok := cfg["gws_path"].(string); ok && path != "" {
				return path
			}
		}
	}
	return "gws" // default: assume in PATH
}

// getUserID returns the configured user ID (default "me")
func (t *GoogleWorkspaceTool) getUserID() string {
	services := t.registry.GetServices()
	if services != nil && services.ConfigMgr != nil {
		if cfg := services.ConfigMgr.Tools.Services["google_workspace"]; cfg != nil {
			if uid, ok := cfg["user_id"].(string); ok && uid != "" {
				return uid
			}
		}
	}
	return "me"
}

// runGws executes a gws command and returns parsed JSON output
func (t *GoogleWorkspaceTool) runGws(ctx context.Context, args ...string) (map[string]interface{}, error) {
	gwsPath := t.getGwsPath()

	cmd := exec.CommandContext(ctx, gwsPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gws error: %v, output: %s", err, string(output))
	}

	// gws outputs JSON
	var result map[string]interface{}
	if err := json.Unmarshal(output, &result); err != nil {
		// If not JSON, return raw output
		return map[string]interface{}{"raw": string(output)}, nil
	}

	return result, nil
}

func (t *GoogleWorkspaceTool) emailSearch(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query := toolargs.GetString(args, "query", "")
	if query == "" {
		return types.NewErrorResult("missing_query", "query is required for email_search"), nil
	}

	limit := toolargs.GetInt(args, "limit", 10)
	userID := t.getUserID()

	// gws gmail users messages list --params '{"userId":"me","q":"is:unread","maxResults":10}'
	params := map[string]interface{}{
		"userId":     userID,
		"q":          query,
		"maxResults": limit,
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := t.runGws(ctx, "gmail", "users", "messages", "list", "--params", string(paramsJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) emailRead(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageID := toolargs.GetString(args, "message_id", "")
	if messageID == "" {
		return types.NewErrorResult("missing_message_id", "message_id is required for email_read"), nil
	}

	userID := t.getUserID()

	// gws gmail users messages get --params '{"userId":"me","id":"xxx","format":"full"}'
	params := map[string]interface{}{
		"userId": userID,
		"id":     messageID,
		"format": "full",
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := t.runGws(ctx, "gmail", "users", "messages", "get", "--params", string(paramsJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) emailSend(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	to := toolargs.GetString(args, "to", "")
	subject := toolargs.GetString(args, "subject", "")
	body := toolargs.GetString(args, "body", "")

	if to == "" || subject == "" || body == "" {
		return types.NewErrorResult("missing_args", "to, subject, and body are required for email_send"), nil
	}

	// Determine from address
	fromAddr := ""
	fromAlias := toolargs.GetString(args, "from_alias", "")
	services := t.registry.GetServices()

	if services != nil && services.ConfigMgr != nil {
		agentEmail := services.ConfigMgr.Agent.Email
		if fromAlias != "" {
			// Validate alias
			valid := fromAlias == agentEmail.Address
			for _, alias := range agentEmail.Aliases {
				if fromAlias == alias {
					valid = true
					break
				}
			}
			if !valid {
				return types.NewErrorResult("invalid_alias",
					fmt.Sprintf("from_alias '%s' not in configured addresses/aliases", fromAlias)), nil
			}
			fromAddr = fromAlias
		} else if agentEmail.Address != "" {
			fromAddr = agentEmail.Address
		}
	}

	// Build RFC 2822 message
	var msg strings.Builder
	if fromAddr != "" {
		msg.WriteString(fmt.Sprintf("From: %s\r\n", fromAddr))
	}
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	if cc := toolargs.GetString(args, "cc", ""); cc != "" {
		msg.WriteString(fmt.Sprintf("Cc: %s\r\n", cc))
	}
	if bcc := toolargs.GetString(args, "bcc", ""); bcc != "" {
		msg.WriteString(fmt.Sprintf("Bcc: %s\r\n", bcc))
	}
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	userID := t.getUserID()

	// gws gmail users messages send --params '{"userId":"me"}' --json '{"raw":"base64..."}'
	params := map[string]interface{}{"userId": userID}
	paramsJSON, _ := json.Marshal(params)

	// Base64url encode the message
	rawMsg := base64URLEncode([]byte(msg.String()))
	bodyJSON := map[string]interface{}{"raw": rawMsg}
	bodyJSONStr, _ := json.Marshal(bodyJSON)

	result, err := t.runGws(ctx, "gmail", "users", "messages", "send",
		"--params", string(paramsJSON), "--json", string(bodyJSONStr))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) emailTrash(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageID := toolargs.GetString(args, "message_id", "")
	if messageID == "" {
		return types.NewErrorResult("missing_message_id", "message_id is required for email_trash"), nil
	}

	userID := t.getUserID()

	// gws gmail users messages trash --params '{"userId":"me","id":"xxx"}'
	params := map[string]interface{}{
		"userId": userID,
		"id":     messageID,
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := t.runGws(ctx, "gmail", "users", "messages", "trash", "--params", string(paramsJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) calendarList(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	limit := toolargs.GetInt(args, "limit", 10)
	days := toolargs.GetInt(args, "days", 7)
	calendarID := toolargs.GetString(args, "calendar_id", "primary")

	// Calculate time range
	now := time.Now()
	timeMin := now.Format(time.RFC3339)
	timeMax := now.AddDate(0, 0, days).Format(time.RFC3339)

	// gws calendar events list --params '{"calendarId":"primary","timeMin":"...","timeMax":"...","maxResults":10}'
	params := map[string]interface{}{
		"calendarId":   calendarID,
		"timeMin":      timeMin,
		"timeMax":      timeMax,
		"maxResults":   limit,
		"singleEvents": true,
		"orderBy":      "startTime",
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := t.runGws(ctx, "calendar", "events", "list", "--params", string(paramsJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) calendarCreate(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	title := toolargs.GetString(args, "title", "")
	start := toolargs.GetString(args, "start", "")
	end := toolargs.GetString(args, "end", "")

	if title == "" || start == "" || end == "" {
		return types.NewErrorResult("missing_args", "title, start, and end are required for calendar_create"), nil
	}

	calendarID := toolargs.GetString(args, "calendar_id", "primary")

	// gws calendar events insert --params '{"calendarId":"primary"}' --json '{"summary":"...","start":{"dateTime":"..."},"end":{"dateTime":"..."}}'
	params := map[string]interface{}{"calendarId": calendarID}
	paramsJSON, _ := json.Marshal(params)

	event := map[string]interface{}{
		"summary": title,
		"start":   map[string]string{"dateTime": start},
		"end":     map[string]string{"dateTime": end},
	}
	if desc := toolargs.GetString(args, "description", ""); desc != "" {
		event["description"] = desc
	}
	if loc := toolargs.GetString(args, "location", ""); loc != "" {
		event["location"] = loc
	}
	eventJSON, _ := json.Marshal(event)

	result, err := t.runGws(ctx, "calendar", "events", "insert",
		"--params", string(paramsJSON), "--json", string(eventJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

func (t *GoogleWorkspaceTool) calendarDelete(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	eventID := toolargs.GetString(args, "event_id", "")
	if eventID == "" {
		return types.NewErrorResult("missing_event_id", "event_id is required for calendar_delete"), nil
	}

	calendarID := toolargs.GetString(args, "calendar_id", "primary")

	// gws calendar events delete --params '{"calendarId":"primary","eventId":"xxx"}'
	params := map[string]interface{}{
		"calendarId": calendarID,
		"eventId":    eventID,
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := t.runGws(ctx, "calendar", "events", "delete", "--params", string(paramsJSON))
	if err != nil {
		return types.NewErrorResult("gws_error", err.Error()), nil
	}

	output, _ := json.MarshalIndent(result, "", "  ")
	return &types.ToolResult{Success: true, Content: string(output)}, nil
}

// base64URLEncode encodes data for Gmail API (URL-safe base64, no padding)
func base64URLEncode(data []byte) string {
	encoded := base64.URLEncoding.EncodeToString(data)
	return strings.TrimRight(encoded, "=")
}
