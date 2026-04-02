package datadog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ = toolargs.GetString // silence unused import during transition

func TestNewMonitorTool(t *testing.T) {
	cfg := &config.DatadogConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		AppKey:  "test-app-key",
		Site:    "datadoghq.com",
	}

	tool, err := NewMonitorTool(nil, cfg)
	require.NoError(t, err)
	assert.NotNil(t, tool)
	assert.Equal(t, "Datadog", tool.Name())
}

func TestNewMonitorTool_NilConfig(t *testing.T) {
	_, err := NewMonitorTool(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestNewMonitorTool_Disabled(t *testing.T) {
	cfg := &config.DatadogConfig{
		Enabled: false,
		APIKey:  "test-api-key",
		AppKey:  "test-app-key",
	}

	_, err := NewMonitorTool(nil, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

func TestNewMonitorTool_MissingAPIKey(t *testing.T) {
	cfg := &config.DatadogConfig{
		Enabled: true,
		AppKey:  "test-app-key",
	}

	_, err := NewMonitorTool(nil, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key")
}

func TestMonitorTool_Execute_MissingAction(t *testing.T) {
	tool := createTestTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "action parameter is required")
}

func TestMonitorTool_Execute_UnknownAction(t *testing.T) {
	tool := createTestTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "unknown_action",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action")
}

func TestMonitorTool_ListMonitors(t *testing.T) {
	monitors := []Monitor{
		{ID: 1, Name: "Test Monitor 1", Type: "metric alert", OverallState: "OK", Tags: []string{"env:prod"}},
		{ID: 2, Name: "Test Monitor 2", Type: "query alert", OverallState: "Alert", Tags: []string{"env:staging"}},
		{ID: 3, Name: "Test Monitor 3", Type: "service check", OverallState: "Warn", Tags: []string{"env:prod"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/monitor")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(monitors)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_monitors",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "3 monitor(s)")
	assert.Contains(t, result.Content, "1 ALERTING")
	assert.Contains(t, result.Content, "1 warning")

	// Verify data structure
	data := result.Data
	require.NotNil(t, data)
	resultMonitors, ok := data["monitors"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, resultMonitors, 3)

	// Verify sorting (Alert first)
	assert.Equal(t, "Alert", resultMonitors[0]["status"])
	assert.Equal(t, "Warn", resultMonitors[1]["status"])
	assert.Equal(t, "OK", resultMonitors[2]["status"])

	// Verify summary
	summary := data["summary"].(map[string]interface{})
	assert.Equal(t, 3, summary["total"])
	assert.Equal(t, 1, summary["alerting"])
	assert.Equal(t, 1, summary["warning"])
	assert.Equal(t, 1, summary["ok"])
}

func TestMonitorTool_ListMonitors_WithFilters(t *testing.T) {
	monitors := []Monitor{
		{ID: 1, Name: "Prod Monitor", OverallState: "Alert"},
	}

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(monitors)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_monitors",
		"name":   "Prod",
		"tags":   []interface{}{"env:prod", "team:platform"},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify query parameters were passed
	assert.Contains(t, gotQuery, "name=Prod")
	assert.Contains(t, gotQuery, "tags=env%3Aprod%2Cteam%3Aplatform")
}

func TestMonitorTool_ListMonitors_StatusFilter(t *testing.T) {
	monitors := []Monitor{
		{ID: 1, Name: "Monitor 1", OverallState: "OK"},
		{ID: 2, Name: "Monitor 2", OverallState: "Alert"},
		{ID: 3, Name: "Monitor 3", OverallState: "Alert"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(monitors)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_monitors",
		"status": "Alert",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Should only have alerting monitors
	data := result.Data
	resultMonitors, ok := data["monitors"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, resultMonitors, 2)
	assert.Equal(t, "Alert", resultMonitors[0]["status"])
	assert.Equal(t, "Alert", resultMonitors[1]["status"])
}

func TestMonitorTool_GetMonitor(t *testing.T) {
	monitor := Monitor{
		ID:           123,
		Name:         "Test Monitor",
		Type:         "metric alert",
		Query:        "avg(last_5m):avg:system.cpu.user{*} > 80",
		Message:      "CPU is high!",
		OverallState: "OK",
		Tags:         []string{"env:prod", "service:api"},
		Options: &MonitorOptions{
			Thresholds: map[string]interface{}{
				"critical": 80.0,
				"warning":  70.0,
			},
			NotifyNoData: true,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/monitor/123")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(monitor)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "get_monitor",
		"monitor_id": 123,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Monitor 123")
	assert.Contains(t, result.Content, "Test Monitor")

	// Verify data
	data := result.Data
	assert.Equal(t, int64(123), data["id"])
	assert.Equal(t, "Test Monitor", data["name"])
	assert.Equal(t, "avg(last_5m):avg:system.cpu.user{*} > 80", data["query"])
	assert.NotNil(t, data["thresholds"])
}

func TestMonitorTool_GetMonitor_MissingID(t *testing.T) {
	tool := createTestTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_monitor",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "monitor_id parameter is required")
}

func TestMonitorTool_GetMonitor_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "get_monitor",
		"monitor_id": 999,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestMonitorTool_GetMonitorStatus(t *testing.T) {
	lastTriggered := int64(1712000000000) // millis
	monitor := Monitor{
		ID:           123,
		Name:         "Test Monitor",
		Type:         "metric alert",
		OverallState: "Alert",
		State: &MonitorState_{
			Groups: map[string]MonitorGroupState{
				"*": {
					Status:          "Alert",
					LastTriggeredTS: &lastTriggered,
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "group_states=all")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(monitor)
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "get_monitor_status",
		"monitor_id": 123,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Alert")
	assert.Contains(t, result.Content, "last triggered")

	// Verify data
	data := result.Data
	assert.Equal(t, "Alert", data["overall_state"])
	groups, ok := data["groups"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, groups, 1)
}

func TestMonitorTool_MuteMonitor_RequiresConfirmation(t *testing.T) {
	tool := createTestTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "mute_monitor",
		"monitor_id": 123,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires confirmation")
	assert.True(t, result.Data["requires_confirmation"].(bool))
}

func TestMonitorTool_MuteMonitor_WithConfirmation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/monitor/123/mute")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 123})
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "mute_monitor",
		"monitor_id": 123,
		"confirmed":  true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "muted")
	assert.True(t, result.Data["muted"].(bool))
}

func TestMonitorTool_MuteMonitor_WithScope(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "mute_monitor",
		"monitor_id": 123,
		"scope":      "host:myhost",
		"end":        int64(1712345678),
		"confirmed":  true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Verify body was sent correctly
	assert.Equal(t, "host:myhost", gotBody["scope"])
	assert.Equal(t, float64(1712345678), gotBody["end"])
}

func TestMonitorTool_UnmuteMonitor_RequiresConfirmation(t *testing.T) {
	tool := createTestTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "unmute_monitor",
		"monitor_id": 123,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires confirmation")
}

func TestMonitorTool_UnmuteMonitor_WithConfirmation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/api/v1/monitor/123/unmute")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "unmute_monitor",
		"monitor_id": 123,
		"confirmed":  true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "unmuted")
	assert.False(t, result.Data["muted"].(bool))
}

func TestMonitorTool_UnmuteMonitor_WithScope(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))
	defer srv.Close()

	tool := createTestTool(t, srv)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "unmute_monitor",
		"monitor_id": 123,
		"scope":      "host:myhost",
		"confirmed":  true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, gotPath, "scope=host%3Amyhost")
}

func TestClassifyAction(t *testing.T) {
	tests := []struct {
		action   string
		expected SecurityTier
	}{
		{"list_monitors", TierRead},
		{"get_monitor", TierRead},
		{"get_monitor_status", TierRead},
		{"mute_monitor", TierModify},
		{"unmute_monitor", TierModify},
		{"unknown", TierModify}, // Default to modify for safety
	}

	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			assert.Equal(t, tc.expected, classifyAction(tc.action))
		})
	}
}

func TestRequiresConfirmation(t *testing.T) {
	assert.False(t, requiresConfirmation("list_monitors"))
	assert.False(t, requiresConfirmation("get_monitor"))
	assert.False(t, requiresConfirmation("get_monitor_status"))
	assert.True(t, requiresConfirmation("mute_monitor"))
	assert.True(t, requiresConfirmation("unmute_monitor"))
}

func TestNormalizeState(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ok", "OK"},
		{"OK", "OK"},
		{"alert", "Alert"},
		{"Alert", "Alert"},
		{"ALERT", "Alert"},
		{"warn", "Warn"},
		{"Warn", "Warn"},
		{"no data", "No Data"},
		{"No Data", "No Data"},
		{"", "Unknown"},
		{"other", "other"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.expected, normalizeState(tc.input))
		})
	}
}

func TestStatePriority(t *testing.T) {
	// Alert should have highest priority (lowest number)
	assert.Less(t, statePriority("Alert"), statePriority("Warn"))
	assert.Less(t, statePriority("Warn"), statePriority("No Data"))
	assert.Less(t, statePriority("No Data"), statePriority("OK"))
}

func TestMonitorTool_Parameters(t *testing.T) {
	tool := createTestTool(t, nil)

	params := tool.Parameters()
	require.NotNil(t, params)

	props := params["properties"].(map[string]interface{})
	require.NotNil(t, props)

	// Check required parameters
	assert.Contains(t, params["required"], "action")

	// Check action enum
	actionProp := props["action"].(map[string]interface{})
	actionEnum := actionProp["enum"].([]string)
	assert.Contains(t, actionEnum, "list_monitors")
	assert.Contains(t, actionEnum, "get_monitor")
	assert.Contains(t, actionEnum, "get_monitor_status")
	assert.Contains(t, actionEnum, "mute_monitor")
	assert.Contains(t, actionEnum, "unmute_monitor")
}

func TestMonitorTool_GetActionDocs(t *testing.T) {
	tool := createTestTool(t, nil)

	docs := tool.GetActionDocs()
	require.NotNil(t, docs)

	// Check that all actions are documented
	assert.Contains(t, docs, "list_monitors")
	assert.Contains(t, docs, "get_monitor")
	assert.Contains(t, docs, "get_monitor_status")
	assert.Contains(t, docs, "mute_monitor")
	assert.Contains(t, docs, "unmute_monitor")

	// Check mute_monitor requires confirmation
	muteDoc := docs["mute_monitor"]
	assert.Contains(t, muteDoc.RequiredParams, "monitor_id")
	assert.Contains(t, muteDoc.RequiredParams, "confirmed")
}

func TestHelperFunctions(t *testing.T) {
	args := map[string]interface{}{
		"string_val": "test",
		"int_val":    42,
		"float_val":  123.45,
		"int64_val":  int64(999),
		"bool_val":   true,
	}

	assert.Equal(t, "test", toolargs.GetString(args, "string_val", "default"))
	assert.Equal(t, "default", toolargs.GetString(args, "missing", "default"))

	assert.Equal(t, 42, toolargs.GetInt(args, "int_val", 0))
	assert.Equal(t, 123, toolargs.GetInt(args, "float_val", 0))
	assert.Equal(t, 999, toolargs.GetInt(args, "int64_val", 0))
	assert.Equal(t, 0, toolargs.GetInt(args, "missing", 0))

	assert.Equal(t, int64(123), toolargs.GetInt64(args, "float_val", 0))
	assert.Equal(t, int64(999), toolargs.GetInt64(args, "int64_val", 0))

	assert.True(t, toolargs.GetBool(args, "bool_val", false))
	assert.False(t, toolargs.GetBool(args, "missing", false))
}

// ---------- Test helpers ----------

func createTestTool(t *testing.T, srv *httptest.Server) *MonitorTool {
	t.Helper()

	cfg := &config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		Site:         "datadoghq.com",
		RateLimitRPS: 100,
	}

	tool, err := NewMonitorTool(nil, cfg)
	require.NoError(t, err)

	if srv != nil {
		// Override baseURL to point at the test server
		tool.client.baseURL = srv.URL + "/"
	}

	return tool
}
