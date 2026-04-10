//go:build with_pagerduty

package pagerduty

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestTool creates a PagerDutyTool with a mock HTTP server.
func setupTestTool(t *testing.T, handler http.HandlerFunc) (*PagerDutyTool, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)

	cfg := &config.PagerDutyConfig{
		Enabled:                   true,
		APIToken:                  "test-token",
		BaseURL:                   server.URL,
		DefaultServiceID:          "P123ABC",
		DefaultEscalationPolicyID: "PESCALATE",
		RateLimitRPS:              100, // High limit for tests
	}

	tool, err := NewPagerDutyTool(nil, cfg)
	require.NoError(t, err)

	return tool, server
}

func TestNewPagerDutyTool(t *testing.T) {
	cfg := &config.PagerDutyConfig{
		Enabled:  true,
		APIToken: "test-token",
	}
	tool, err := NewPagerDutyTool(nil, cfg)
	require.NoError(t, err)
	assert.NotNil(t, tool)
	assert.NotNil(t, tool.client)
}

func TestNewPagerDutyTool_NilConfig(t *testing.T) {
	_, err := NewPagerDutyTool(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestPagerDutyTool_Name(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()
	assert.Equal(t, "PagerDuty", tool.Name())
}

func TestPagerDutyTool_Execute_MissingAction(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "action parameter is required")
}

func TestPagerDutyTool_Execute_UnknownAction(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "unknown_action",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action")
}

func TestPagerDutyTool_Execute_ListIncidents(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Contains(t, r.URL.Path, "/incidents")
		assert.Equal(t, "Token token=test-token", r.Header.Get("Authorization"))

		resp := map[string]interface{}{
			"incidents": []map[string]interface{}{
				{
					"id":              "P1234567",
					"incident_number": 123,
					"title":           "Server Down",
					"status":          "triggered",
					"urgency":         "high",
					"created_at":      time.Now().Format(time.RFC3339),
					"html_url":        "https://example.pagerduty.com/incidents/P1234567",
					"service": map[string]interface{}{
						"id":      "PABC123",
						"summary": "Web Service",
					},
				},
			},
			"total": 1,
			"more":  false,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_incidents",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 incident")
	assert.NotNil(t, result.Data["incidents"])
}

func TestPagerDutyTool_Execute_ListIncidents_WithFilters(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "triggered", r.URL.Query().Get("statuses[]"))
		assert.Equal(t, "high", r.URL.Query().Get("urgencies[]"))

		resp := map[string]interface{}{
			"incidents": []map[string]interface{}{},
			"total":     0,
			"more":      false,
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "list_incidents",
		"status":  "triggered",
		"urgency": "high",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestPagerDutyTool_Execute_GetIncident(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/incidents/P1234567", r.URL.Path)

		resp := map[string]interface{}{
			"incident": map[string]interface{}{
				"id":                    "P1234567",
				"incident_number":       123,
				"title":                 "Server Down",
				"summary":               "Critical server is not responding",
				"status":                "triggered",
				"urgency":               "high",
				"created_at":            time.Now().Format(time.RFC3339),
				"last_status_change_at": time.Now().Format(time.RFC3339),
				"html_url":              "https://example.pagerduty.com/incidents/P1234567",
				"service": map[string]interface{}{
					"id":      "PABC123",
					"summary": "Web Service",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "get_incident",
		"incident_id": "P1234567",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "P1234567")
	assert.Equal(t, "P1234567", result.Data["id"])
}

func TestPagerDutyTool_Execute_GetIncident_NotFound(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"Not Found"}}`))
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "get_incident",
		"incident_id": "PNOTFOUND",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestPagerDutyTool_Execute_GetIncident_MissingID(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_incident",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "incident_id is required")
}

func TestPagerDutyTool_Execute_Acknowledge(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/incidents/P1234567", r.URL.Path)

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		incident := payload["incident"].(map[string]interface{})
		assert.Equal(t, "acknowledged", incident["status"])

		resp := map[string]interface{}{
			"incident": map[string]interface{}{
				"id":     "P1234567",
				"title":  "Server Down",
				"status": "acknowledged",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "acknowledge",
		"incident_id": "P1234567",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "acknowledged")
}

func TestPagerDutyTool_Execute_Resolve_RequiresConfirmation(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "resolve",
		"incident_id": "P1234567",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "dangerous")
	assert.Contains(t, result.Error, "confirmed=true")
}

func TestPagerDutyTool_Execute_Resolve_WithConfirmation(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		incident := payload["incident"].(map[string]interface{})
		assert.Equal(t, "resolved", incident["status"])

		resp := map[string]interface{}{
			"incident": map[string]interface{}{
				"id":     "P1234567",
				"title":  "Server Down",
				"status": "resolved",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "resolve",
		"incident_id": "P1234567",
		"confirmed":   true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "resolved")
}

func TestPagerDutyTool_Execute_Snooze(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/incidents/P1234567/snooze", r.URL.Path)

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		assert.Equal(t, float64(3600), payload["duration"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":          "snooze",
		"incident_id":     "P1234567",
		"snooze_duration": float64(3600), // 1 hour
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "snoozed")
	assert.Contains(t, result.Content, "3600 seconds")
}

func TestPagerDutyTool_Execute_Snooze_MissingDuration(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "snooze",
		"incident_id": "P1234567",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "snooze_duration")
}

func TestPagerDutyTool_Execute_AddNote(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/incidents/P1234567/notes", r.URL.Path)

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		note := payload["note"].(map[string]interface{})
		assert.Equal(t, "Investigating the issue", note["content"])

		w.WriteHeader(http.StatusCreated)
		resp := map[string]interface{}{
			"note": map[string]interface{}{
				"id":         "PN12345",
				"content":    "Investigating the issue",
				"created_at": time.Now().Format(time.RFC3339),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "add_note",
		"incident_id": "P1234567",
		"note":        "Investigating the issue",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Note added")
	assert.Equal(t, "PN12345", result.Data["note_id"])
}

func TestPagerDutyTool_Execute_AddNote_MissingNote(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "add_note",
		"incident_id": "P1234567",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "note content is required")
}

func TestPagerDutyTool_Execute_Trigger_RequiresConfirmation(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "trigger",
		"title":  "New Incident",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "dangerous")
	assert.Contains(t, result.Error, "confirmed=true")
}

func TestPagerDutyTool_Execute_Trigger_WithConfirmation(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/incidents", r.URL.Path)

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		incident := payload["incident"].(map[string]interface{})
		assert.Equal(t, "New Incident", incident["title"])
		assert.NotNil(t, incident["service"])

		w.WriteHeader(http.StatusCreated)
		resp := map[string]interface{}{
			"incident": map[string]interface{}{
				"id":              "P9999999",
				"incident_number": 999,
				"title":           "New Incident",
				"status":          "triggered",
				"urgency":         "high",
				"html_url":        "https://example.pagerduty.com/incidents/P9999999",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "trigger",
		"title":     "New Incident",
		"confirmed": true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "triggered")
	assert.Contains(t, result.Content, "P9999999")
}

func TestPagerDutyTool_Execute_Trigger_MissingTitle(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "trigger",
		"confirmed": true,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "title is required")
}

func TestPagerDutyTool_Execute_Trigger_MissingServiceID(t *testing.T) {
	cfg := &config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      "http://localhost",
		RateLimitRPS: 100,
		// No DefaultServiceID
	}

	tool, err := NewPagerDutyTool(nil, cfg)
	require.NoError(t, err)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "trigger",
		"title":     "New Incident",
		"confirmed": true,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "service_id is required")
}

func TestClassifyAction(t *testing.T) {
	tests := []struct {
		action   string
		expected SecurityTier
	}{
		{"list_incidents", TierRead},
		{"get_incident", TierRead},
		{"acknowledge", TierModify},
		{"snooze", TierModify},
		{"add_note", TierModify},
		{"resolve", TierDangerous},
		{"trigger", TierDangerous},
		{"unknown", TierDangerous}, // Default to dangerous
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			tier := ClassifyAction(tt.action)
			assert.Equal(t, tt.expected, tier)
		})
	}
}

func TestPagerDutyTool_GetActionDocs(t *testing.T) {
	tool, server := setupTestTool(t, nil)
	defer server.Close()

	docs := tool.GetActionDocs()

	// Verify all actions have documentation
	expectedActions := []string{"list_incidents", "get_incident", "acknowledge", "resolve", "snooze", "add_note", "trigger"}
	for _, action := range expectedActions {
		doc, ok := docs[action]
		assert.True(t, ok, "Missing doc for action: %s", action)
		assert.NotEmpty(t, doc.Description)
		assert.NotEmpty(t, doc.Returns)
	}
}

func TestPagerDutyTool_APIError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"Internal Server Error","code":500}}`))
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_incidents",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "500")
}

// --- SelfTest Tests ---

func TestPagerDutyTool_SelfTest_NotConfigured(t *testing.T) {
	tool := &PagerDutyTool{
		config: nil,
	}

	result := tool.SelfTest(context.Background(), nil)
	assert.Equal(t, types.SelfTestStatusFailed, result.Status)
	assert.Contains(t, result.Message, "not configured")
	assert.Len(t, result.Dependencies, 1)
	assert.Equal(t, "not_configured", result.Dependencies[0].Status)
}

func TestPagerDutyTool_SelfTest_MissingToken(t *testing.T) {
	tool := &PagerDutyTool{
		config: &config.PagerDutyConfig{
			APIToken: "", // Missing
		},
	}

	result := tool.SelfTest(context.Background(), nil)
	assert.Equal(t, types.SelfTestStatusFailed, result.Status)
	assert.Contains(t, result.Message, "not configured")
}

func TestPagerDutyTool_SelfTest_Connected(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abilities" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"abilities": []string{"teams", "schedules"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result := tool.SelfTest(context.Background(), nil)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	assert.Contains(t, result.Message, "connected")
	assert.True(t, len(result.Capabilities) > 0)
	assert.Contains(t, result.Capabilities, "list_incidents")
}

func TestPagerDutyTool_SelfTest_Degraded_InvalidToken(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abilities" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	result := tool.SelfTest(context.Background(), nil)
	assert.Equal(t, types.SelfTestStatusDegraded, result.Status)
	assert.Contains(t, result.Message, "not connected")
	assert.True(t, len(result.UnavailableCapabilities) > 0)
}

func TestPagerDutyTool_SelfTest_WithVerbose(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/abilities" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"abilities": []string{}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}

	tool, server := setupTestTool(t, handler)
	defer server.Close()

	opts := &types.SelfTestOptions{
		Verbose:         true,
		IncludeExamples: true,
	}
	result := tool.SelfTest(context.Background(), opts)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	assert.NotNil(t, result.Details)
	assert.True(t, len(result.Examples) > 0)
}

func TestPagerDutyTool_SelfTest_NoExamplesOnFailure(t *testing.T) {
	tool := &PagerDutyTool{
		config: nil,
	}

	opts := &types.SelfTestOptions{
		IncludeExamples: true,
	}
	result := tool.SelfTest(context.Background(), opts)
	assert.Equal(t, types.SelfTestStatusFailed, result.Status)
	assert.Len(t, result.Examples, 0) // No examples when failed
}
