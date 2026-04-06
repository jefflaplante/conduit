package tools

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

func TestGoogleWorkspaceTool_Name(t *testing.T) {
	tool := &GoogleWorkspaceTool{}
	if tool.Name() != "google_workspace" {
		t.Errorf("expected name 'google_workspace', got '%s'", tool.Name())
	}
}

func TestGoogleWorkspaceTool_Description(t *testing.T) {
	tool := &GoogleWorkspaceTool{}
	desc := tool.Description()
	if desc == "" {
		t.Error("description should not be empty")
	}
	if !strings.Contains(desc, "gws") {
		t.Error("description should mention gws CLI")
	}
}

func TestGoogleWorkspaceTool_Parameters(t *testing.T) {
	tool := &GoogleWorkspaceTool{}
	params := tool.Parameters()

	// Check action enum
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters should have properties")
	}

	action, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters should have action property")
	}

	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action should have enum")
	}

	expectedActions := []string{
		"email_search", "email_read", "email_send", "email_trash",
		"calendar_list", "calendar_create", "calendar_delete",
	}
	for _, expected := range expectedActions {
		found := false
		for _, actual := range enum {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected action '%s' in enum", expected)
		}
	}

	// Check required fields
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("parameters should have required")
	}
	if len(required) != 1 || required[0] != "action" {
		t.Error("only 'action' should be required")
	}
}

func TestGoogleWorkspaceTool_EmailSearch_MissingQuery(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	// This will fail because gws isn't available in tests, but we can check validation
	result, err := tool.emailSearch(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing query")
	}
	if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_query" {
		t.Errorf("expected error type 'missing_query', got '%v'", result.ErrorDetails)
	}
}

func TestGoogleWorkspaceTool_EmailRead_MissingMessageID(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	result, err := tool.emailRead(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing message_id")
	}
	if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_message_id" {
		t.Errorf("expected error type 'missing_message_id', got '%v'", result.ErrorDetails)
	}
}

func TestGoogleWorkspaceTool_EmailSend_MissingArgs(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	testCases := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing all", map[string]interface{}{}},
		{"missing subject and body", map[string]interface{}{"to": "test@example.com"}},
		{"missing to and body", map[string]interface{}{"subject": "Test"}},
		{"missing to and subject", map[string]interface{}{"body": "Hello"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.emailSend(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Success {
				t.Error("expected failure for missing args")
			}
			if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_args" {
				t.Errorf("expected error type 'missing_args', got '%v'", result.ErrorDetails)
			}
		})
	}
}

func TestGoogleWorkspaceTool_EmailTrash_MissingMessageID(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	result, err := tool.emailTrash(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing message_id")
	}
	if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_message_id" {
		t.Errorf("expected error type 'missing_message_id', got '%v'", result.ErrorDetails)
	}
}

func TestGoogleWorkspaceTool_CalendarCreate_MissingArgs(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	testCases := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing all", map[string]interface{}{}},
		{"missing start and end", map[string]interface{}{"title": "Meeting"}},
		{"missing title and end", map[string]interface{}{"start": "2024-03-15T10:00:00Z"}},
		{"missing title and start", map[string]interface{}{"end": "2024-03-15T11:00:00Z"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.calendarCreate(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Success {
				t.Error("expected failure for missing args")
			}
			if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_args" {
				t.Errorf("expected error type 'missing_args', got '%v'", result.ErrorDetails)
			}
		})
	}
}

func TestGoogleWorkspaceTool_CalendarDelete_MissingEventID(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	result, err := tool.calendarDelete(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for missing event_id")
	}
	if result.ErrorDetails == nil || result.ErrorDetails.Type != "missing_event_id" {
		t.Errorf("expected error type 'missing_event_id', got '%v'", result.ErrorDetails)
	}
}

func TestGoogleWorkspaceTool_Execute_InvalidAction(t *testing.T) {
	// Create a mock registry with services
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}

	// Note: This test will fail with gws_not_available if gws isn't installed,
	// which is expected behavior. We test invalid action separately.
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})

	// If gws isn't available, we get gws_not_available error
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Accept either gws_not_available or invalid_action
	if result.ErrorDetails == nil {
		t.Fatal("expected error details")
	}
	if result.ErrorDetails.Type != "gws_not_available" && result.ErrorDetails.Type != "invalid_action" {
		t.Errorf("expected error type 'gws_not_available' or 'invalid_action', got '%s'", result.ErrorDetails.Type)
	}
}

func TestGoogleWorkspaceTool_EmailSend_ValidatesAlias(t *testing.T) {
	// Create a registry with configured email
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{
				Agent: config.AgentConfig{
					Email: config.AgentEmail{
						Address: "agent@example.com",
						Aliases: []string{"alias@example.com"},
					},
				},
			},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}

	// Try to send with an invalid alias
	result, err := tool.emailSend(context.Background(), map[string]interface{}{
		"to":         "recipient@example.com",
		"subject":    "Test",
		"body":       "Hello",
		"from_alias": "invalid@example.com",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Success {
		t.Error("expected failure for invalid alias")
	}
	if result.ErrorDetails == nil || result.ErrorDetails.Type != "invalid_alias" {
		t.Errorf("expected error type 'invalid_alias', got '%v'", result.ErrorDetails)
	}
}

func TestGoogleWorkspaceTool_GetGwsPath_Default(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}
	path := tool.getGwsPath()
	if path != "gws" {
		t.Errorf("expected default path 'gws', got '%s'", path)
	}
}

func TestGoogleWorkspaceTool_GetGwsPath_Configured(t *testing.T) {
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{
				Tools: config.ToolsConfig{
					Services: map[string]map[string]interface{}{
						"google_workspace": {
							"gws_path": "/usr/local/bin/gws",
						},
					},
				},
			},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}
	path := tool.getGwsPath()
	if path != "/usr/local/bin/gws" {
		t.Errorf("expected configured path '/usr/local/bin/gws', got '%s'", path)
	}
}

func TestGoogleWorkspaceTool_GetUserID_Default(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}
	userID := tool.getUserID()
	if userID != "me" {
		t.Errorf("expected default user ID 'me', got '%s'", userID)
	}
}

func TestGoogleWorkspaceTool_GetUserID_Configured(t *testing.T) {
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{
				Tools: config.ToolsConfig{
					Services: map[string]map[string]interface{}{
						"google_workspace": {
							"user_id": "user@example.com",
						},
					},
				},
			},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}
	userID := tool.getUserID()
	if userID != "user@example.com" {
		t.Errorf("expected configured user ID 'user@example.com', got '%s'", userID)
	}
}

func TestBase64URLEncode(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"hello", "aGVsbG8"},
		{"test", "dGVzdA"},
		{"", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := base64URLEncode([]byte(tc.input))
			if result != tc.expected {
				t.Errorf("expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestGoogleWorkspaceTool_SelfTest_GwsNotInstalled(t *testing.T) {
	// Create a tool with a bogus gws path that won't exist
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{
				Tools: config.ToolsConfig{
					Services: map[string]map[string]interface{}{
						"google_workspace": {
							"gws_path": "/nonexistent/path/to/gws",
						},
					},
				},
			},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}

	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Status != types.SelfTestStatusFailed {
		t.Errorf("Expected failed status when gws not installed, got %s", result.Status)
	}

	if result.IsFunctional() {
		t.Error("Tool should not be functional without gws")
	}

	// Should have suggestions
	if len(result.Suggestions) == 0 {
		t.Error("Expected suggestions for missing gws")
	}

	// Check for npm install suggestion
	hasInstallSuggestion := false
	for _, s := range result.Suggestions {
		if strings.Contains(s, "npm install") {
			hasInstallSuggestion = true
			break
		}
	}
	if !hasInstallSuggestion {
		t.Error("Expected npm install suggestion")
	}

	// Should have unavailable capabilities
	if len(result.UnavailableCapabilities) == 0 {
		t.Error("Expected unavailable capabilities")
	}
}

func TestGoogleWorkspaceTool_SelfTest_Dependencies(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should have dependencies reported
	if len(result.Dependencies) == 0 {
		t.Error("Expected dependency information")
	}

	// Check for gws CLI dependency
	foundGwsDep := false
	for _, dep := range result.Dependencies {
		if dep.Name == "gws CLI" {
			foundGwsDep = true
			if dep.Required != true {
				t.Error("gws CLI should be marked as required")
			}
		}
	}
	if !foundGwsDep {
		t.Error("Expected 'gws CLI' dependency")
	}
}

func TestGoogleWorkspaceTool_SelfTest_Timing(t *testing.T) {
	tool := &GoogleWorkspaceTool{registry: &Registry{services: &types.ToolServices{}}}

	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.TestedAt.IsZero() {
		t.Error("Expected TestedAt to be set")
	}

	if result.TestDuration == 0 {
		t.Error("Expected TestDuration to be non-zero")
	}
}

func TestGoogleWorkspaceTool_SelfTest_VerboseMode(t *testing.T) {
	registry := &Registry{
		services: &types.ToolServices{
			ConfigMgr: &config.Config{
				Agent: config.AgentConfig{
					Email: config.AgentEmail{
						Address: "agent@example.com",
						Aliases: []string{"alias@example.com"},
					},
				},
			},
		},
	}
	tool := &GoogleWorkspaceTool{registry: registry}

	opts := &types.SelfTestOptions{
		Verbose: true,
	}
	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// In verbose mode with config, should have details
	if result.Details == nil {
		t.Error("Expected details in verbose mode")
	} else {
		if _, ok := result.Details["gws_path"]; !ok {
			t.Error("Expected gws_path in details")
		}
		if _, ok := result.Details["user_id"]; !ok {
			t.Error("Expected user_id in details")
		}
	}
}
