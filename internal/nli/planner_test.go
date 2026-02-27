package nli

import (
	"testing"
	"time"
)

func TestNewConversationPlanner(t *testing.T) {
	cp := NewConversationPlanner()
	if cp == nil {
		t.Fatal("NewConversationPlanner returned nil")
	}
	if cp.parser == nil {
		t.Error("parser not initialized")
	}
}

func TestPlanExecution_Empty(t *testing.T) {
	cp := NewConversationPlanner()
	plan := cp.PlanExecution(nil)

	if plan == nil {
		t.Fatal("PlanExecution returned nil for empty intents")
	}
	if len(plan.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(plan.Steps))
	}
	if plan.ID == "" {
		t.Error("plan ID should not be empty")
	}
}

func TestPlanExecution_SingleIntent(t *testing.T) {
	cp := NewConversationPlanner()
	intents := []Intent{
		{
			Action:     ActionQuery,
			Target:     "weather data",
			Parameters: map[string]interface{}{"query": "weather"},
			Entities:   nil,
			Confidence: 0.9,
			RawText:    "search for weather data",
		},
	}

	plan := cp.PlanExecution(intents)

	if len(plan.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].ToolName == "" {
		t.Error("tool name should be resolved")
	}
	if len(plan.Steps[0].DependsOn) != 0 {
		t.Error("single step should have no dependencies")
	}
}

func TestPlanExecution_MultipleIntents(t *testing.T) {
	cp := NewConversationPlanner()
	intents := []Intent{
		{
			Action:     ActionQuery,
			Target:     "web search results",
			Parameters: map[string]interface{}{},
			Entities:   nil,
			Confidence: 0.8,
			RawText:    "search the web for Go tutorials",
		},
		{
			Action:     ActionQuery,
			Target:     "fetch url",
			Parameters: map[string]interface{}{"url": "https://example.com"},
			Entities: []Entity{
				{Type: EntityURL, Value: "https://example.com"},
			},
			Confidence: 0.9,
			RawText:    "fetch https://example.com",
		},
		{
			Action:     ActionCreate,
			Target:     "file",
			Parameters: map[string]interface{}{"path": "/tmp/result.txt"},
			Entities: []Entity{
				{Type: EntityFilePath, Value: "/tmp/result.txt"},
			},
			Confidence: 0.85,
			RawText:    "save to /tmp/result.txt",
		},
	}

	plan := cp.PlanExecution(intents)

	if len(plan.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(plan.Steps))
	}

	if plan.EstimatedDuration <= 0 {
		t.Error("estimated duration should be positive")
	}

	if plan.ID == "" {
		t.Error("plan ID should not be empty")
	}
}

func TestPlanExecution_ToolResolution(t *testing.T) {
	cp := NewConversationPlanner()

	tests := []struct {
		intent       Intent
		expectedTool string
	}{
		{
			intent: Intent{
				Action:     ActionQuery,
				Target:     "web results",
				Parameters: map[string]interface{}{},
				Entities:   nil,
				Confidence: 0.9,
			},
			expectedTool: "web_search",
		},
		{
			intent: Intent{
				Action:     ActionQuery,
				Target:     "something",
				Parameters: map[string]interface{}{},
				Entities: []Entity{
					{Type: EntityURL, Value: "https://example.com"},
				},
				Confidence: 0.9,
			},
			expectedTool: "web_fetch",
		},
		{
			intent: Intent{
				Action:     ActionCreate,
				Target:     "file",
				Parameters: map[string]interface{}{},
				Entities: []Entity{
					{Type: EntityFilePath, Value: "/tmp/test.go"},
				},
				Confidence: 0.9,
			},
			expectedTool: "write_file",
		},
		{
			intent: Intent{
				Action:     ActionModify,
				Target:     "file",
				Parameters: map[string]interface{}{},
				Entities: []Entity{
					{Type: EntityFilePath, Value: "/tmp/test.go"},
				},
				Confidence: 0.9,
			},
			expectedTool: "edit_file",
		},
		{
			intent: Intent{
				Action:     ActionCommand,
				Target:     "something",
				Parameters: map[string]interface{}{"tool": "send_message"},
				Entities:   nil,
				Confidence: 0.9,
			},
			expectedTool: "send_message",
		},
		{
			intent: Intent{
				Action:     ActionQuery,
				Target:     "memory recall",
				Parameters: map[string]interface{}{},
				Entities:   nil,
				Confidence: 0.9,
			},
			expectedTool: "memory_search",
		},
	}

	for i, tc := range tests {
		plan := cp.PlanExecution([]Intent{tc.intent})
		if len(plan.Steps) != 1 {
			t.Errorf("test %d: expected 1 step", i)
			continue
		}
		if plan.Steps[0].ToolName != tc.expectedTool {
			t.Errorf("test %d: expected tool %s, got %s", i, tc.expectedTool, plan.Steps[0].ToolName)
		}
	}
}

func TestPlanExecution_Dependencies(t *testing.T) {
	cp := NewConversationPlanner()

	// Create intents where step 2 (web_fetch) might depend on step 1 (web_search).
	intents := []Intent{
		{
			Action:     ActionQuery,
			Target:     "web search",
			Parameters: map[string]interface{}{},
			Entities: []Entity{
				{Type: EntityToolName, Value: "web_search"},
			},
			Confidence: 0.9,
			RawText:    "search the web",
		},
		{
			Action:     ActionQuery,
			Target:     "fetch results",
			Parameters: map[string]interface{}{},
			Entities: []Entity{
				{Type: EntityURL, Value: "https://example.com"},
			},
			Confidence: 0.8,
			RawText:    "fetch the URL https://example.com",
		},
	}

	plan := cp.PlanExecution(intents)

	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}

	// Step 2 should depend on step 1 (web_search produces URLs, web_fetch consumes them).
	if len(plan.Steps[1].DependsOn) == 0 {
		t.Error("expected step 2 to have dependencies on step 1")
	}
}

func TestValidatePlan_NilPlan(t *testing.T) {
	cp := NewConversationPlanner()
	errors := cp.ValidatePlan(nil)

	if len(errors) == 0 {
		t.Error("expected validation error for nil plan")
	}
}

func TestValidatePlan_ValidPlan(t *testing.T) {
	cp := NewConversationPlanner()
	plan := &ExecutionPlan{
		ID: "test_plan",
		Steps: []ExecutionStep{
			{
				ToolName:   "web_search",
				Parameters: map[string]interface{}{"query": "test"},
				DependsOn:  nil,
			},
			{
				ToolName:   "web_fetch",
				Parameters: map[string]interface{}{"url": "https://example.com"},
				DependsOn:  []int{0},
			},
		},
		CreatedAt: time.Now(),
	}

	errors := cp.ValidatePlan(plan)
	if len(errors) != 0 {
		t.Errorf("expected no validation errors, got %d: %v", len(errors), errors)
	}
}

func TestValidatePlan_MissingToolName(t *testing.T) {
	cp := NewConversationPlanner()
	plan := &ExecutionPlan{
		ID: "test_plan",
		Steps: []ExecutionStep{
			{
				ToolName:   "",
				Parameters: map[string]interface{}{},
			},
		},
		CreatedAt: time.Now(),
	}

	errors := cp.ValidatePlan(plan)
	found := false
	for _, e := range errors {
		if e.Field == "tool_name" {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for missing tool name")
	}
}

func TestValidatePlan_InvalidDependency(t *testing.T) {
	cp := NewConversationPlanner()
	plan := &ExecutionPlan{
		ID: "test_plan",
		Steps: []ExecutionStep{
			{
				ToolName:  "web_search",
				DependsOn: []int{5}, // invalid: no step 5
			},
		},
		CreatedAt: time.Now(),
	}

	errors := cp.ValidatePlan(plan)
	found := false
	for _, e := range errors {
		if e.Field == "depends_on" {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for invalid dependency index")
	}
}

func TestPlanExecution_RollbackGeneration(t *testing.T) {
	cp := NewConversationPlanner()

	intents := []Intent{
		{
			Action:     ActionModify,
			Target:     "file",
			Parameters: map[string]interface{}{"path": "/tmp/config.json"},
			Entities: []Entity{
				{Type: EntityFilePath, Value: "/tmp/config.json"},
			},
			Confidence: 0.9,
		},
	}

	plan := cp.PlanExecution(intents)

	if len(plan.RollbackSteps) == 0 {
		t.Error("expected rollback steps for modify action with file path")
	}
}

func TestPlanExecution_EstimatedDuration(t *testing.T) {
	cp := NewConversationPlanner()

	intents := []Intent{
		{
			Action:     ActionQuery,
			Target:     "web search",
			Parameters: map[string]interface{}{},
			Entities: []Entity{
				{Type: EntityToolName, Value: "web_search"},
			},
			Confidence: 0.9,
		},
		{
			Action:     ActionQuery,
			Target:     "memory",
			Parameters: map[string]interface{}{},
			Entities: []Entity{
				{Type: EntityToolName, Value: "memory_search"},
			},
			Confidence: 0.9,
		},
	}

	plan := cp.PlanExecution(intents)

	// web_search (2s) + memory_search (500ms) = 2.5s
	expectedMin := 2*time.Second + 500*time.Millisecond
	if plan.EstimatedDuration < expectedMin {
		t.Errorf("expected estimated duration >= %v, got %v", expectedMin, plan.EstimatedDuration)
	}
}

func TestContainsWord(t *testing.T) {
	tests := []struct {
		text     string
		word     string
		expected bool
	}{
		{"search the web", "search", true},
		{"websearch results", "search", false},
		{"web search results", "search", true},
		{"SEARCH for things", "search", true},
		{"no match here", "search", false},
		{"", "search", false},
		{"search", "search", true},
	}

	for _, tc := range tests {
		result := containsWord(tc.text, tc.word)
		if result != tc.expected {
			t.Errorf("containsWord(%q, %q) = %v, want %v", tc.text, tc.word, result, tc.expected)
		}
	}
}

func TestMergeParameters(t *testing.T) {
	params := map[string]interface{}{
		"query": "test",
	}
	entities := []Entity{
		{Type: EntityURL, Value: "https://example.com"},
		{Type: EntityFilePath, Value: "/tmp/file.txt"},
	}

	merged := mergeParameters(params, entities)

	if merged["query"] != "test" {
		t.Error("original parameter should be preserved")
	}
	if merged["url"] != "https://example.com" {
		t.Error("URL entity should be added")
	}
	if merged["path"] != "/tmp/file.txt" {
		t.Error("file path entity should be added")
	}
}
