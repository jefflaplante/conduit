package gateway

import (
	"sort"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// TestConvertToolsToAIFormatDeterministicOrder verifies the tools array is
// byte-stable across calls — the provider's prefix cache depends on
// deterministic ordering. Registry.GetAvailableTools returns a Go map,
// which iterates in random order; without sorting, the tools array
// reshuffles on every request, invalidating provider prompt caches.
func TestConvertToolsToAIFormatDeterministicOrder(t *testing.T) {
	// Enable a spread of real core tools (empty services still registers them).
	reg := tools.NewRegistry(config.ToolsConfig{
		EnabledTools: []string{
			"ReadFile", "WriteFile", "Exec", "ListFiles", "Edit",
			"StatusUpdate", "Tts", "Cron", "Image",
		},
	})
	reg.SetServices(&types.ToolServices{})

	var first []string
	for i := 0; i < 25; i++ {
		aiTools := convertToolsToAIFormat(reg)
		names := make([]string, 0, len(aiTools))
		for _, tl := range aiTools {
			names = append(names, tl.Name)
		}
		if first == nil {
			first = names
			if len(first) < 3 {
				t.Fatalf("expected at least 3 enabled tools, got %d: %v", len(first), first)
			}
			if !sort.StringsAreSorted(first) {
				t.Fatalf("first conversion not sorted: %v", first)
			}
			continue
		}
		if len(names) != len(first) {
			t.Fatalf("call %d: tool count changed: %d vs %d", i, len(names), len(first))
		}
		for j := range names {
			if names[j] != first[j] {
				t.Fatalf("call %d: order changed at index %d:\n got %v\nwant %v", i, j, names, first)
			}
		}
	}
}
