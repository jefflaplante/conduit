//go:build with_sre

package sre

import (
	"context"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// mockToolExecutor implements ToolExecutor for testing.
type mockToolExecutor struct {
	results map[string]*types.ToolResult
	calls   []mockCall
}

type mockCall struct {
	ToolName string
	Args     map[string]interface{}
}

func newMockExecutor() *mockToolExecutor {
	return &mockToolExecutor{
		results: make(map[string]*types.ToolResult),
		calls:   make([]mockCall, 0),
	}
}

func (m *mockToolExecutor) setResult(toolName string, result *types.ToolResult) {
	m.results[toolName] = result
}

func (m *mockToolExecutor) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
	m.calls = append(m.calls, mockCall{ToolName: name, Args: args})
	if result, ok := m.results[name]; ok {
		return result, nil
	}
	return &types.ToolResult{Success: false, Error: "mock not configured"}, nil
}

func setupTestTool(t *testing.T) (*SRETool, *mockToolExecutor) {
	t.Helper()

	cfg := &config.Config{
		PagerDuty: config.PagerDutyConfig{
			Enabled:  true,
			APIToken: "test-key",
		},
		Datadog: config.DatadogConfig{
			Enabled: true,
			APIKey:  "test-key",
			AppKey:  "test-app-key",
		},
		Kubernetes: config.KubernetesConfig{
			Enabled: true,
			Clusters: []config.KubernetesCluster{
				{Name: "test-cluster"},
			},
		},
		RemoteSSH: config.RemoteSSHConfig{
			Enabled: true,
			Hosts: []config.SSHHostConfig{
				{Name: "test-host", Hostname: "192.168.1.100"},
			},
		},
	}

	services := &types.ToolServices{
		ConfigMgr: cfg,
	}

	executor := newMockExecutor()

	tool, err := NewSRETool(services, executor)
	if err != nil {
		t.Fatalf("Failed to create SRE tool: %v", err)
	}

	return tool, executor
}

func TestSRETool_Name(t *testing.T) {
	tool, _ := setupTestTool(t)
	if tool.Name() != "Sre" {
		t.Errorf("Expected name 'Sre', got '%s'", tool.Name())
	}
}

func TestSRETool_Description(t *testing.T) {
	tool, _ := setupTestTool(t)
	desc := tool.Description()
	if desc == "" {
		t.Error("Expected non-empty description")
	}
	if !containsString(desc, "triage_incident") {
		t.Error("Description should mention triage_incident action")
	}
	if !containsString(desc, "correlate") {
		t.Error("Description should mention correlate action")
	}
}

func TestSRETool_Parameters(t *testing.T) {
	tool, _ := setupTestTool(t)
	params := tool.Parameters()

	if params["type"] != "object" {
		t.Error("Parameters should be an object type")
	}

	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters should have properties")
	}

	// Check action parameter
	action, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have action parameter")
	}
	if action["type"] != "string" {
		t.Error("action should be string type")
	}

	// Check enum values
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action should have enum values")
	}
	expectedActions := []string{"triage_incident", "correlate", "suggest_investigation", "status"}
	for _, expected := range expectedActions {
		found := false
		for _, actual := range enum {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing expected action: %s", expected)
		}
	}
}

func TestSRETool_GetActionDocs(t *testing.T) {
	tool, _ := setupTestTool(t)
	docs := tool.GetActionDocs()

	if len(docs) == 0 {
		t.Fatal("Should have action documentation")
	}

	// Check triage_incident doc
	triageDoc, ok := docs["triage_incident"]
	if !ok {
		t.Fatal("Should have triage_incident documentation")
	}
	if triageDoc.Description == "" {
		t.Error("triage_incident should have description")
	}
	if len(triageDoc.RequiredParams) == 0 {
		t.Error("triage_incident should have required params")
	}
}

func TestSRETool_Execute_MissingAction(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Should fail without action parameter")
	}
	if !containsString(result.Error, "action") {
		t.Error("Error should mention missing action")
	}
}

func TestSRETool_Execute_InvalidAction(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Should fail with invalid action")
	}
}

func TestSRETool_Execute_Status(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Status should succeed: %s", result.Error)
	}

	// Check data
	if result.Data == nil {
		t.Fatal("Should have data")
	}
	integrations, ok := result.Data["integrations"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have integrations in data")
	}

	// Check PagerDuty
	pd, ok := integrations["pagerduty"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have pagerduty integration")
	}
	if pd["enabled"] != true {
		t.Error("PagerDuty should be enabled")
	}

	// Check Kubernetes
	k8s, ok := integrations["kubernetes"].(map[string]interface{})
	if !ok {
		t.Fatal("Should have kubernetes integration")
	}
	if k8s["enabled"] != true {
		t.Error("Kubernetes should be enabled")
	}
}

func TestSRETool_Execute_TriageIncident_MissingID(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "triage_incident",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Should fail without incident_id")
	}
	if !containsString(result.Error, "incident_id") {
		t.Error("Error should mention missing incident_id")
	}
}

func TestSRETool_Execute_TriageIncident(t *testing.T) {
	tool, executor := setupTestTool(t)

	// Mock PagerDuty response
	executor.setResult("PagerDuty", &types.ToolResult{
		Success: true,
		Content: "Incident details",
		Data: map[string]interface{}{
			"id":         "P12345",
			"title":      "High CPU on api-server",
			"status":     "triggered",
			"urgency":    "high",
			"created_at": "2024-01-15T10:00:00Z",
			"service": map[string]interface{}{
				"id":   "SVCABC",
				"name": "api-server",
			},
		},
	})

	// Mock Datadog responses
	executor.setResult("Datadog", &types.ToolResult{
		Success: true,
		Content: "Metrics data",
		Data: map[string]interface{}{
			"series": []interface{}{
				map[string]interface{}{"metric": "system.cpu.user"},
			},
		},
	})

	// Mock Kubernetes response
	executor.setResult("Kubernetes", &types.ToolResult{
		Success: true,
		Content: "Pod list",
		Data: map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"name": "api-server-abc", "phase": "Running"},
			},
		},
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "triage_incident",
		"incident_id": "P12345",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Triage should succeed: %s", result.Error)
	}

	// Check that tools were called
	if len(executor.calls) == 0 {
		t.Error("Should have called other tools")
	}

	// Verify PagerDuty was called
	pdCalled := false
	for _, call := range executor.calls {
		if call.ToolName == "PagerDuty" {
			pdCalled = true
			if call.Args["action"] != "get_incident" {
				t.Error("Should call PagerDuty get_incident")
			}
		}
	}
	if !pdCalled {
		t.Error("Should call PagerDuty tool")
	}

	// Check response data
	if result.Data == nil {
		t.Fatal("Should have data")
	}
	if result.Data["incident_title"] != "High CPU on api-server" {
		t.Error("Should include incident title")
	}
}

func TestSRETool_Execute_Correlate_MissingService(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "correlate",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Should fail without service")
	}
}

func TestSRETool_Execute_Correlate(t *testing.T) {
	tool, executor := setupTestTool(t)

	// Mock PagerDuty list incidents
	executor.setResult("PagerDuty", &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"incidents": []interface{}{
				map[string]interface{}{
					"id":     "P12345",
					"title":  "api-server high latency",
					"status": "triggered",
					"service": map[string]interface{}{
						"name": "api-server",
					},
				},
			},
		},
	})

	// Mock Datadog monitors
	executor.setResult("DatadogMonitor", &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"monitors": []interface{}{
				map[string]interface{}{
					"name":   "api-server latency",
					"status": "Alert",
				},
			},
		},
	})

	// Mock Datadog logs
	executor.setResult("Datadog", &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"logs": []interface{}{},
		},
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "correlate",
		"service": "api-server",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Correlate should succeed: %s", result.Error)
	}

	if result.Data == nil {
		t.Fatal("Should have data")
	}
	if result.Data["service"] != "api-server" {
		t.Error("Should include service name")
	}
}

func TestSRETool_Execute_SuggestInvestigation_MissingParams(t *testing.T) {
	tool, _ := setupTestTool(t)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "suggest_investigation",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Error("Should fail without incident_id or incident_type")
	}
}

func TestSRETool_Execute_SuggestInvestigation_ByType(t *testing.T) {
	tool, _ := setupTestTool(t)

	testCases := []struct {
		incidentType  string
		expectK8s     bool
		expectDatadog bool
		expectSSH     bool
	}{
		{"high_cpu", true, true, false}, // SSH not configured in test
		{"oom", true, true, false},
		{"5xx_errors", true, true, false},
		{"latency", true, true, false},
		{"unknown", true, true, false},
	}

	for _, tc := range testCases {
		t.Run(tc.incidentType, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"action":        "suggest_investigation",
				"incident_type": tc.incidentType,
			})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if !result.Success {
				t.Errorf("Should succeed for type %s: %s", tc.incidentType, result.Error)
			}

			if result.Data == nil {
				t.Fatal("Should have data")
			}

			suggestions, ok := result.Data["suggestions"].([]map[string]interface{})
			if !ok {
				t.Fatal("Should have suggestions array")
			}

			if len(suggestions) == 0 {
				t.Error("Should have at least one suggestion")
			}

			// Check categories are present as expected
			hasK8s := false
			hasDatadog := false
			for _, s := range suggestions {
				if s["category"] == "k8s" {
					hasK8s = true
				}
				if s["category"] == "datadog" {
					hasDatadog = true
				}
			}

			if tc.expectK8s && !hasK8s {
				t.Errorf("Expected K8s suggestion for %s", tc.incidentType)
			}
			if tc.expectDatadog && !hasDatadog {
				t.Errorf("Expected Datadog suggestion for %s", tc.incidentType)
			}
		})
	}
}

func TestNewSRETool_RequiresBothPDAndDD(t *testing.T) {
	// Test with only PagerDuty
	cfg := &config.Config{
		PagerDuty: config.PagerDutyConfig{Enabled: true},
		Datadog:   config.DatadogConfig{Enabled: false},
	}
	services := &types.ToolServices{ConfigMgr: cfg}
	executor := newMockExecutor()

	_, err := NewSRETool(services, executor)
	if err == nil {
		t.Error("Should fail without Datadog enabled")
	}

	// Test with only Datadog
	cfg = &config.Config{
		PagerDuty: config.PagerDutyConfig{Enabled: false},
		Datadog:   config.DatadogConfig{Enabled: true},
	}
	services = &types.ToolServices{ConfigMgr: cfg}

	_, err = NewSRETool(services, executor)
	if err == nil {
		t.Error("Should fail without PagerDuty enabled")
	}
}

func TestInferIncidentType(t *testing.T) {
	tool, executor := setupTestTool(t)

	testCases := []struct {
		title    string
		expected string
	}{
		{"High CPU usage on api-server", "high_cpu"},
		{"OOM killed on worker node", "oom"},
		{"Out of memory exception", "oom"},
		{"5xx errors spike", "5xx_errors"},
		{"502 Bad Gateway", "5xx_errors"},
		{"High latency on checkout", "latency"},
		{"Timeout errors", "latency"},
		{"Disk full on database", "disk_full"},
		{"CrashLoopBackOff", "crashloop"},
		{"Connection refused to redis", "connectivity"},
		{"Something completely random", "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.title, func(t *testing.T) {
			executor.setResult("PagerDuty", &types.ToolResult{
				Success: true,
				Data: map[string]interface{}{
					"title": tc.title,
				},
			})

			result := tool.inferIncidentType(context.Background(), "test-id")
			if result != tc.expected {
				t.Errorf("For '%s': expected %s, got %s", tc.title, tc.expected, result)
			}
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	testCases := []struct {
		input    string
		expected string // Duration string
	}{
		{"1h", "1h0m0s"},
		{"30m", "30m0s"},
		{"6h", "6h0m0s"},
		{"1d", "24h0m0s"},
		{"", "1h0m0s"},        // Default
		{"invalid", "1h0m0s"}, // Default on error
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := parseTimeRange(tc.input)
			if result.String() != tc.expected {
				t.Errorf("For '%s': expected %s, got %s", tc.input, tc.expected, result.String())
			}
		})
	}
}

// === SelfTest Tests ===

func TestSRETool_SelfTest_OK(t *testing.T) {
	tool, _ := setupTestTool(t)

	result := tool.SelfTest(context.Background(), nil)

	if result.Status != types.SelfTestStatusOK {
		t.Errorf("SelfTest() status = %v, want OK", result.Status)
	}

	if !containsString(result.Message, "full investigation") {
		t.Errorf("Message should indicate full investigation capabilities, got: %s", result.Message)
	}

	if len(result.Capabilities) == 0 {
		t.Error("Should have capabilities")
	}

	// Check expected capabilities
	expectedCaps := []string{"triage_incident", "correlate", "suggest_investigation", "status"}
	for _, expected := range expectedCaps {
		found := false
		for _, cap := range result.Capabilities {
			if cap == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing capability: %s", expected)
		}
	}

	if result.TestDuration == 0 {
		t.Error("TestDuration should be set")
	}
}

func TestSRETool_SelfTest_Degraded_NoK8s(t *testing.T) {
	cfg := &config.Config{
		PagerDuty: config.PagerDutyConfig{
			Enabled:  true,
			APIToken: "test-key",
		},
		Datadog: config.DatadogConfig{
			Enabled: true,
			APIKey:  "test-key",
			AppKey:  "test-app-key",
		},
		// No Kubernetes
	}

	services := &types.ToolServices{ConfigMgr: cfg}
	executor := newMockExecutor()
	tool, err := NewSRETool(services, executor)
	if err != nil {
		t.Fatalf("Failed to create SRE tool: %v", err)
	}

	result := tool.SelfTest(context.Background(), nil)

	if result.Status != types.SelfTestStatusDegraded {
		t.Errorf("SelfTest() status = %v, want Degraded", result.Status)
	}

	if len(result.UnavailableCapabilities) == 0 {
		t.Error("Should have unavailable capabilities")
	}

	// Should have k8s_context in unavailable
	hasK8s := false
	for _, cap := range result.UnavailableCapabilities {
		if cap == "k8s_context" {
			hasK8s = true
			break
		}
	}
	if !hasK8s {
		t.Error("Should have k8s_context as unavailable")
	}
}

func TestSRETool_SelfTest_Dependencies(t *testing.T) {
	tool, _ := setupTestTool(t)

	result := tool.SelfTest(context.Background(), nil)

	if len(result.Dependencies) == 0 {
		t.Error("Should report dependencies")
	}

	// Check for expected dependencies
	foundPD := false
	foundDD := false
	foundK8s := false
	foundExecutor := false

	for _, dep := range result.Dependencies {
		switch dep.Name {
		case "PagerDuty":
			foundPD = true
			if !dep.Available {
				t.Error("PagerDuty should be available")
			}
			if !dep.Required {
				t.Error("PagerDuty should be required")
			}
		case "Datadog":
			foundDD = true
			if !dep.Available {
				t.Error("Datadog should be available")
			}
			if !dep.Required {
				t.Error("Datadog should be required")
			}
		case "Kubernetes":
			foundK8s = true
			if !dep.Available {
				t.Error("Kubernetes should be available in test config")
			}
			if dep.Required {
				t.Error("Kubernetes should not be required")
			}
		case "ToolExecutor":
			foundExecutor = true
			if !dep.Available {
				t.Error("ToolExecutor should be available")
			}
		}
	}

	if !foundPD {
		t.Error("Should have PagerDuty dependency")
	}
	if !foundDD {
		t.Error("Should have Datadog dependency")
	}
	if !foundK8s {
		t.Error("Should have Kubernetes dependency")
	}
	if !foundExecutor {
		t.Error("Should have ToolExecutor dependency")
	}
}

func TestSRETool_SelfTest_WithVerbose(t *testing.T) {
	tool, _ := setupTestTool(t)

	opts := &types.SelfTestOptions{
		Verbose:         true,
		IncludeExamples: false,
	}
	result := tool.SelfTest(context.Background(), opts)

	if result.Details == nil {
		t.Error("Verbose mode should include details")
	}

	// Check for expected detail keys
	if _, ok := result.Details["pagerduty_enabled"]; !ok {
		t.Error("Details should include pagerduty_enabled")
	}
	if _, ok := result.Details["datadog_enabled"]; !ok {
		t.Error("Details should include datadog_enabled")
	}
	if _, ok := result.Details["k8s_clusters"]; !ok {
		t.Error("Details should include k8s_clusters when K8s is enabled")
	}
}

func TestSRETool_SelfTest_WithExamples(t *testing.T) {
	tool, _ := setupTestTool(t)

	opts := &types.SelfTestOptions{
		Verbose:         false,
		IncludeExamples: true,
	}
	result := tool.SelfTest(context.Background(), opts)

	if len(result.Examples) == 0 {
		t.Error("Should include examples when requested")
	}

	// Check examples cover main actions
	actions := make(map[string]bool)
	for _, ex := range result.Examples {
		if action, ok := ex.Args["action"].(string); ok {
			actions[action] = true
		}
	}

	expectedActions := []string{"triage_incident", "correlate", "suggest_investigation"}
	for _, action := range expectedActions {
		if !actions[action] {
			t.Errorf("Missing example for action: %s", action)
		}
	}
}

func TestSRETool_SelfTest_NilExecutor(t *testing.T) {
	cfg := &config.Config{
		PagerDuty: config.PagerDutyConfig{
			Enabled:  true,
			APIToken: "test-key",
		},
		Datadog: config.DatadogConfig{
			Enabled: true,
			APIKey:  "test-key",
			AppKey:  "test-app-key",
		},
	}

	services := &types.ToolServices{ConfigMgr: cfg}
	// Create tool without executor
	tool := &SRETool{
		services:     services,
		pdConfig:     &cfg.PagerDuty,
		ddConfig:     &cfg.Datadog,
		toolExecutor: nil,
	}

	result := tool.SelfTest(context.Background(), nil)

	if result.Status != types.SelfTestStatusFailed {
		t.Errorf("SelfTest() status = %v, want Failed", result.Status)
	}

	// Check executor dependency is marked unavailable
	for _, dep := range result.Dependencies {
		if dep.Name == "ToolExecutor" {
			if dep.Available {
				t.Error("ToolExecutor should be unavailable")
			}
			break
		}
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || containsString(s[1:], substr)))
}
