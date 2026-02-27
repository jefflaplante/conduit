package tools

import (
	"os"
	"testing"
)

func TestGetAnthropicTool(t *testing.T) {
	tests := []struct {
		name          string
		alias         string
		expectedName  string
		expectedAlias string
		shouldError   bool
	}{
		{
			name:          "Valid web_search alias",
			alias:         "web_search",
			expectedName:  "web_search_20250305",
			expectedAlias: "web_search",
			shouldError:   false,
		},
		{
			name:          "Valid web_fetch alias",
			alias:         "web_fetch",
			expectedName:  "web_fetch_20250305",
			expectedAlias: "web_fetch",
			shouldError:   false,
		},
		{
			name:        "Invalid alias",
			alias:       "nonexistent_tool",
			shouldError: true,
		},
		{
			name:        "Empty alias",
			alias:       "",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := GetAnthropicTool(tt.alias)

			if tt.shouldError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if config.Name != tt.expectedName {
				t.Errorf("Expected name %s, got %s", tt.expectedName, config.Name)
			}

			if config.Alias != tt.expectedAlias {
				t.Errorf("Expected alias %s, got %s", tt.expectedAlias, config.Alias)
			}

			if !config.IsAnthropicTool {
				t.Error("Expected IsAnthropicTool to be true")
			}

			if config.Version == "" {
				t.Error("Expected Version to be set")
			}
		})
	}
}

func TestGetAnthropicToolWithEnvironmentOverride(t *testing.T) {
	// Set environment variable for testing
	envKey := "ANTHROPIC_TOOL_WEB_SEARCH_VERSION"
	overrideValue := "web_search_20250401"

	// Clean up after test
	defer func() {
		os.Unsetenv(envKey)
	}()

	// Set the override
	os.Setenv(envKey, overrideValue)

	config, err := GetAnthropicTool("web_search")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if config.Name != overrideValue {
		t.Errorf("Expected overridden name %s, got %s", overrideValue, config.Name)
	}

	if config.Alias != "web_search" {
		t.Errorf("Expected alias to remain web_search, got %s", config.Alias)
	}
}

func TestGetAllAnthropicTools(t *testing.T) {
	configs := GetAllAnthropicTools()

	// Should have at least the tools we defined
	expectedAliases := []string{"web_search", "web_fetch"}

	for _, alias := range expectedAliases {
		config, exists := configs[alias]
		if !exists {
			t.Errorf("Missing config for alias: %s", alias)
			continue
		}

		if config.Alias != alias {
			t.Errorf("Config alias mismatch: expected %s, got %s", alias, config.Alias)
		}

		if !config.IsAnthropicTool {
			t.Errorf("Expected IsAnthropicTool to be true for %s", alias)
		}
	}
}

func TestIsAnthropicTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		expected bool
	}{
		{
			name:     "Versioned tool name",
			toolName: "web_search_20250305",
			expected: true,
		},
		{
			name:     "Alias name",
			toolName: "web_search",
			expected: true,
		},
		{
			name:     "Unknown tool",
			toolName: "unknown_tool",
			expected: false,
		},
		{
			name:     "Empty string",
			toolName: "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsAnthropicTool(tt.toolName)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestExtractVersionFromToolName(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		expected string
	}{
		{
			name:     "Standard versioned tool",
			toolName: "web_search_20250305",
			expected: "20250305",
		},
		{
			name:     "Different version format",
			toolName: "web_fetch_20250305",
			expected: "20250305",
		},
		{
			name:     "No version suffix",
			toolName: "web_search",
			expected: "",
		},
		{
			name:     "Empty string",
			toolName: "",
			expected: "",
		},
		{
			name:     "Single word",
			toolName: "search",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractVersionFromToolName(tt.toolName)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestGetInternalToolName(t *testing.T) {
	tests := []struct {
		name     string
		alias    string
		expected string
	}{
		{
			name:     "web_search maps to WebSearch",
			alias:    "web_search",
			expected: "WebSearch",
		},
		{
			name:     "web_fetch maps to WebFetch",
			alias:    "web_fetch",
			expected: "WebFetch",
		},
		{
			name:     "bash maps to Bash",
			alias:    "bash",
			expected: "Bash",
		},
		{
			name:     "unknown alias returns itself",
			alias:    "unknown",
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInternalToolName(tt.alias)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestValidateToolConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      *ToolConfig
		shouldError bool
	}{
		{
			name: "Valid config",
			config: &ToolConfig{
				Name:            "web_search_20250305",
				Alias:           "web_search",
				Version:         "20250305",
				IsAnthropicTool: true,
			},
			shouldError: false,
		},
		{
			name:        "Nil config",
			config:      nil,
			shouldError: true,
		},
		{
			name: "Empty name",
			config: &ToolConfig{
				Name:            "",
				Alias:           "web_search",
				Version:         "20250305",
				IsAnthropicTool: true,
			},
			shouldError: true,
		},
		{
			name: "Empty alias",
			config: &ToolConfig{
				Name:            "web_search_20250305",
				Alias:           "",
				Version:         "20250305",
				IsAnthropicTool: true,
			},
			shouldError: true,
		},
		{
			name: "Anthropic tool missing version",
			config: &ToolConfig{
				Name:            "web_search_20250305",
				Alias:           "web_search",
				Version:         "",
				IsAnthropicTool: true,
			},
			shouldError: true,
		},
		{
			name: "Non-Anthropic tool without version is ok",
			config: &ToolConfig{
				Name:            "custom_tool",
				Alias:           "custom",
				Version:         "",
				IsAnthropicTool: false,
			},
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolConfig(tt.config)

			if tt.shouldError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.shouldError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestGetToolVersionHistory(t *testing.T) {
	// Test with known tool
	history, err := GetToolVersionHistory("web_search")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(history) == 0 {
		t.Error("Expected version history to have entries")
	}

	// Test with unknown tool
	_, err = GetToolVersionHistory("unknown_tool")
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
}

func TestGetCurrentToolVersion(t *testing.T) {
	version, err := GetCurrentToolVersion("web_search")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if version == "" {
		t.Error("Expected version to be set")
	}

	// Test with unknown tool
	_, err = GetCurrentToolVersion("unknown_tool")
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
}

func TestListAvailableAliases(t *testing.T) {
	aliases := ListAvailableAliases()

	if len(aliases) == 0 {
		t.Error("Expected at least some aliases to be available")
	}

	// Check that known aliases are present
	expectedAliases := map[string]bool{
		"web_search": false,
		"web_fetch":  false,
	}

	for _, alias := range aliases {
		if _, exists := expectedAliases[alias]; exists {
			expectedAliases[alias] = true
		}
	}

	for alias, found := range expectedAliases {
		if !found {
			t.Errorf("Expected alias %s to be in available aliases", alias)
		}
	}
}
