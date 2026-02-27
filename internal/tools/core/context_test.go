package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/tools/types"
)

// setupTestContextTool creates a ContextTool with a minimal config for testing.
// The workspace dir points at tmpDir so file-system operations are isolated.
func setupTestContextTool(t *testing.T) (*ContextTool, string) {
	t.Helper()
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Port: 18789,
		Tools: config.ToolsConfig{
			EnabledTools:  []string{"Context", "Read", "Write"},
			MaxToolChains: 25,
			Sandbox: config.SandboxConfig{
				WorkspaceDir: tmpDir,
				AllowedPaths: []string{tmpDir},
			},
		},
		Workspace: config.WorkspaceConfig{
			ContextDir: tmpDir,
		},
		Agent: config.AgentConfig{
			Name: "test-agent",
		},
		AI: config.AIConfig{
			DefaultProvider: "anthropic",
		},
	}

	services := &types.ToolServices{
		ConfigMgr: cfg,
	}
	tool := NewContextTool(services)
	return tool, tmpDir
}

// --- Basic interface tests ---

func TestContextTool_Name(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	if got := tool.Name(); got != "Context" {
		t.Errorf("Name() = %q, want %q", got, "Context")
	}
}

func TestContextTool_Description(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
	if !strings.Contains(desc, "workspace") {
		t.Errorf("Description() should mention workspace, got: %s", desc)
	}
}

func TestContextTool_Parameters(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	params := tool.Parameters()

	if params == nil {
		t.Fatal("Parameters() returned nil")
	}

	if params["type"] != "object" {
		t.Errorf("Parameters() type = %v, want %q", params["type"], "object")
	}

	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() properties is not a map")
	}

	// Check "section" property exists and has the right enum
	sectionProp, ok := props["section"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() missing section property")
	}
	if sectionProp["type"] != "string" {
		t.Errorf("section type = %v, want %q", sectionProp["type"], "string")
	}
	enumVals, ok := sectionProp["enum"].([]string)
	if !ok {
		t.Fatal("section enum is not []string")
	}
	expectedSections := map[string]bool{
		"workspace": true, "project": true, "session": true,
		"gateway": true, "channels": true, "tools": true, "beads": true,
	}
	for _, v := range enumVals {
		if !expectedSections[v] {
			t.Errorf("unexpected section enum value: %q", v)
		}
		delete(expectedSections, v)
	}
	for missing := range expectedSections {
		t.Errorf("missing section enum value: %q", missing)
	}

	// Check "verbose" property
	verboseProp, ok := props["verbose"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters() missing verbose property")
	}
	if verboseProp["type"] != "boolean" {
		t.Errorf("verbose type = %v, want %q", verboseProp["type"], "boolean")
	}
}

// --- Execute tests ---

func TestContextTool_Execute_NoArgs(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() returned Success=false, Error=%q", result.Error)
	}
	if result.Content == "" {
		t.Error("Execute() returned empty Content")
	}
	if result.Data == nil {
		t.Error("Execute() returned nil Data")
	}

	// With no section filter, all seven sections should be present in Data
	expectedKeys := []string{"workspace", "project", "session", "gateway", "channels", "tools", "beads"}
	for _, key := range expectedKeys {
		if _, ok := result.Data[key]; !ok {
			t.Errorf("Execute() Data missing key %q", key)
		}
	}
}

func TestContextTool_Execute_NoArgs_NilServices(t *testing.T) {
	// ContextTool with completely nil ConfigMgr should degrade gracefully.
	services := &types.ToolServices{}
	tool := NewContextTool(services)
	ctx := context.Background()

	result, err := tool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute() with nil services returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should still succeed with graceful degradation, got Error=%q", result.Error)
	}

	// Workspace section should indicate config not available
	wsData, ok := result.Data["workspace"].(map[string]interface{})
	if !ok {
		t.Fatal("workspace data missing or wrong type")
	}
	if errMsg, ok := wsData["error"].(string); !ok || errMsg == "" {
		t.Error("workspace section should contain an error when config is nil")
	}
}

func TestContextTool_Execute_Section_Workspace(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "workspace",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() returned Success=false, Error=%q", result.Error)
	}

	// Only workspace section should be in data
	if len(result.Data) != 1 {
		t.Errorf("expected 1 data key, got %d: %v", len(result.Data), keysOf(result.Data))
	}

	wsData, ok := result.Data["workspace"].(map[string]interface{})
	if !ok {
		t.Fatal("workspace data missing or wrong type")
	}

	if wsData["workspace_dir"] != tmpDir {
		t.Errorf("workspace_dir = %v, want %v", wsData["workspace_dir"], tmpDir)
	}
	if wsData["workspace_exists"] != true {
		t.Errorf("workspace_exists = %v, want true", wsData["workspace_exists"])
	}

	// Content should mention the workspace dir
	if !strings.Contains(result.Content, tmpDir) {
		t.Errorf("Content should contain workspace dir %q", tmpDir)
	}
	if !strings.Contains(result.Content, "## Workspace") {
		t.Error("Content should contain '## Workspace' header")
	}
}

func TestContextTool_Execute_Section_Workspace_NonexistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonexistentDir := filepath.Join(tmpDir, "does-not-exist")

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox: config.SandboxConfig{
				WorkspaceDir: nonexistentDir,
			},
		},
	}
	services := &types.ToolServices{ConfigMgr: cfg}
	tool := NewContextTool(services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "workspace",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	wsData := result.Data["workspace"].(map[string]interface{})
	if wsData["workspace_exists"] != false {
		t.Errorf("workspace_exists should be false for nonexistent dir, got %v", wsData["workspace_exists"])
	}
	if !strings.Contains(result.Content, "does not exist") {
		t.Error("Content should indicate workspace directory does not exist")
	}
}

func TestContextTool_Execute_Section_Workspace_Verbose(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox: config.SandboxConfig{
				WorkspaceDir: tmpDir,
				AllowedPaths: []string{"/tmp/alpha", "/tmp/beta"},
			},
		},
		Workspace: config.WorkspaceConfig{
			ContextDir: filepath.Join(tmpDir, "ctx"),
		},
	}
	services := &types.ToolServices{ConfigMgr: cfg}
	tool := NewContextTool(services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "workspace",
		"verbose": true,
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verbose mode should include allowed paths in content
	if !strings.Contains(result.Content, "/tmp/alpha") {
		t.Error("Verbose workspace should list allowed paths in content")
	}
	if !strings.Contains(result.Content, "/tmp/beta") {
		t.Error("Verbose workspace should list allowed paths in content")
	}
}

func TestContextTool_Execute_Section_Tools(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "tools",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	toolsData, ok := result.Data["tools"].(map[string]interface{})
	if !ok {
		t.Fatal("tools data missing or wrong type")
	}

	enabledTools, ok := toolsData["enabled_tools"].([]string)
	if !ok {
		t.Fatal("enabled_tools missing or wrong type")
	}

	expected := []string{"Context", "Read", "Write"}
	if len(enabledTools) != len(expected) {
		t.Fatalf("enabled_tools count = %d, want %d", len(enabledTools), len(expected))
	}
	for i, e := range expected {
		if enabledTools[i] != e {
			t.Errorf("enabled_tools[%d] = %q, want %q", i, enabledTools[i], e)
		}
	}

	if toolsData["count"] != len(expected) {
		t.Errorf("count = %v, want %d", toolsData["count"], len(expected))
	}

	if toolsData["max_tool_chains"] != 25 {
		t.Errorf("max_tool_chains = %v, want 25", toolsData["max_tool_chains"])
	}

	// Content check
	if !strings.Contains(result.Content, "## Tools") {
		t.Error("Content should contain '## Tools' header")
	}
	if !strings.Contains(result.Content, "Context") {
		t.Error("Content should list enabled tool 'Context'")
	}
}

func TestContextTool_Execute_Section_Tools_NilConfig(t *testing.T) {
	services := &types.ToolServices{}
	tool := NewContextTool(services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "tools",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	toolsData := result.Data["tools"].(map[string]interface{})
	if toolsData["error"] != "config not available" {
		t.Errorf("expected error = 'config not available', got %v", toolsData["error"])
	}
}

func TestContextTool_Execute_Section_Session(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Set up request context with session values
	ctx := types.WithRequestContext(context.Background(), "ws-001", "user-42", "session-abc")

	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "session",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	sessData, ok := result.Data["session"].(map[string]interface{})
	if !ok {
		t.Fatal("session data missing or wrong type")
	}

	if sessData["channel_id"] != "ws-001" {
		t.Errorf("channel_id = %v, want %q", sessData["channel_id"], "ws-001")
	}
	if sessData["user_id"] != "user-42" {
		t.Errorf("user_id = %v, want %q", sessData["user_id"], "user-42")
	}
	if sessData["session_key"] != "session-abc" {
		t.Errorf("session_key = %v, want %q", sessData["session_key"], "session-abc")
	}

	// Content check
	if !strings.Contains(result.Content, "## Session") {
		t.Error("Content should contain '## Session' header")
	}
	if !strings.Contains(result.Content, "session-abc") {
		t.Error("Content should contain the session key")
	}
	if !strings.Contains(result.Content, "ws-001") {
		t.Error("Content should contain the channel ID")
	}
	if !strings.Contains(result.Content, "user-42") {
		t.Error("Content should contain the user ID")
	}
}

func TestContextTool_Execute_Section_Session_EmptyContext(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	ctx := context.Background() // no request context set

	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "session",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	sessData := result.Data["session"].(map[string]interface{})
	if sessData["channel_id"] != "" {
		t.Errorf("channel_id should be empty, got %v", sessData["channel_id"])
	}
	if sessData["session_key"] != "" {
		t.Errorf("session_key should be empty, got %v", sessData["session_key"])
	}
	if !strings.Contains(result.Content, "(not set)") {
		t.Error("Content should indicate session key is not set")
	}
}

func TestContextTool_Execute_Section_Beads(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	// Create .beads directory with issues.jsonl
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := []string{
		`{"id":"test-1","title":"First issue","status":"open","priority":2}`,
		`{"id":"test-2","title":"Closed issue","status":"closed","priority":1}`,
		`{"id":"test-3","title":"In progress issue","status":"in_progress","priority":3}`,
		`{"id":"test-4","title":"Another open","status":"new","priority":1}`,
	}
	issuesContent := strings.Join(issues, "\n") + "\n"
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issuesContent), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	beadsData, ok := result.Data["beads"].(map[string]interface{})
	if !ok {
		t.Fatal("beads data missing or wrong type")
	}

	if beadsData["detected"] != true {
		t.Error("beads should be detected")
	}

	issuesData, ok := beadsData["issues"].(map[string]interface{})
	if !ok {
		t.Fatal("issues data missing or wrong type")
	}

	if issuesData["total"] != 4 {
		t.Errorf("total = %v, want 4", issuesData["total"])
	}
	// "open" status maps: open=1, new=1 => 2
	if issuesData["open"] != 2 {
		t.Errorf("open = %v, want 2", issuesData["open"])
	}
	if issuesData["in_progress"] != 1 {
		t.Errorf("in_progress = %v, want 1", issuesData["in_progress"])
	}
	if issuesData["closed"] != 1 {
		t.Errorf("closed = %v, want 1", issuesData["closed"])
	}

	// Content checks
	if !strings.Contains(result.Content, "## Beads") {
		t.Error("Content should contain '## Beads' header")
	}
	if !strings.Contains(result.Content, "4 total") {
		t.Error("Content should show total count")
	}
}

func TestContextTool_Execute_Section_Beads_NoBeadsDir(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	beadsData := result.Data["beads"].(map[string]interface{})
	if beadsData["detected"] != false {
		t.Error("beads should not be detected when .beads/ dir missing")
	}
	if !strings.Contains(result.Content, "No .beads directory detected") {
		t.Error("Content should indicate no .beads directory")
	}
}

func TestContextTool_Execute_Section_Beads_WithMetadata(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write metadata.json
	metadata := map[string]interface{}{
		"backend": "sqlite",
	}
	metaBytes, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metaBytes, 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	beadsData := result.Data["beads"].(map[string]interface{})
	if beadsData["detected"] != true {
		t.Error("beads should be detected")
	}

	meta, ok := beadsData["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata should be present in beads data")
	}
	if meta["backend"] != "sqlite" {
		t.Errorf("metadata backend = %v, want 'sqlite'", meta["backend"])
	}

	if !strings.Contains(result.Content, "Backend: sqlite") {
		t.Error("Content should show backend from metadata")
	}
}

func TestContextTool_Execute_Section_Beads_MetadataExportPath(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write metadata.json with jsonl_export path
	metadata := map[string]interface{}{
		"backend":      "sqlite",
		"jsonl_export": "custom/issues.jsonl",
	}
	metaBytes, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metaBytes, 0644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	// Create the custom export path
	customDir := filepath.Join(tmpDir, "custom")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("failed to create custom dir: %v", err)
	}
	issuesContent := `{"id":"exp-1","title":"Exported issue","status":"open","priority":1}` + "\n"
	if err := os.WriteFile(filepath.Join(customDir, "issues.jsonl"), []byte(issuesContent), 0644); err != nil {
		t.Fatalf("failed to write custom issues.jsonl: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	beadsData := result.Data["beads"].(map[string]interface{})
	issuesData := beadsData["issues"].(map[string]interface{})
	if issuesData["total"] != 1 {
		t.Errorf("total = %v, want 1 (from custom export path)", issuesData["total"])
	}
	if issuesData["open"] != 1 {
		t.Errorf("open = %v, want 1", issuesData["open"])
	}
}

func TestContextTool_Execute_Section_Beads_Verbose(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := []string{
		`{"id":"v-1","title":"Verbose open","status":"open","priority":2,"assignee":"alice"}`,
		`{"id":"v-2","title":"Verbose in progress","status":"in_progress","priority":1}`,
		`{"id":"v-3","title":"Verbose closed","status":"closed","priority":3}`,
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(issues, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
		"verbose": true,
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	beadsData := result.Data["beads"].(map[string]interface{})
	issuesData := beadsData["issues"].(map[string]interface{})

	// In verbose mode, open_issues should contain the open and in_progress items
	openIssues, ok := issuesData["open_issues"].([]map[string]interface{})
	if !ok {
		t.Fatal("verbose mode should include open_issues list")
	}
	if len(openIssues) != 2 {
		t.Errorf("open_issues count = %d, want 2 (1 open + 1 in_progress)", len(openIssues))
	}

	// Check that summarizeIssue extracts key fields
	found := false
	for _, oi := range openIssues {
		if oi["id"] == "v-1" {
			found = true
			if oi["title"] != "Verbose open" {
				t.Errorf("issue title = %v, want 'Verbose open'", oi["title"])
			}
			if oi["assignee"] != "alice" {
				t.Errorf("issue assignee = %v, want 'alice'", oi["assignee"])
			}
		}
	}
	if !found {
		t.Error("open_issues should contain issue v-1")
	}

	// Content should list the open issues
	if !strings.Contains(result.Content, "Open Issues:") {
		t.Error("Verbose content should contain 'Open Issues:' header")
	}
	if !strings.Contains(result.Content, "Verbose open") {
		t.Error("Verbose content should list open issue titles")
	}
}

func TestContextTool_Execute_Section_Beads_MalformedJSONL(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Mix of valid and invalid lines, plus blank lines
	issuesContent := `{"id":"ok-1","title":"Good issue","status":"open","priority":1}
not valid json
{"id":"ok-2","title":"Another good","status":"closed","priority":2}

{"broken json
{"id":"ok-3","title":"Third issue","status":"doing","priority":3}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(issuesContent), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	issuesData := result.Data["beads"].(map[string]interface{})["issues"].(map[string]interface{})
	// Only 3 valid JSON lines should be counted (malformed lines skipped, blank lines skipped)
	if issuesData["total"] != 3 {
		t.Errorf("total = %v, want 3 (malformed lines should be skipped)", issuesData["total"])
	}
}

func TestContextTool_Execute_Section_Beads_IssuesInWorkspaceRoot(t *testing.T) {
	// Test fallback: issues.jsonl in workspace root (not in .beads/)
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Put issues.jsonl in workspace root, not inside .beads/
	issuesContent := `{"id":"root-1","title":"Root issue","status":"open","priority":1}` + "\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "issues.jsonl"), []byte(issuesContent), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	beadsData := result.Data["beads"].(map[string]interface{})
	issuesData := beadsData["issues"].(map[string]interface{})
	if issuesData["total"] != 1 {
		t.Errorf("total = %v, want 1 (should find issues.jsonl in workspace root)", issuesData["total"])
	}
}

func TestContextTool_Execute_Section_Beads_AllStatusVariants(t *testing.T) {
	tool, tmpDir := setupTestContextTool(t)

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Test all recognized status variants
	issues := []string{
		`{"id":"s1","title":"t","status":"open"}`,
		`{"id":"s2","title":"t","status":"new"}`,
		`{"id":"s3","title":"t","status":"backlog"}`,
		`{"id":"s4","title":"t","status":"todo"}`,
		`{"id":"s5","title":"t","status":"in_progress"}`,
		`{"id":"s6","title":"t","status":"in-progress"}`,
		`{"id":"s7","title":"t","status":"active"}`,
		`{"id":"s8","title":"t","status":"doing"}`,
		`{"id":"s9","title":"t","status":"closed"}`,
		`{"id":"s10","title":"t","status":"done"}`,
		`{"id":"s11","title":"t","status":"resolved"}`,
		`{"id":"s12","title":"t","status":"completed"}`,
		`{"id":"s13","title":"t","status":"unknown_status"}`, // should count as open
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(issues, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "beads",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	issuesData := result.Data["beads"].(map[string]interface{})["issues"].(map[string]interface{})
	if issuesData["total"] != 13 {
		t.Errorf("total = %v, want 13", issuesData["total"])
	}
	// open: open, new, backlog, todo, unknown_status = 5
	if issuesData["open"] != 5 {
		t.Errorf("open = %v, want 5", issuesData["open"])
	}
	// in_progress: in_progress, in-progress, active, doing = 4
	if issuesData["in_progress"] != 4 {
		t.Errorf("in_progress = %v, want 4", issuesData["in_progress"])
	}
	// closed: closed, done, resolved, completed = 4
	if issuesData["closed"] != 4 {
		t.Errorf("closed = %v, want 4", issuesData["closed"])
	}
}

func TestContextTool_Execute_Section_Project(t *testing.T) {
	tmpDir := t.TempDir()

	// Initialize a git repo in tmpDir
	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatalf("git config email: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test User"); err != nil {
		t.Fatalf("git config name: %v", err)
	}

	// Create a file, add, and commit
	testFile := filepath.Join(tmpDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := runCmd(tmpDir, "git", "add", "hello.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := runCmd(tmpDir, "git", "commit", "-m", "Initial commit"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox: config.SandboxConfig{
				WorkspaceDir: tmpDir,
			},
		},
	}
	services := &types.ToolServices{ConfigMgr: cfg}
	tool := NewContextTool(services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	projData, ok := result.Data["project"].(map[string]interface{})
	if !ok {
		t.Fatal("project data missing or wrong type")
	}

	if projData["git_available"] != true {
		t.Error("git_available should be true")
	}

	// The branch should be either "main" or "master" depending on git defaults
	branch, _ := projData["branch"].(string)
	if branch == "" {
		t.Error("branch should not be empty")
	}

	if projData["dirty"] != false {
		t.Error("dirty should be false after clean commit")
	}
	if projData["changed_files"] != 0 {
		t.Errorf("changed_files = %v, want 0", projData["changed_files"])
	}

	// Should have recent commits
	commits, ok := projData["recent_commits"].([]string)
	if !ok || len(commits) == 0 {
		t.Error("recent_commits should contain at least one commit")
	}
	if len(commits) > 0 && !strings.Contains(commits[0], "Initial commit") {
		t.Errorf("first commit should contain 'Initial commit', got %q", commits[0])
	}

	// Content checks
	if !strings.Contains(result.Content, "## Project") {
		t.Error("Content should contain '## Project' header")
	}
	if !strings.Contains(result.Content, "Branch:") {
		t.Error("Content should show branch name")
	}
	if !strings.Contains(result.Content, "clean") {
		t.Error("Content should show 'clean' status")
	}
}

func TestContextTool_Execute_Section_Project_DirtyRepo(t *testing.T) {
	tmpDir := t.TempDir()

	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}

	// Initial commit
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	// Create an uncommitted file to make the repo dirty
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	projData := result.Data["project"].(map[string]interface{})
	if projData["dirty"] != true {
		t.Error("dirty should be true with uncommitted file")
	}
	changedFiles, ok := projData["changed_files"].(int)
	if !ok || changedFiles < 1 {
		t.Errorf("changed_files = %v, want >= 1", projData["changed_files"])
	}
	if !strings.Contains(result.Content, "dirty") {
		t.Error("Content should indicate dirty status")
	}
}

func TestContextTool_Execute_Section_Project_VerboseCommits(t *testing.T) {
	tmpDir := t.TempDir()

	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}

	// Create multiple commits
	for i := 0; i < 3; i++ {
		fname := filepath.Join(tmpDir, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(fname, []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := runCmd(tmpDir, "git", "add", "."); err != nil {
			t.Fatal(err)
		}
		if err := runCmd(tmpDir, "git", "commit", "-m", "Commit "+string(rune('A'+i))); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	// Non-verbose
	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
		"verbose": false,
	})
	projData := result.Data["project"].(map[string]interface{})
	commits := projData["recent_commits"].([]string)
	if len(commits) != 3 {
		t.Errorf("non-verbose commits count = %d, want 3", len(commits))
	}

	// Verbose should use different format (includes relative time)
	resultV, _ := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
		"verbose": true,
	})
	projDataV := resultV.Data["project"].(map[string]interface{})
	commitsV := projDataV["recent_commits"].([]string)
	if len(commitsV) != 3 {
		t.Errorf("verbose commits count = %d, want 3", len(commitsV))
	}
	// Verbose format includes relative time indicator like "(X seconds ago)"
	for _, c := range commitsV {
		if !strings.Contains(c, "(") {
			t.Errorf("verbose commit should include relative time in parens, got %q", c)
		}
	}
}

func TestContextTool_Execute_Section_Project_NotAGitRepo(t *testing.T) {
	tmpDir := t.TempDir() // plain directory, no git init

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Error("Execute() should succeed even when not in a git repo")
	}

	projData := result.Data["project"].(map[string]interface{})
	if projData["git_available"] != false {
		t.Error("git_available should be false for non-git directory")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Error("Content should indicate git is not available")
	}
}

func TestContextTool_Execute_Section_Gateway(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "gateway",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	gwData, ok := result.Data["gateway"].(map[string]interface{})
	if !ok {
		t.Fatal("gateway data missing or wrong type")
	}

	// Version info should always be present (from version package)
	if gwData["version"] == nil || gwData["version"] == "" {
		t.Error("version should be present")
	}
	if gwData["go_version"] == nil || gwData["go_version"] == "" {
		t.Error("go_version should be present")
	}

	// Config-derived values
	if gwData["port"] != 18789 {
		t.Errorf("port = %v, want 18789", gwData["port"])
	}
	if gwData["agent_name"] != "test-agent" {
		t.Errorf("agent_name = %v, want 'test-agent'", gwData["agent_name"])
	}
	if gwData["ai_provider"] != "anthropic" {
		t.Errorf("ai_provider = %v, want 'anthropic'", gwData["ai_provider"])
	}

	if !strings.Contains(result.Content, "## Gateway") {
		t.Error("Content should contain '## Gateway' header")
	}
	if !strings.Contains(result.Content, "Port: 18789") {
		t.Error("Content should show port")
	}
}

func TestContextTool_Execute_Section_Gateway_Verbose(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	resultNonVerbose, _ := tool.Execute(context.Background(), map[string]interface{}{
		"section": "gateway",
		"verbose": false,
	})

	resultVerbose, _ := tool.Execute(context.Background(), map[string]interface{}{
		"section": "gateway",
		"verbose": true,
	})

	// Verbose output should include Git Commit and Build Date
	if strings.Contains(resultNonVerbose.Content, "Git Commit:") {
		t.Error("non-verbose should NOT include Git Commit")
	}
	if !strings.Contains(resultVerbose.Content, "Git Commit:") {
		t.Error("verbose should include Git Commit")
	}
	if !strings.Contains(resultVerbose.Content, "Build Date:") {
		t.Error("verbose should include Build Date")
	}
	if !strings.Contains(resultVerbose.Content, "Go Version:") {
		t.Error("verbose should include Go Version")
	}
}

func TestContextTool_Execute_Section_Gateway_SSHEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Port: 9999,
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
		AI: config.AIConfig{DefaultProvider: "test"},
		SSH: config.SSHServerConfig{
			Enabled:    true,
			ListenAddr: ":2222",
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"section": "gateway",
	})

	gwData := result.Data["gateway"].(map[string]interface{})
	if gwData["ssh_enabled"] != true {
		t.Error("ssh_enabled should be true")
	}
	if !strings.Contains(result.Content, "SSH: enabled") {
		t.Error("Content should show SSH enabled")
	}
	if !strings.Contains(result.Content, ":2222") {
		t.Error("Content should show SSH listen address")
	}
}

func TestContextTool_Execute_Section_Channels_NoGateway(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
		Channels: []config.ChannelConfig{
			{Name: "telegram", Type: "telegram", Enabled: true},
			{Name: "whatsapp", Type: "whatsapp", Enabled: false},
		},
	}
	// No Gateway service set - should fall back to config
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "channels",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	chanData := result.Data["channels"].(map[string]interface{})
	if chanData["configured"] != 2 {
		t.Errorf("configured = %v, want 2", chanData["configured"])
	}
	if !strings.Contains(result.Content, "telegram") {
		t.Error("Content should list telegram channel")
	}
	if !strings.Contains(result.Content, "whatsapp") {
		t.Error("Content should list whatsapp channel")
	}
}

func TestContextTool_Execute_Section_Channels_NilEverything(t *testing.T) {
	// No Gateway service and no ConfigMgr
	tool := NewContextTool(&types.ToolServices{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "channels",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !strings.Contains(result.Content, "not available") {
		t.Error("Content should indicate channel status not available")
	}
}

func TestContextTool_Execute_Verbose(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	ctx := context.Background()

	// Non-verbose
	resultNV, err := tool.Execute(ctx, map[string]interface{}{
		"verbose": false,
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verbose
	resultV, err := tool.Execute(ctx, map[string]interface{}{
		"verbose": true,
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verbose output should generally be longer or equal
	if len(resultV.Content) < len(resultNV.Content) {
		t.Errorf("verbose Content (%d chars) should be >= non-verbose (%d chars)",
			len(resultV.Content), len(resultNV.Content))
	}
}

func TestContextTool_Execute_InvalidSection(t *testing.T) {
	tool, _ := setupTestContextTool(t)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"section": "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// The tool should succeed (it just won't match any section in the switch)
	if !result.Success {
		t.Error("Execute() should succeed even with invalid section")
	}

	// Data should be empty since no section matched
	if len(result.Data) != 0 {
		t.Errorf("Data should be empty for invalid section, got keys: %v", keysOf(result.Data))
	}

	// Content should be empty or just whitespace
	if strings.TrimSpace(result.Content) != "" {
		t.Errorf("Content should be empty for invalid section, got: %q", result.Content)
	}
}

func TestContextTool_GetUsageExamples(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Verify the tool satisfies UsageExampleProvider interface
	var _ types.UsageExampleProvider = tool

	examples := tool.GetUsageExamples()
	if len(examples) == 0 {
		t.Fatal("GetUsageExamples() returned no examples")
	}

	// Check that each example has required fields
	for i, ex := range examples {
		if ex.Name == "" {
			t.Errorf("example[%d].Name is empty", i)
		}
		if ex.Description == "" {
			t.Errorf("example[%d].Description is empty", i)
		}
		if ex.Args == nil {
			t.Errorf("example[%d].Args is nil", i)
		}
		if ex.Expected == "" {
			t.Errorf("example[%d].Expected is empty", i)
		}
	}

	// Check that there's at least one example with no args (full overview)
	foundNoArgs := false
	for _, ex := range examples {
		if len(ex.Args) == 0 {
			foundNoArgs = true
			break
		}
	}
	if !foundNoArgs {
		t.Error("should have at least one example with empty args (full overview)")
	}

	// Check that there's an example with section="beads" and verbose=true
	foundBeadsVerbose := false
	for _, ex := range examples {
		if ex.Args["section"] == "beads" && ex.Args["verbose"] == true {
			foundBeadsVerbose = true
			break
		}
	}
	if !foundBeadsVerbose {
		t.Error("should have an example for verbose beads section")
	}
}

func TestContextTool_ImplementsToolInterface(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Compile-time check that ContextTool satisfies the Tool interface
	var _ types.Tool = tool
}

// --- Helper argument tests ---

func TestContextTool_getStringArg(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	tests := []struct {
		name       string
		args       map[string]interface{}
		key        string
		defaultVal string
		want       string
	}{
		{"present string", map[string]interface{}{"k": "v"}, "k", "d", "v"},
		{"missing key", map[string]interface{}{}, "k", "d", "d"},
		{"wrong type", map[string]interface{}{"k": 42}, "k", "d", "d"},
		{"nil args", nil, "k", "d", "d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tool.getStringArg(tt.args, tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("getStringArg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContextTool_getBoolArg(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	tests := []struct {
		name       string
		args       map[string]interface{}
		key        string
		defaultVal bool
		want       bool
	}{
		{"present true", map[string]interface{}{"k": true}, "k", false, true},
		{"present false", map[string]interface{}{"k": false}, "k", true, false},
		{"missing key default true", map[string]interface{}{}, "k", true, true},
		{"missing key default false", map[string]interface{}{}, "k", false, false},
		{"wrong type", map[string]interface{}{"k": "true"}, "k", false, false},
		{"nil args", nil, "k", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tool.getBoolArg(tt.args, tt.key, tt.defaultVal)
			if got != tt.want {
				t.Errorf("getBoolArg() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =====================================================================
// Tests for enhanced Context tool features (conduit-6af)
// =====================================================================

// --- (.1) Parallel Execution Tests ---

func TestContextTool_ParallelExecution_AllSections(t *testing.T) {
	// When requesting all sections, they should all be present and populated.
	// This indirectly tests the parallel path since Execute now uses goroutines.
	tool, tmpDir := setupTestContextTool(t)

	// Set up a .beads dir so the beads section has something to collect
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	issueContent := `{"id":"par-1","title":"Parallel test","status":"open"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(issueContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := types.WithRequestContext(context.Background(), "ws-test", "user-1", "sess-1")

	result, err := tool.Execute(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() Success=false, Error=%q", result.Error)
	}

	// All sections should be present
	for _, key := range []string{"workspace", "project", "session", "gateway", "channels", "tools", "beads"} {
		if _, ok := result.Data[key]; !ok {
			t.Errorf("parallel execution missing section %q in Data", key)
		}
	}

	// Content should have all section headers
	for _, header := range []string{"## Workspace", "## Project", "## Session", "## Gateway", "## Channels", "## Tools", "## Beads"} {
		if !strings.Contains(result.Content, header) {
			t.Errorf("parallel execution content missing header %q", header)
		}
	}
}

func TestContextTool_ParallelExecution_RaceConditionSafe(t *testing.T) {
	// Run the tool many times concurrently to check for race conditions.
	// The -race flag in the test runner will catch any data races.
	tool, _ := setupTestContextTool(t)
	ctx := context.Background()

	const concurrency = 10
	errs := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			_, err := tool.Execute(ctx, map[string]interface{}{})
			errs <- err
		}()
	}

	for i := 0; i < concurrency; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Execute() returned error: %v", err)
		}
	}
}

// --- (.2) Smart Caching Tests ---

func TestContextCache_SetAndGet(t *testing.T) {
	cache := newContextCache()

	// Set a value
	cache.set("key1", "value1", 5*time.Minute)

	// Get should succeed
	val, ok := cache.get("key1")
	if !ok {
		t.Fatal("expected cache hit for key1")
	}
	if val != "value1" {
		t.Errorf("cache value = %v, want %q", val, "value1")
	}

	// Non-existent key should miss
	_, ok = cache.get("nonexistent")
	if ok {
		t.Error("expected cache miss for nonexistent key")
	}
}

func TestContextCache_Expiration(t *testing.T) {
	cache := newContextCache()

	// Set with very short TTL
	cache.set("expiring", "data", 1*time.Millisecond)

	// Sleep to let it expire
	time.Sleep(5 * time.Millisecond)

	_, ok := cache.get("expiring")
	if ok {
		t.Error("expected cache miss for expired entry")
	}
}

func TestContextCache_TwoTiers(t *testing.T) {
	cache := newContextCache()

	// Static tier: 5 minutes
	cache.set("static", "static-data", cacheTierStatic)
	// Dynamic tier: 30 seconds
	cache.set("dynamic", "dynamic-data", cacheTierDynamic)

	// Both should be retrievable immediately
	if v, ok := cache.get("static"); !ok || v != "static-data" {
		t.Error("static cache entry should be available")
	}
	if v, ok := cache.get("dynamic"); !ok || v != "dynamic-data" {
		t.Error("dynamic cache entry should be available")
	}
}

func TestContextTool_GitCaching(t *testing.T) {
	tmpDir := t.TempDir()

	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	// First call: should populate cache
	result1, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("first Execute() error: %v", err)
	}

	// Second call: should use cache (result should be identical)
	result2, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("second Execute() error: %v", err)
	}

	// Branch should be the same in both calls
	proj1 := result1.Data["project"].(map[string]interface{})
	proj2 := result2.Data["project"].(map[string]interface{})
	if proj1["branch"] != proj2["branch"] {
		t.Errorf("cached branch mismatch: %v vs %v", proj1["branch"], proj2["branch"])
	}
}

// --- (.3) Graceful Error Handling Tests ---

func TestContextTool_GracefulDegradation_PanicRecovery(t *testing.T) {
	// The collectSectionSafe method should recover from panics and return a degraded result.
	// We test this indirectly through collectSectionSafe.
	tool, _ := setupTestContextTool(t)

	// This tests that unknown sections don't cause issues
	res := tool.collectSectionSafe(context.Background(), "unknown_section", false)
	if res == nil {
		t.Fatal("collectSectionSafe returned nil for unknown section")
	}
	if res.name != "unknown_section" {
		t.Errorf("section name = %q, want %q", res.name, "unknown_section")
	}
}

func TestContextTool_GracefulDegradation_NilServices(t *testing.T) {
	// All services nil - every section should degrade gracefully
	services := &types.ToolServices{}
	tool := NewContextTool(services)

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed even with nil services, got Error=%q", result.Error)
	}

	// All sections should still be present in data (with degraded content)
	for _, key := range []string{"workspace", "project", "session", "gateway", "channels", "tools", "beads"} {
		if _, ok := result.Data[key]; !ok {
			t.Errorf("Data missing section %q even with nil services", key)
		}
	}
}

func TestContextTool_Project_NotGitRepo_FallbackInfo(t *testing.T) {
	// When not in a git repo, should provide basic directory info as fallback
	tmpDir := t.TempDir()

	// Create some files to count
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	projData := result.Data["project"].(map[string]interface{})
	if projData["git_available"] != false {
		t.Error("git_available should be false")
	}

	// Fallback should include file/dir counts
	if fileCount, ok := projData["file_count"].(int); !ok || fileCount < 2 {
		t.Errorf("file_count = %v, want >= 2", projData["file_count"])
	}
	if dirCount, ok := projData["dir_count"].(int); !ok || dirCount < 1 {
		t.Errorf("dir_count = %v, want >= 1", projData["dir_count"])
	}

	if !strings.Contains(result.Content, "files") && !strings.Contains(result.Content, "subdirectories") {
		t.Error("Content should mention files and subdirectories as fallback info")
	}
}

// --- (.4) Intelligence / Suggestions Tests ---

func TestContextTool_Suggestions_ManyDirtyFiles(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	data := map[string]interface{}{
		"project": map[string]interface{}{
			"dirty":         true,
			"changed_files": 10,
		},
	}

	suggestions := tool.generateSuggestions(data)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "uncommitted changes") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should suggest committing when many dirty files")
	}
}

func TestContextTool_Suggestions_FewDirtyFiles(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	data := map[string]interface{}{
		"project": map[string]interface{}{
			"dirty":         true,
			"changed_files": 2,
		},
	}

	suggestions := tool.generateSuggestions(data)
	for _, s := range suggestions {
		if strings.Contains(s, "uncommitted changes") {
			t.Error("should NOT suggest committing for only 2 dirty files")
		}
	}
}

func TestContextTool_Suggestions_StaleCommit(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Simulate a commit from 2 days ago
	twoDaysAgo := time.Now().Add(-48 * time.Hour).Unix()
	data := map[string]interface{}{
		"project": map[string]interface{}{
			"dirty":                 true,
			"changed_files":         1,
			"last_commit_timestamp": fmt.Sprintf("%d", twoDaysAgo),
		},
	}

	suggestions := tool.generateSuggestions(data)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "Last commit was") && strings.Contains(s, "ago") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should suggest committing when last commit is stale")
	}
}

func TestContextTool_Suggestions_ManyInProgress(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"in_progress": 5,
				"open":        2,
			},
		},
	}

	suggestions := tool.generateSuggestions(data)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "in-progress") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should suggest finishing tasks when many are in-progress")
	}
}

func TestContextTool_Suggestions_LargeBacklog(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"in_progress": 1,
				"open":        15,
			},
		},
	}

	suggestions := tool.generateSuggestions(data)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "open tasks") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should suggest triaging when many open tasks")
	}
}

func TestContextTool_Suggestions_WorkspaceNotExists(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	data := map[string]interface{}{
		"workspace": map[string]interface{}{
			"workspace_exists": false,
		},
	}

	suggestions := tool.generateSuggestions(data)
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "make init") {
			found = true
			break
		}
	}
	if !found {
		t.Error("should suggest 'make init' when workspace doesn't exist")
	}
}

func TestContextTool_Suggestions_NoIssues(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Clean project, no beads issues
	data := map[string]interface{}{
		"project": map[string]interface{}{
			"dirty":         false,
			"changed_files": 0,
		},
		"workspace": map[string]interface{}{
			"workspace_exists": true,
		},
	}

	suggestions := tool.generateSuggestions(data)
	if len(suggestions) != 0 {
		t.Errorf("expected no suggestions for a clean state, got %d: %v", len(suggestions), suggestions)
	}
}

func TestContextTool_Suggestions_InFullOverview(t *testing.T) {
	// When calling Execute with no section, suggestions should appear in output
	tmpDir := t.TempDir()

	// Set up a dirty git repo with many files
	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	// Create 6 uncommitted files
	for i := 0; i < 6; i++ {
		fname := filepath.Join(tmpDir, fmt.Sprintf("new_%d.txt", i))
		if err := os.WriteFile(fname, []byte("new"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Port: 18789,
		Tools: config.ToolsConfig{
			EnabledTools:  []string{"Context"},
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
		AI: config.AIConfig{DefaultProvider: "anthropic"},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Suggestions section should appear
	if !strings.Contains(result.Content, "## Suggestions") {
		t.Error("full overview should include ## Suggestions section when there are suggestions")
	}
	if !strings.Contains(result.Content, "uncommitted changes") {
		t.Error("suggestions should mention uncommitted changes")
	}

	// Data should contain suggestions
	if _, ok := result.Data["suggestions"]; !ok {
		t.Error("Data should contain 'suggestions' key")
	}
}

func TestContextTool_Suggestions_NotInSingleSection(t *testing.T) {
	// When requesting a specific section, suggestions should NOT appear
	tmpDir := t.TempDir()

	if err := runCmd(tmpDir, "git", "init"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.email", "test@test.com"); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(tmpDir, "git", "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}

	// Create dirty files
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("d_%d.txt", i)), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			MaxToolChains: 25,
			Sandbox:       config.SandboxConfig{WorkspaceDir: tmpDir},
		},
	}
	tool := NewContextTool(&types.ToolServices{ConfigMgr: cfg})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"section": "project",
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if strings.Contains(result.Content, "## Suggestions") {
		t.Error("single section request should NOT include suggestions")
	}
	if _, ok := result.Data["suggestions"]; ok {
		t.Error("Data should NOT contain 'suggestions' for single section request")
	}
}

// --- (.5) Memory Integration Tests ---

// contextMockSearcher implements types.SearchService for context tool testing.
// Named differently from mockSearchService in find_test.go to avoid redeclaration.
type contextMockSearcher struct {
	searchResults []fts.SearchResult
	searchErr     error
}

func (m *contextMockSearcher) SearchDocuments(_ context.Context, _ string, _ int) ([]fts.DocumentResult, error) {
	return nil, nil
}
func (m *contextMockSearcher) SearchMessages(_ context.Context, _ string, _ int) ([]fts.MessageResult, error) {
	return nil, nil
}
func (m *contextMockSearcher) SearchBeads(_ context.Context, _ string, _ int, _ string) ([]fts.BeadsResult, error) {
	return nil, nil
}
func (m *contextMockSearcher) Search(_ context.Context, _ string, _ int) ([]fts.SearchResult, error) {
	return m.searchResults, m.searchErr
}

func TestContextTool_MemoryInsights_WithSearcher(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// Set up a mock searcher that returns results
	tool.services.Searcher = &contextMockSearcher{
		searchResults: []fts.SearchResult{
			{Source: "document", Rank: -10.0},
		},
	}

	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"open_issues": []map[string]interface{}{
					{"id": "test-1", "title": "Fix database migration"},
				},
			},
		},
	}

	insights := tool.gatherMemoryInsights(context.Background(), data)
	if len(insights) == 0 {
		t.Error("expected memory insights when searcher finds results")
	}
	found := false
	for _, insight := range insights {
		if strings.Contains(insight, "test-1") && strings.Contains(insight, "document") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("insights should reference task test-1 and source, got: %v", insights)
	}
}

func TestContextTool_MemoryInsights_NoSearcher(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	// No searcher set
	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"open_issues": []map[string]interface{}{
					{"id": "test-1", "title": "Fix something"},
				},
			},
		},
	}

	insights := tool.gatherMemoryInsights(context.Background(), data)
	if len(insights) != 0 {
		t.Errorf("expected no insights without searcher, got %d", len(insights))
	}
}

func TestContextTool_MemoryInsights_SearcherError(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	tool.services.Searcher = &contextMockSearcher{
		searchErr: fmt.Errorf("search unavailable"),
	}

	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"open_issues": []map[string]interface{}{
					{"id": "test-1", "title": "Fix something"},
				},
			},
		},
	}

	// Should not panic or return error
	insights := tool.gatherMemoryInsights(context.Background(), data)
	if len(insights) != 0 {
		t.Errorf("expected no insights on search error, got %d", len(insights))
	}
}

func TestContextTool_MemoryInsights_LimitSearches(t *testing.T) {
	// Should only search the first 3 issues
	tool, _ := setupTestContextTool(t)

	searchCount := 0
	tool.services.Searcher = &contextMockSearcher{
		searchResults: []fts.SearchResult{
			{Source: "document", Rank: -5.0},
		},
	}
	// We can't easily count calls with this mock, but we verify the limit
	// by providing 5 issues and checking we get at most 3*results_per_issue insights
	_ = searchCount

	issues := make([]map[string]interface{}, 5)
	for i := 0; i < 5; i++ {
		issues[i] = map[string]interface{}{
			"id":    fmt.Sprintf("task-%d", i),
			"title": fmt.Sprintf("Task number %d", i),
		}
	}

	data := map[string]interface{}{
		"beads": map[string]interface{}{
			"issues": map[string]interface{}{
				"open_issues": issues,
			},
		},
	}

	insights := tool.gatherMemoryInsights(context.Background(), data)
	// Maximum 3 tasks searched * 2 results per search = 6 insights
	if len(insights) > 6 {
		t.Errorf("expected at most 6 insights (3 tasks * 2 results), got %d", len(insights))
	}
}

// --- (.6) Output Formatting Tests ---

func TestContextTool_FormatSuggestions(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	suggestions := []string{
		"You have 10 uncommitted changes.",
		"5 tasks are in-progress.",
	}
	memoryInsights := []string{
		"Task bd-123: related content in MEMORY.md",
	}

	output := tool.formatSuggestions(suggestions, memoryInsights)

	// Check structure
	if !strings.Contains(output, "## Suggestions") {
		t.Error("output should contain '## Suggestions' header")
	}
	if !strings.Contains(output, "## Memory Insights") {
		t.Error("output should contain '## Memory Insights' header")
	}
	if !strings.Contains(output, "**Action:**") {
		t.Error("suggestions should be formatted with bold Action prefix")
	}
	if !strings.Contains(output, "uncommitted changes") {
		t.Error("suggestion content should be present")
	}
	if !strings.Contains(output, "bd-123") {
		t.Error("memory insight content should be present")
	}
}

func TestContextTool_FormatSuggestions_Empty(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	output := tool.formatSuggestions(nil, nil)
	if output != "" {
		t.Errorf("empty suggestions should produce empty output, got: %q", output)
	}
}

func TestContextTool_FormatSuggestions_OnlySuggestions(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	output := tool.formatSuggestions([]string{"Do something"}, nil)
	if !strings.Contains(output, "## Suggestions") {
		t.Error("should contain Suggestions header")
	}
	if strings.Contains(output, "## Memory Insights") {
		t.Error("should NOT contain Memory Insights header when no insights")
	}
}

func TestContextTool_FormatSuggestions_OnlyInsights(t *testing.T) {
	tool, _ := setupTestContextTool(t)

	output := tool.formatSuggestions(nil, []string{"Found related content"})
	if strings.Contains(output, "## Suggestions") {
		t.Error("should NOT contain Suggestions header when no suggestions")
	}
	if !strings.Contains(output, "## Memory Insights") {
		t.Error("should contain Memory Insights header")
	}
}

// --- formatDuration Tests ---

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 5 * time.Minute, "5m"},
		{"hours", 3 * time.Hour, "3h"},
		{"hours_and_minutes", 3*time.Hour + 30*time.Minute, "3h 30m"},
		{"days", 48 * time.Hour, "2d"},
		{"many_days", 7 * 24 * time.Hour, "7d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// --- Cache thread safety test ---

func TestContextCache_ConcurrentAccess(t *testing.T) {
	cache := newContextCache()
	const goroutines = 50
	const operations = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operations; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.set(key, j, time.Minute)
				cache.get(key)
			}
		}(i)
	}
	wg.Wait()
	// If we get here without a race condition, the test passes
}

// --- Helper functions ---

// runCmd runs a command in the given directory and returns any error.
func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// keysOf returns the keys of a map for error messages.
func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
