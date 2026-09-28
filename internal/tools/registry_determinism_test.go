package tools

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// TestRegistry_ModelVisibleNameListsSorted covers conduit-oc3u: tool-name
// lists the model sees in error results (tool_not_found / tool_disabled
// available values) are built from the tools map and must come out sorted,
// not in Go's randomized map order.
func TestRegistry_ModelVisibleNameListsSorted(t *testing.T) {
	ws := t.TempDir()
	reg := NewRegistry(config.ToolsConfig{
		EnabledTools: []string{"ReadFile", "WriteFile", "ListFiles", "Edit", "Exec", "Glob", "Find", "WebFetch", "Message", "Cron", "NoSuchTool"},
		Sandbox:      config.SandboxConfig{WorkspaceDir: ws, AllowedPaths: []string{ws}},
	})
	reg.SetServices(&types.ToolServices{ConfigMgr: &config.Config{Workspace: config.WorkspaceConfig{ContextDir: ws}}})

	var first []string
	for i := 0; i < 20; i++ {
		res, err := reg.ExecuteTool(context.Background(), "NoSuchTool", nil)
		if err != nil {
			t.Fatalf("ExecuteTool: %v", err)
		}
		if res.ErrorDetails == nil || len(res.ErrorDetails.AvailableValues) < 3 {
			t.Fatalf("expected tool_not_found with several available values, got %+v", res)
		}
		got := res.ErrorDetails.AvailableValues
		if !sort.StringsAreSorted(got) {
			t.Fatalf("available values not sorted: %v", got)
		}
		if first == nil {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatalf("available values changed between calls: %v vs %v", first, got)
		}
	}

	if names := ListAvailableOptionalTools(); !sort.StringsAreSorted(names) {
		t.Fatalf("ListAvailableOptionalTools not sorted: %v", names)
	}
}
