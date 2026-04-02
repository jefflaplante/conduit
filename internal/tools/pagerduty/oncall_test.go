package pagerduty

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/config"
)

func TestClient_GetOnCalls(t *testing.T) {
	response := oncallsResponse{
		Oncalls: []OnCall{
			{
				User: OnCallUser{
					ID:      "USER123",
					Type:    "user",
					Summary: "Jane Doe",
					Name:    "Jane Doe",
					Email:   "jane@example.com",
				},
				Schedule: &OnCallSchedule{
					ID:      "SCHED123",
					Type:    "schedule",
					Summary: "Primary On-Call",
				},
				EscalationLevel: 1,
				Start:           time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC),
				End:             time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC),
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oncalls" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	oncalls, err := client.GetOnCalls(context.Background(), GetOnCallsOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(oncalls) != 1 {
		t.Fatalf("expected 1 oncall, got %d", len(oncalls))
	}

	oc := oncalls[0]
	if oc.User.ID != "USER123" {
		t.Errorf("user ID = %q, want %q", oc.User.ID, "USER123")
	}
	if oc.User.Name != "Jane Doe" {
		t.Errorf("user name = %q, want %q", oc.User.Name, "Jane Doe")
	}
	if oc.Schedule.ID != "SCHED123" {
		t.Errorf("schedule ID = %q, want %q", oc.Schedule.ID, "SCHED123")
	}
	if oc.EscalationLevel != 1 {
		t.Errorf("escalation level = %d, want %d", oc.EscalationLevel, 1)
	}
}

func TestClient_GetOnCalls_WithFilters(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oncallsResponse{Oncalls: []OnCall{}})
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	_, err := client.GetOnCalls(context.Background(), GetOnCallsOptions{
		ScheduleIDs:         []string{"SCHED1", "SCHED2"},
		EscalationPolicyIDs: []string{"POL1"},
		TimeZone:            "America/New_York",
		Earliest:            true,
		Limit:               50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check query params
	if gotPath == "/oncalls" {
		t.Error("expected query params but got none")
	}

	// We can't rely on exact order, but verify key params are present
	tests := []struct {
		param string
		want  string
	}{
		{"schedule_ids%5B%5D=SCHED1", "schedule_ids[]=SCHED1"},
		{"schedule_ids%5B%5D=SCHED2", "schedule_ids[]=SCHED2"},
		{"escalation_policy_ids%5B%5D=POL1", "escalation_policy_ids[]=POL1"},
		{"time_zone=America", "time_zone=America/New_York"},
		{"earliest=true", "earliest=true"},
		{"limit=50", "limit=50"},
	}

	for _, tc := range tests {
		found := false
		if contains(gotPath, tc.param) {
			found = true
		}
		if !found {
			t.Errorf("expected %s in path %s", tc.want, gotPath)
		}
	}
}

func TestClient_GetCurrentOnCall(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oncallsResponse{
			Oncalls: []OnCall{
				{
					User: OnCallUser{
						ID:   "USER1",
						Name: "Test User",
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	oncalls, err := client.GetCurrentOnCall(context.Background(), "SCHED123", "UTC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(oncalls) != 1 {
		t.Fatalf("expected 1 oncall, got %d", len(oncalls))
	}

	// Verify schedule filter was applied
	if !contains(gotPath, "schedule_ids") {
		t.Errorf("expected schedule_ids in path %s", gotPath)
	}
	if !contains(gotPath, "SCHED123") {
		t.Errorf("expected SCHED123 in path %s", gotPath)
	}
	if !contains(gotPath, "earliest=true") {
		t.Errorf("expected earliest=true in path %s", gotPath)
	}
}

func TestClient_GetCurrentOnCallForPolicy(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oncallsResponse{Oncalls: []OnCall{}})
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	_, err := client.GetCurrentOnCallForPolicy(context.Background(), "POL456", "America/Los_Angeles")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(gotPath, "escalation_policy_ids") {
		t.Errorf("expected escalation_policy_ids in path %s", gotPath)
	}
	if !contains(gotPath, "POL456") {
		t.Errorf("expected POL456 in path %s", gotPath)
	}
}

func TestClient_GetOnCalls_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Invalid API token"}}`))
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "bad-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	_, err := client.GetOnCalls(context.Background(), GetOnCallsOptions{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
}

func TestOnCall_ToSummary(t *testing.T) {
	start := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC)

	oc := OnCall{
		User: OnCallUser{
			ID:    "USER123",
			Name:  "Jane Doe",
			Email: "jane@example.com",
		},
		Schedule: &OnCallSchedule{
			ID:      "SCHED123",
			Summary: "Primary On-Call",
		},
		EscalationPolicy: &OnCallEscPolicy{
			ID:      "POL456",
			Summary: "Backend Team",
		},
		EscalationLevel: 2,
		Start:           start,
		End:             end,
	}

	summary := oc.ToSummary()

	if summary.UserName != "Jane Doe" {
		t.Errorf("UserName = %q, want %q", summary.UserName, "Jane Doe")
	}
	if summary.UserEmail != "jane@example.com" {
		t.Errorf("UserEmail = %q, want %q", summary.UserEmail, "jane@example.com")
	}
	if summary.UserID != "USER123" {
		t.Errorf("UserID = %q, want %q", summary.UserID, "USER123")
	}
	if summary.ScheduleName != "Primary On-Call" {
		t.Errorf("ScheduleName = %q, want %q", summary.ScheduleName, "Primary On-Call")
	}
	if summary.ScheduleID != "SCHED123" {
		t.Errorf("ScheduleID = %q, want %q", summary.ScheduleID, "SCHED123")
	}
	if summary.PolicyName != "Backend Team" {
		t.Errorf("PolicyName = %q, want %q", summary.PolicyName, "Backend Team")
	}
	if summary.PolicyID != "POL456" {
		t.Errorf("PolicyID = %q, want %q", summary.PolicyID, "POL456")
	}
	if summary.EscalationLevel != 2 {
		t.Errorf("EscalationLevel = %d, want %d", summary.EscalationLevel, 2)
	}
	if summary.StartTime != start.Format(time.RFC3339) {
		t.Errorf("StartTime = %q, want %q", summary.StartTime, start.Format(time.RFC3339))
	}
	if summary.EndTime != end.Format(time.RFC3339) {
		t.Errorf("EndTime = %q, want %q", summary.EndTime, end.Format(time.RFC3339))
	}
}

func TestOnCall_ToSummary_FallbackToSummary(t *testing.T) {
	oc := OnCall{
		User: OnCallUser{
			ID:      "USER123",
			Summary: "Jane D.",
			// Name is empty
		},
	}

	summary := oc.ToSummary()

	// Should fall back to Summary when Name is empty
	if summary.UserName != "Jane D." {
		t.Errorf("UserName = %q, want %q (fallback to Summary)", summary.UserName, "Jane D.")
	}
}

func TestToSummaries(t *testing.T) {
	oncalls := []OnCall{
		{User: OnCallUser{ID: "U1", Name: "User 1"}},
		{User: OnCallUser{ID: "U2", Name: "User 2"}},
	}

	summaries := ToSummaries(oncalls)

	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	if summaries[0].UserID != "U1" {
		t.Errorf("summaries[0].UserID = %q, want %q", summaries[0].UserID, "U1")
	}
	if summaries[1].UserName != "User 2" {
		t.Errorf("summaries[1].UserName = %q, want %q", summaries[1].UserName, "User 2")
	}
}

func TestClient_ListSchedules(t *testing.T) {
	response := schedulesResponse{
		Schedules: []Schedule{
			{
				ID:       "SCHED1",
				Name:     "Primary",
				TimeZone: "America/New_York",
				Users:    []UserRef{{ID: "U1"}, {ID: "U2"}},
			},
			{
				ID:       "SCHED2",
				Name:     "Secondary",
				TimeZone: "UTC",
			},
		},
		Total: 2,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/schedules" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	schedules, err := client.ListSchedules(context.Background(), ListSchedulesOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(schedules) != 2 {
		t.Fatalf("expected 2 schedules, got %d", len(schedules))
	}
	if schedules[0].Name != "Primary" {
		t.Errorf("schedules[0].Name = %q, want %q", schedules[0].Name, "Primary")
	}
	if len(schedules[0].Users) != 2 {
		t.Errorf("schedules[0].Users = %d, want %d", len(schedules[0].Users), 2)
	}
}

func TestClient_GetSchedule(t *testing.T) {
	response := scheduleResponse{
		Schedule: Schedule{
			ID:          "SCHED123",
			Name:        "Primary On-Call",
			TimeZone:    "America/New_York",
			Description: "Main on-call rotation",
			Users:       []UserRef{{ID: "U1", Summary: "Alice"}, {ID: "U2", Summary: "Bob"}},
			ScheduleLayers: []ScheduleLayer{
				{ID: "LAYER1", Name: "Layer 1"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/schedules/SCHED123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	schedule, err := client.GetSchedule(context.Background(), "SCHED123", GetScheduleOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if schedule.ID != "SCHED123" {
		t.Errorf("schedule.ID = %q, want %q", schedule.ID, "SCHED123")
	}
	if schedule.Name != "Primary On-Call" {
		t.Errorf("schedule.Name = %q, want %q", schedule.Name, "Primary On-Call")
	}
	if len(schedule.Users) != 2 {
		t.Errorf("len(Users) = %d, want %d", len(schedule.Users), 2)
	}
	if len(schedule.ScheduleLayers) != 1 {
		t.Errorf("len(ScheduleLayers) = %d, want %d", len(schedule.ScheduleLayers), 1)
	}
}

func TestClient_ListEscalationPolicies(t *testing.T) {
	response := escalationPoliciesResponse{
		EscalationPolicies: []EscalationPolicy{
			{
				ID:       "POL1",
				Name:     "Backend Team",
				NumLoops: 2,
				EscalationRules: []EscalationRule{
					{EscalationDelayInMinutes: 15},
					{EscalationDelayInMinutes: 30},
				},
			},
		},
		Total: 1,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/escalation_policies" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	policies, err := client.ListEscalationPolicies(context.Background(), ListEscalationPoliciesOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(policies) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(policies))
	}
	if policies[0].Name != "Backend Team" {
		t.Errorf("policies[0].Name = %q, want %q", policies[0].Name, "Backend Team")
	}
	if len(policies[0].EscalationRules) != 2 {
		t.Errorf("len(EscalationRules) = %d, want %d", len(policies[0].EscalationRules), 2)
	}
}

func TestClient_GetEscalationPolicy(t *testing.T) {
	response := escalationPolicyResponse{
		EscalationPolicy: EscalationPolicy{
			ID:          "POL123",
			Name:        "Backend Team",
			Description: "Backend team escalation",
			NumLoops:    3,
			EscalationRules: []EscalationRule{
				{
					ID:                       "RULE1",
					EscalationDelayInMinutes: 15,
					Targets: []Target{
						{ID: "SCHED1", Type: "schedule_reference", Summary: "Primary"},
					},
				},
			},
			Services: []ServiceRef{{ID: "SVC1", Summary: "API Service"}},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/escalation_policies/POL123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	client := NewClient(config.PagerDutyConfig{
		Enabled:      true,
		APIToken:     "test-token",
		BaseURL:      srv.URL,
		RateLimitRPS: 100,
	})

	policy, err := client.GetEscalationPolicy(context.Background(), "POL123", GetEscalationPolicyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if policy.ID != "POL123" {
		t.Errorf("policy.ID = %q, want %q", policy.ID, "POL123")
	}
	if policy.NumLoops != 3 {
		t.Errorf("policy.NumLoops = %d, want %d", policy.NumLoops, 3)
	}
	if len(policy.EscalationRules) != 1 {
		t.Fatalf("len(EscalationRules) = %d, want %d", len(policy.EscalationRules), 1)
	}
	if len(policy.EscalationRules[0].Targets) != 1 {
		t.Errorf("len(Targets) = %d, want %d", len(policy.EscalationRules[0].Targets), 1)
	}
}

func TestSchedule_ToSummary(t *testing.T) {
	s := Schedule{
		ID:                 "SCHED123",
		Name:               "Primary",
		Description:        "Main rotation",
		TimeZone:           "UTC",
		HTMLURL:            "https://example.pagerduty.com/schedules/SCHED123",
		Users:              []UserRef{{ID: "U1"}, {ID: "U2"}, {ID: "U3"}},
		ScheduleLayers:     []ScheduleLayer{{ID: "L1"}, {ID: "L2"}},
		EscalationPolicies: []PolicyRef{{ID: "P1"}},
	}

	summary := s.ToSummary()

	if summary.ID != "SCHED123" {
		t.Errorf("ID = %q, want %q", summary.ID, "SCHED123")
	}
	if summary.Name != "Primary" {
		t.Errorf("Name = %q, want %q", summary.Name, "Primary")
	}
	if summary.UserCount != 3 {
		t.Errorf("UserCount = %d, want %d", summary.UserCount, 3)
	}
	if summary.LayerCount != 2 {
		t.Errorf("LayerCount = %d, want %d", summary.LayerCount, 2)
	}
	if summary.PolicyCount != 1 {
		t.Errorf("PolicyCount = %d, want %d", summary.PolicyCount, 1)
	}
}

func TestEscalationPolicy_ToSummary(t *testing.T) {
	p := EscalationPolicy{
		ID:              "POL123",
		Name:            "Backend",
		Description:     "Backend team",
		NumLoops:        2,
		HTMLURL:         "https://example.pagerduty.com/escalation_policies/POL123",
		EscalationRules: []EscalationRule{{ID: "R1"}, {ID: "R2"}, {ID: "R3"}},
		Services:        []ServiceRef{{ID: "S1"}, {ID: "S2"}},
		Teams:           []TeamRef{{ID: "T1"}},
	}

	summary := p.ToSummary()

	if summary.ID != "POL123" {
		t.Errorf("ID = %q, want %q", summary.ID, "POL123")
	}
	if summary.Name != "Backend" {
		t.Errorf("Name = %q, want %q", summary.Name, "Backend")
	}
	if summary.NumLoops != 2 {
		t.Errorf("NumLoops = %d, want %d", summary.NumLoops, 2)
	}
	if summary.RuleCount != 3 {
		t.Errorf("RuleCount = %d, want %d", summary.RuleCount, 3)
	}
	if summary.ServiceCount != 2 {
		t.Errorf("ServiceCount = %d, want %d", summary.ServiceCount, 2)
	}
	if summary.TeamCount != 1 {
		t.Errorf("TeamCount = %d, want %d", summary.TeamCount, 1)
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
