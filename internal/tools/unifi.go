package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// UniFiTool implements UniFi Network/Protect API functionality
type UniFiTool struct {
	registry *Registry
}

func (t *UniFiTool) Name() string {
	return "UniFi"
}

func (t *UniFiTool) Description() string {
	return "Interact with UniFi Network and Protect systems (cameras, devices, etc.)"
}

func (t *UniFiTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"snapshot", "devices", "cameras", "status"},
				"description": "Action to perform",
			},
			"camera": map[string]interface{}{
				"type":        "string",
				"description": "Camera name or ID for snapshot actions (optional)",
			},
			"system": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"network", "protect"},
				"description": "UniFi system to query (defaults to protect for cameras)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *UniFiTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := getStringArg(args, "action", "")
	camera := getStringArg(args, "camera", "")
	system := getStringArg(args, "system", "protect")

	switch action {
	case "snapshot":
		return t.getSnapshot(ctx, camera)
	case "cameras":
		return t.getCameras(ctx)
	case "devices":
		return t.getDevices(ctx, system)
	case "status":
		return t.getStatus(ctx, system)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)).
			WithParameter("action", action).
			WithAvailableValues([]string{"snapshot", "devices", "cameras", "status"}), nil
	}
}

func (t *UniFiTool) getSnapshot(ctx context.Context, cameraName string) (*types.ToolResult, error) {
	// Get UniFi Protect credentials from environment
	unvrURL := os.Getenv("UNVR_URL")
	apiKey := os.Getenv("UNVR_API_KEY")

	if unvrURL == "" || apiKey == "" {
		return types.NewErrorResult("missing_credentials",
			"UNVR_URL and UNVR_API_KEY environment variables are required").
			WithSuggestions([]string{
				"Set UNVR_URL and UNVR_API_KEY environment variables",
				"Check secrets file configuration",
			}), nil
	}

	// First, get the list of cameras to find the camera ID
	cameras, err := t.fetchProtectCameras(unvrURL, apiKey)
	if err != nil {
		return types.NewErrorResult("api_error",
			fmt.Sprintf("Failed to fetch cameras: %v", err)), nil
	}

	// If no specific camera requested, use the first available camera
	var selectedCamera map[string]interface{}
	if cameraName == "" {
		if len(cameras) == 0 {
			return types.NewErrorResult("no_cameras",
				"No cameras available"), nil
		}
		selectedCamera = cameras[0]
	} else {
		// Find camera by name
		found := false
		for _, cam := range cameras {
			if name, ok := cam["name"].(string); ok && strings.EqualFold(name, cameraName) {
				selectedCamera = cam
				found = true
				break
			}
		}
		if !found {
			var cameraNames []string
			for _, cam := range cameras {
				if name, ok := cam["name"].(string); ok {
					cameraNames = append(cameraNames, name)
				}
			}
			return types.NewErrorResult("camera_not_found",
				fmt.Sprintf("Camera '%s' not found", cameraName)).
				WithParameter("camera", cameraName).
				WithAvailableValues(cameraNames), nil
		}
	}

	cameraID, ok := selectedCamera["id"].(string)
	if !ok {
		return types.NewErrorResult("invalid_camera_data",
			"Camera ID not found in API response"), nil
	}

	// Get snapshot from the camera
	snapshotURL := fmt.Sprintf("%s/proxy/protect/api/cameras/%s/snapshot", unvrURL, cameraID)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", snapshotURL, nil)
	if err != nil {
		return types.NewErrorResult("request_error",
			fmt.Sprintf("Failed to create request: %v", err)), nil
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	resp, err := client.Do(req)
	if err != nil {
		return types.NewErrorResult("network_error",
			fmt.Sprintf("Failed to fetch snapshot: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return types.NewErrorResult("api_error",
			fmt.Sprintf("Snapshot request failed with status: %d", resp.StatusCode)), nil
	}

	// Save snapshot to temporary file
	snapshotData, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.NewErrorResult("read_error",
			fmt.Sprintf("Failed to read snapshot data: %v", err)), nil
	}

	tempFile := fmt.Sprintf("/tmp/unifi_snapshot_%s_%d.jpg",
		strings.ReplaceAll(selectedCamera["name"].(string), " ", "_"),
		time.Now().Unix())

	if err := os.WriteFile(tempFile, snapshotData, 0644); err != nil {
		return types.NewErrorResult("file_error",
			fmt.Sprintf("Failed to save snapshot: %v", err)), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Snapshot saved from camera '%s' to %s (%d bytes)",
			selectedCamera["name"], tempFile, len(snapshotData)),
		Data: map[string]interface{}{
			"camera_name": selectedCamera["name"],
			"camera_id":   cameraID,
			"file_path":   tempFile,
			"file_size":   len(snapshotData),
			"timestamp":   time.Now().Unix(),
		},
	}, nil
}

func (t *UniFiTool) getCameras(ctx context.Context) (*types.ToolResult, error) {
	unvrURL := os.Getenv("UNVR_URL")
	apiKey := os.Getenv("UNVR_API_KEY")

	if unvrURL == "" || apiKey == "" {
		return types.NewErrorResult("missing_credentials",
			"UNVR_URL and UNVR_API_KEY environment variables are required"), nil
	}

	cameras, err := t.fetchProtectCameras(unvrURL, apiKey)
	if err != nil {
		return types.NewErrorResult("api_error",
			fmt.Sprintf("Failed to fetch cameras: %v", err)), nil
	}

	// Format camera information
	var cameraList []map[string]interface{}
	for _, cam := range cameras {
		cameraInfo := map[string]interface{}{
			"name":   cam["name"],
			"id":     cam["id"],
			"type":   cam["type"],
			"state":  cam["state"],
			"online": cam["isConnected"],
		}
		if ip, ok := cam["host"]; ok {
			cameraInfo["ip"] = ip
		}
		cameraList = append(cameraList, cameraInfo)
	}

	camerasJSON, _ := json.MarshalIndent(cameraList, "", "  ")

	return &types.ToolResult{
		Success: true,
		Content: string(camerasJSON),
		Data: map[string]interface{}{
			"cameras": cameraList,
			"count":   len(cameraList),
		},
	}, nil
}

func (t *UniFiTool) getDevices(ctx context.Context, system string) (*types.ToolResult, error) {
	// Implementation for network devices would go here
	return types.NewErrorResult("not_implemented",
		"Device listing not yet implemented"), nil
}

func (t *UniFiTool) getStatus(ctx context.Context, system string) (*types.ToolResult, error) {
	// Implementation for system status would go here
	return types.NewErrorResult("not_implemented",
		"Status checking not yet implemented"), nil
}

func (t *UniFiTool) fetchProtectCameras(unvrURL, apiKey string) ([]map[string]interface{}, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}

	url := fmt.Sprintf("%s/proxy/protect/api/cameras", unvrURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status: %d", resp.StatusCode)
	}

	var cameras []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&cameras); err != nil {
		return nil, err
	}

	return cameras, nil
}
