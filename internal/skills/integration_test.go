package skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSkillsToolIntegration tests the complete skills-to-tools integration
func TestSkillsToolIntegration(t *testing.T) {
	// Create a skills manager with test configuration
	config := SkillsConfig{
		Enabled:     true,
		SearchPaths: []string{"testdata"},
		Execution: ExecutionConfig{
			TimeoutSeconds: 10,
			Environment:    map[string]string{},
			AllowedActions: map[string][]string{},
		},
		Cache: CacheConfig{
			TTLSeconds: 300,
			Enabled:    true,
		},
	}

	manager := NewManager(config)

	// Test that tool adapters can be generated
	adapters, err := GenerateToolAdapters(context.Background(), manager)
	if err != nil {
		t.Errorf("GenerateToolAdapters failed: %v", err)
		return
	}

	// Should return a valid slice even with no skills (never nil)
	// An empty slice `[]` is perfectly valid when there are no skills
	t.Logf("Adapters: %v, len: %d", adapters, len(adapters))

	// With no skills in testdata, should return empty slice (this is expected behavior)
	t.Logf("Generated %d tool adapters", len(adapters))
}

// TestSkillToolAdapter tests the SkillToolAdapter implementation
func TestSkillToolAdapter(t *testing.T) {
	// Create a mock skill tool
	mockSkillTool := &MockSkillTool{
		name:        "test_skill",
		description: "Test skill for adapter testing",
		parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"description": "Action to perform",
				},
			},
		},
	}

	// Create adapter
	adapter := NewSkillToolAdapter(mockSkillTool)

	// Test basic methods
	if adapter.Name() != "test_skill" {
		t.Errorf("Expected name 'test_skill', got '%s'", adapter.Name())
	}

	if adapter.Description() != "Test skill for adapter testing" {
		t.Errorf("Expected description 'Test skill for adapter testing', got '%s'", adapter.Description())
	}

	params := adapter.Parameters()
	if params == nil {
		t.Error("Expected non-nil parameters")
	}

	// Test execute method
	ctx := context.Background()
	args := map[string]interface{}{
		"action": "test",
	}

	result, err := adapter.Execute(ctx, args)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result")
	} else {
		if !result.Success {
			t.Error("Expected successful result")
		}
		if result.Content != "test execution completed" {
			t.Errorf("Expected content 'test execution completed', got '%s'", result.Content)
		}
	}
}

// TestDisabledSkillsManager tests behavior with disabled manager
func TestDisabledSkillsManager(t *testing.T) {
	// Create a disabled skills manager
	config := SkillsConfig{
		Enabled: false,
	}

	manager := NewManager(config)

	// Test that no tools are generated when disabled
	adapters, err := GenerateToolAdapters(context.Background(), manager)
	if err != nil {
		t.Errorf("GenerateToolAdapters failed: %v", err)
	}

	if len(adapters) != 0 {
		t.Errorf("Expected 0 adapters for disabled manager, got %d", len(adapters))
	}

	// Test with nil manager
	adapters, err = GenerateToolAdapters(context.Background(), nil)
	if err != nil {
		t.Errorf("GenerateToolAdapters with nil manager failed: %v", err)
	}

	if len(adapters) != 0 {
		t.Errorf("Expected 0 adapters for nil manager, got %d", len(adapters))
	}
}

// MockSkillTool implements SkillToolInterface for testing
type MockSkillTool struct {
	name        string
	description string
	parameters  map[string]interface{}
}

func (m *MockSkillTool) Name() string {
	return m.name
}

func (m *MockSkillTool) Description() string {
	return m.description
}

func (m *MockSkillTool) Parameters() map[string]interface{} {
	return m.parameters
}

func (m *MockSkillTool) Execute(ctx context.Context, args map[string]interface{}) (*SkillToolResult, error) {
	// Simulate execution
	time.Sleep(1 * time.Millisecond)

	return &SkillToolResult{
		Success: true,
		Content: "test execution completed",
		Data: map[string]interface{}{
			"args": args,
		},
	}, nil
}

// --- conduit-1jd9 regression tests: skill description bloat + honest actions ---

func mkBloatSkill(name, desc string) Skill {
	return Skill{
		Name:        name,
		Description: desc,
		Content:     "Some content with 'do NOT trigger' prose that used to be scraped.",
	}
}

// TestBuildSkillsContext_CompactFormat pins the one-line-per-skill format:
// no full-description duplication (the catalog already carries it) and no
// Location lines.
func TestBuildSkillsContext_CompactFormat(t *testing.T) {
	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: nil}
	longDesc := strings.Repeat("Trigger words. ", 40) // ~560 chars of bloat
	skills := []Skill{mkBloatSkill("spot", longDesc)}

	out := integrator.BuildSkillsContext(skills)

	if !strings.Contains(out, "skill_spot") {
		t.Errorf("section should name the tool, got: %s", out)
	}
	if strings.Count(out, "Trigger words.") > 2 {
		t.Errorf("full description must not be repeated in prompt section; got: %s", out)
	}
	if strings.Contains(out, "Location:") || strings.Contains(out, "**Tool:**") {
		t.Errorf("legacy verbose format still present; got: %s", out)
	}
	if len(out) > 2000 {
		t.Errorf("section for one skill should be compact; got %d chars: %s", len(out), out)
	}
}

// TestTruncateDescription word-boundary and sentence-preference behavior.
func TestTruncateDescription(t *testing.T) {
	short := "Short description."
	if got := truncateDescription(short, 160); got != short {
		t.Errorf("short desc should pass through, got %q", got)
	}

	long := strings.Repeat("word ", 60) // 300 chars, no sentence break
	got := truncateDescription(long, 100)
	if len(got) > 105 || !strings.HasSuffix(got, "…") {
		t.Errorf("expected word-boundary truncation with ellipsis, got %q (len %d)", got, len(got))
	}

	sentency := "First sentence is here. Then a long tail " + strings.Repeat("x", 200)
	if got := truncateDescription(sentency, 160); got != "First sentence is here." {
		t.Errorf("expected first-sentence truncation, got %q", got)
	}
}

// TestParameters_EnumOnlyWithHonestActions pins the schema contract: enum
// present when honest actions exist (frontmatter or scripts), omitted
// otherwise — never populated from prose scraping.
func TestParameters_EnumOnlyWithHonestActions(t *testing.T) {
	executor := &Executor{}
	loader := NewSkillLoader()

	// Explicit frontmatter actions → enum present.
	skill, err := loader.LoadSkillFromContent("---\nname: t\ndescription: d\nactions: [search, send]\n---\nbody")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tool := &SkillTool{skill: *skill, executor: executor}
	params := tool.Parameters()
	props := params["properties"].(map[string]interface{})
	actionProp := props["action"].(map[string]interface{})
	if enum, ok := actionProp["enum"].([]string); !ok || len(enum) != 2 {
		t.Errorf("expected enum [search send], got %v", actionProp["enum"])
	}

	// No actions, no scripts → enum omitted, tool still usable.
	empty := &SkillTool{skill: Skill{Name: "e", Description: "d"}, executor: executor}
	props2 := empty.Parameters()["properties"].(map[string]interface{})
	actionProp2 := props2["action"].(map[string]interface{})
	if _, hasEnum := actionProp2["enum"]; hasEnum {
		t.Errorf("enum must be omitted when no honest actions exist; got %v", actionProp2["enum"])
	}
	if actionProp2["description"] == "" {
		t.Error("action param must still carry a description")
	}
}

// --- bd-3f3l.1: SkillTool.Execute must hoist top-level tool-call params ---

// captureSkillForTool runs a SkillTool against a real executor + temp-dir bash
// script that echoes its stdin envelope back as JSON, then returns the decoded
// map the skill invocation actually received.
func captureSkillForTool(t *testing.T, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	dir := t.TempDir()
	skill := writeScript(t, dir, "dispatch.sh", `#!/bin/bash
line=$(cat)
echo "$line"
`)
	skill.Name = "hoist-probe"
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	tool := &SkillTool{skill: skill, executor: e}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("SkillTool.Execute error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("skill probe failed: %v (output: %s)", result, resultContentFor(result))
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatalf("probe output not JSON: %v (raw: %q)", err, result.Content)
	}
	return got
}

// TestSkillTool_ExecuteHoistsTopLevelParams: providers send extra top-level
// keys (e.g. account, query) in skill_<name> tool calls; the schema only
// declares action+args with no additionalProperties:false, so those keys pass
// validation and are silently dropped today. They must reach the skill.
func TestSkillTool_ExecuteHoistsTopLevelParams(t *testing.T) {
	got := captureSkillForTool(t, map[string]interface{}{
		"action":  "dispatch",
		"account": "test@example.com", // synthetic — public repo
		"query":   "invoice",
		"args":    map[string]interface{}{"limit": 10},
	})

	if acct, ok := got["account"].(string); !ok || acct != "test@example.com" {
		t.Errorf("top-level account not hoisted into skill args; got: %v", got)
	}
	if q, ok := got["query"].(string); !ok || q != "invoice" {
		t.Errorf("top-level query not hoisted into skill args; got: %v", got)
	}
	if lim, ok := got["limit"].(float64); !ok || lim != 10 {
		t.Errorf("nested args.limit lost during hoist; got: %v", got)
	}
	if act, ok := got["action"].(string); !ok || act != "dispatch" {
		t.Errorf("action key must not be hoisted into skill args; got: %v", got)
	}
}

// TestSkillTool_ExecuteArgsWinOverHoisted: when a key exists both top-level
// and inside args, the explicit args value must win (the nested args object is
// the documented channel; top-level keys are the convenience passthrough).
func TestSkillTool_ExecuteArgsWinOverHoisted(t *testing.T) {
	got := captureSkillForTool(t, map[string]interface{}{
		"action":  "dispatch",
		"account": "top-level@example.com",
		"args": map[string]interface{}{
			"account": "nested@example.com",
		},
	})

	if acct, ok := got["account"].(string); !ok || acct != "nested@example.com" {
		t.Errorf("nested args.account must win over hoisted top-level account; got: %v", got)
	}
}

// TestSkillTool_ExecuteNoHoistBypass: without extra top-level keys, behavior
// is unchanged — nested args flow through untouched (regression guard).
func TestSkillTool_ExecuteNoHoistBypass(t *testing.T) {
	got := captureSkillForTool(t, map[string]interface{}{
		"action": "dispatch",
		"args":   map[string]interface{}{"payload": "keep-me"},
	})

	if len(got) != 2 { // payload + action envelope key
		t.Errorf("unexpected envelope keys; got: %v", got)
	}
	if p, ok := got["payload"].(string); !ok || p != "keep-me" {
		t.Errorf("payload lost; got: %v", got)
	}
	if _, leaked := got["args"]; leaked {
		t.Errorf("raw args container must not be forwarded as a key; got: %v", got)
	}
}

// --- bd-312l: SkillActionTool.Execute hoist must be args-wins ---

// captureSkillForActionTool runs a SkillActionTool against a real executor +
// temp-dir bash script that echoes its stdin envelope back as JSON, then
// returns the decoded map the skill invocation actually received.
func captureSkillForActionTool(t *testing.T, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	dir := t.TempDir()
	skill := writeScript(t, dir, "search.sh", `#!/bin/bash
line=$(cat)
echo "$line"
`)
	skill.Name = "hoist-probe"
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	tool := &SkillActionTool{skill: skill, action: "search", executor: e}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("SkillActionTool.Execute error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("skill probe failed: %v (output: %s)", result, resultContentFor(result))
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &got); err != nil {
		t.Fatalf("probe output not JSON: %v (raw: %q)", err, result.Content)
	}
	return got
}

func resultContentFor(r *SkillToolResult) string {
	if r == nil {
		return "<nil result>"
	}
	return r.Content
}

// TestSkillActionTool_ExecuteArgsWinOverHoisted: SkillActionTool.Execute
// hoists action-specific top-level params (e.g. query for a search tool) into
// skill args, but the hoist is unconditional — a same-named key inside nested
// args is silently clobbered by the top-level copy. The nested args object is
// the documented channel, so explicit args keys must win on conflict
// (mirrors TestSkillTool_ExecuteArgsWinOverHoisted from bd-3f3l.1).
func TestSkillActionTool_ExecuteArgsWinOverHoisted(t *testing.T) {
	got := captureSkillForActionTool(t, map[string]interface{}{
		"query": "top-level-query",
		"args": map[string]interface{}{
			"query": "nested-query",
		},
	})

	if q, ok := got["query"].(string); !ok || q != "nested-query" {
		t.Errorf("nested args.query must win over hoisted top-level query; got: %v", got)
	}
}

// TestSkillActionTool_ExecuteHoistsTopLevelParams: action-specific top-level
// params (query, location, ...) declared in the tool schema must still reach
// the skill when the provider sends them at the top level — args-wins must
// not become args-only (regression guard for the bd-312l fix).
func TestSkillActionTool_ExecuteHoistsTopLevelParams(t *testing.T) {
	got := captureSkillForActionTool(t, map[string]interface{}{
		"query":    "invoice",
		"location": "Seattle",
		"args":     map[string]interface{}{"limit": 10},
	})

	if q, ok := got["query"].(string); !ok || q != "invoice" {
		t.Errorf("top-level query not hoisted into skill args; got: %v", got)
	}
	if loc, ok := got["location"].(string); !ok || loc != "Seattle" {
		t.Errorf("top-level location not hoisted into skill args; got: %v", got)
	}
	if lim, ok := got["limit"].(float64); !ok || lim != 10 {
		t.Errorf("nested args.limit lost during hoist; got: %v", got)
	}
	if _, leaked := got["args"]; leaked {
		t.Errorf("raw args container must not be forwarded as a key; got: %v", got)
	}
}
