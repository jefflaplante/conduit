//go:build with_pagerduty

package pagerduty

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/config"
)

func TestOnCallActions_ExecuteGetOnCall_BySchedule(t *testing.T) {
	response := oncallsResponse{
		Oncalls: []OnCall{
			{
				User: OnCallUser{
					ID:    "USER1",
					Name:  "Alice Smith",
					Email: "alice@example.com",
				},
				Schedule: &OnCallSchedule{
					ID:      "SCHED1",
					Summary: "Primary",
				},
				EscalationLevel: 1,
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetOnCall(context.Background(), map[string]interface{}{
		"schedule_id": "SCHED1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data == nil {
		t.Fatal("expected data, got nil")
	}

	oncalls, ok := result.Data["oncalls"].([]interface{})
	if !ok {
		t.Fatalf("expected oncalls array, got %T", result.Data["oncalls"])
	}
	if len(oncalls) != 1 {
		t.Fatalf("expected 1 oncall, got %d", len(oncalls))
	}

	oc := oncalls[0].(map[string]interface{})
	if oc["user_name"] != "Alice Smith" {
		t.Errorf("user_name = %q, want %q", oc["user_name"], "Alice Smith")
	}
	if oc["user_email"] != "alice@example.com" {
		t.Errorf("user_email = %q, want %q", oc["user_email"], "alice@example.com")
	}
}

func TestOnCallActions_ExecuteGetOnCall_ByPolicy(t *testing.T) {
	response := oncallsResponse{
		Oncalls: []OnCall{
			{
				User: OnCallUser{ID: "USER1", Name: "Bob"},
				EscalationPolicy: &OnCallEscPolicy{
					ID:      "POL1",
					Summary: "Backend",
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetOnCall(context.Background(), map[string]interface{}{
		"escalation_policy_id": "POL1",
		"timezone":             "America/New_York",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["timezone"] != "America/New_York" {
		t.Errorf("timezone = %v, want %q", result.Data["timezone"], "America/New_York")
	}
}

func TestOnCallActions_ExecuteGetOnCall_MissingParams(t *testing.T) {
	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      "http://unused",
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetOnCall(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for missing params")
	}
	if result.ErrorDetails == nil {
		t.Error("expected error details")
	}
}

func TestOnCallActions_ExecuteGetOnCall_NoOneOnCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oncallsResponse{Oncalls: []OnCall{}})
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetOnCall(context.Background(), map[string]interface{}{
		"schedule_id": "SCHED1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success even with no oncalls")
	}
	if !contains(result.Content, "No one") {
		t.Errorf("content should mention no one on-call: %s", result.Content)
	}
}

func TestOnCallActions_ExecuteListSchedules(t *testing.T) {
	response := schedulesResponse{
		Schedules: []Schedule{
			{ID: "S1", Name: "Primary", TimeZone: "UTC"},
			{ID: "S2", Name: "Secondary", TimeZone: "America/New_York"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteListSchedules(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	schedules := result.Data["schedules"].([]interface{})
	if len(schedules) != 2 {
		t.Errorf("expected 2 schedules, got %d", len(schedules))
	}
	if result.Data["count"].(int) != 2 {
		t.Errorf("count = %v, want 2", result.Data["count"])
	}
}

func TestOnCallActions_ExecuteListSchedules_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(schedulesResponse{Schedules: []Schedule{}})
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteListSchedules(context.Background(), map[string]interface{}{
		"query": "nonexistent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success")
	}
	if !contains(result.Content, "No schedules found") {
		t.Errorf("content should mention no schedules: %s", result.Content)
	}
}

func TestOnCallActions_ExecuteGetSchedule(t *testing.T) {
	response := scheduleResponse{
		Schedule: Schedule{
			ID:             "SCHED1",
			Name:           "Primary",
			Description:    "Main rotation",
			TimeZone:       "UTC",
			HTMLURL:        "https://pd.example.com/schedules/SCHED1",
			Users:          []UserRef{{ID: "U1", Summary: "Alice"}},
			ScheduleLayers: []ScheduleLayer{{ID: "L1", Name: "Layer 1"}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetSchedule(context.Background(), map[string]interface{}{
		"schedule_id": "SCHED1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["name"] != "Primary" {
		t.Errorf("name = %v, want %q", result.Data["name"], "Primary")
	}
}

func TestOnCallActions_ExecuteGetSchedule_MissingID(t *testing.T) {
	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      "http://unused",
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetSchedule(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for missing schedule_id")
	}
}

func TestOnCallActions_ExecuteListEscalationPolicies(t *testing.T) {
	response := escalationPoliciesResponse{
		EscalationPolicies: []EscalationPolicy{
			{ID: "P1", Name: "Backend", NumLoops: 2},
			{ID: "P2", Name: "Frontend", NumLoops: 3},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteListEscalationPolicies(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	policies := result.Data["escalation_policies"].([]interface{})
	if len(policies) != 2 {
		t.Errorf("expected 2 policies, got %d", len(policies))
	}
}

func TestOnCallActions_ExecuteGetEscalationPolicy(t *testing.T) {
	response := escalationPolicyResponse{
		EscalationPolicy: EscalationPolicy{
			ID:       "POL1",
			Name:     "Backend",
			NumLoops: 3,
			EscalationRules: []EscalationRule{
				{
					EscalationDelayInMinutes: 15,
					Targets: []Target{
						{ID: "SCHED1", Type: "schedule_reference", Summary: "Primary"},
					},
				},
			},
			Services: []ServiceRef{{ID: "SVC1", Summary: "API"}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetEscalationPolicy(context.Background(), map[string]interface{}{
		"escalation_policy_id": "POL1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["name"] != "Backend" {
		t.Errorf("name = %v, want %q", result.Data["name"], "Backend")
	}
	if result.Data["num_loops"].(int) != 3 {
		t.Errorf("num_loops = %v, want 3", result.Data["num_loops"])
	}

	rules := result.Data["escalation_rules"].([]interface{})
	if len(rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(rules))
	}
}

func TestOnCallActions_ExecuteGetEscalationPolicy_MissingID(t *testing.T) {
	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test",
		BaseURL:      "http://unused",
		RateLimitRPS: 100,
	})
	actions := NewOnCallActions(client)

	result, err := actions.ExecuteGetEscalationPolicy(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for missing escalation_policy_id")
	}
}

func TestGetOnCallActionDocs(t *testing.T) {
	docs := GetOnCallActionDocs()

	expectedActions := []string{
		"get_oncall",
		"list_schedules",
		"get_schedule",
		"list_escalation_policies",
		"get_escalation_policy",
	}

	for _, action := range expectedActions {
		doc, ok := docs[action]
		if !ok {
			t.Errorf("missing action doc for %q", action)
			continue
		}
		if doc.Description == "" {
			t.Errorf("action %q has empty description", action)
		}
		if doc.Returns == "" {
			t.Errorf("action %q has empty returns", action)
		}
	}
}
