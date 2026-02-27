package tools

import (
	"testing"
)

func TestAnthropicToolConstants(t *testing.T) {
	// Test that all constants are properly defined
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{
			name:     "WebSearch tool constant",
			constant: AnthropicWebSearchTool,
			expected: "web_search_20250305",
		},
		{
			name:     "WebFetch tool constant",
			constant: AnthropicWebFetchTool,
			expected: "web_fetch_20250305",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.constant)
			}
		})
	}
}

func TestLegacyToolConstants(t *testing.T) {
	// Test that legacy constants are properly defined
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{
			name:     "Legacy WebSearch constant",
			constant: LegacyWebSearchTool,
			expected: "web_search",
		},
		{
			name:     "Legacy WebFetch constant",
			constant: LegacyWebFetchTool,
			expected: "web_fetch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.constant != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.constant)
			}
		})
	}
}

func TestAnthropicToolVersionsMapping(t *testing.T) {
	// Test that the mapping contains expected entries
	expectedMappings := map[string]string{
		"web_search": AnthropicWebSearchTool,
		"web_fetch":  AnthropicWebFetchTool,
	}

	for alias, expectedVersion := range expectedMappings {
		actualVersion, exists := AnthropicToolVersions[alias]
		if !exists {
			t.Errorf("Missing mapping for alias: %s", alias)
			continue
		}

		if actualVersion != expectedVersion {
			t.Errorf("For alias %s: expected %s, got %s", alias, expectedVersion, actualVersion)
		}
	}
}

func TestToolVersionHistory(t *testing.T) {
	// Test that version history exists for known tools
	requiredTools := []string{"web_search", "web_fetch"}

	for _, toolAlias := range requiredTools {
		history, exists := ToolVersionHistory[toolAlias]
		if !exists {
			t.Errorf("Missing version history for tool: %s", toolAlias)
			continue
		}

		if len(history) == 0 {
			t.Errorf("Empty version history for tool: %s", toolAlias)
			continue
		}

		// Test first entry structure
		firstVersion := history[0]
		if firstVersion.Version == "" {
			t.Errorf("Missing version in history for tool: %s", toolAlias)
		}

		if firstVersion.Introduced == "" {
			t.Errorf("Missing introduced date in history for tool: %s", toolAlias)
		}

		if firstVersion.Description == "" {
			t.Errorf("Missing description in history for tool: %s", toolAlias)
		}

		if len(firstVersion.Changes) == 0 {
			t.Errorf("Missing changes in history for tool: %s", toolAlias)
		}
	}
}

func TestToolVersionInfoStructure(t *testing.T) {
	// Create a test ToolVersionInfo and verify its structure
	testInfo := ToolVersionInfo{
		Version:     "web_search_20250305",
		Introduced:  "2025-03-05",
		Description: "Test version",
		Changes:     []string{"Test change 1", "Test change 2"},
	}

	if testInfo.Version == "" {
		t.Error("ToolVersionInfo.Version should not be empty")
	}

	if testInfo.Introduced == "" {
		t.Error("ToolVersionInfo.Introduced should not be empty")
	}

	if testInfo.Description == "" {
		t.Error("ToolVersionInfo.Description should not be empty")
	}

	if len(testInfo.Changes) == 0 {
		t.Error("ToolVersionInfo.Changes should not be empty")
	}
}
