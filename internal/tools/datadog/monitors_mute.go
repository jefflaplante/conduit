//go:build with_datadog

package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"conduit/internal/httpsafe"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

func (t *MonitorTool) executeMuteMonitor(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check confirmation
	if result := checkConfirmation("mute_monitor", args); result != nil {
		return result, nil
	}

	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	// Build mute options
	muteOpts := MuteOptions{}
	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		muteOpts.Scope = scope
	}
	if end := toolargs.GetInt64(args, "end", 0); end > 0 {
		muteOpts.End = &end
	}

	// Encode body
	body, err := json.Marshal(muteOpts)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to encode mute options: %v", err)}, nil
	}

	path := fmt.Sprintf("api/v1/monitor/%d/mute", monitorID)
	resp, err := t.client.Do(ctx, http.MethodPost, path, strings.NewReader(string(body)))
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to mute monitor: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := httpsafe.ReadLimited(resp.Body, httpsafe.ErrorBodyLimit) // conduit-31jg.7
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	content := fmt.Sprintf("Monitor %d has been muted", monitorID)
	data := map[string]interface{}{
		"monitor_id": monitorID,
		"muted":      true,
	}

	if muteOpts.Scope != "" {
		content += fmt.Sprintf(" (scope: %s)", muteOpts.Scope)
		data["scope"] = muteOpts.Scope
	}

	if muteOpts.End != nil {
		endTime := time.Unix(*muteOpts.End, 0)
		content += fmt.Sprintf(" until %s", endTime.Format(time.RFC3339))
		data["mute_end"] = endTime.Format(time.RFC3339)
	} else {
		content += " indefinitely"
		data["indefinite"] = true
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

func (t *MonitorTool) executeUnmuteMonitor(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check confirmation
	if result := checkConfirmation("unmute_monitor", args); result != nil {
		return result, nil
	}

	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	// Build query params for scope if provided
	path := fmt.Sprintf("api/v1/monitor/%d/unmute", monitorID)
	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		path += "?scope=" + url.QueryEscape(scope)
	}

	resp, err := t.client.Do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to unmute monitor: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := httpsafe.ReadLimited(resp.Body, httpsafe.ErrorBodyLimit) // conduit-31jg.7
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	content := fmt.Sprintf("Monitor %d has been unmuted", monitorID)
	data := map[string]interface{}{
		"monitor_id": monitorID,
		"muted":      false,
	}

	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		content += fmt.Sprintf(" (scope: %s)", scope)
		data["scope"] = scope
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}
