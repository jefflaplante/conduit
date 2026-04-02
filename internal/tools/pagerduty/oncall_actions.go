package pagerduty

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// OnCallActions provides tool action implementations for on-call operations.
// These are designed to be integrated into a PagerDuty tool via action dispatch.
type OnCallActions struct {
	client *Client
}

// NewOnCallActions creates a new OnCallActions with the given client.
func NewOnCallActions(client *Client) *OnCallActions {
	return &OnCallActions{client: client}
}

// ExecuteGetOnCall handles the get_oncall action.
// Returns who is currently on-call for a schedule or escalation policy.
func (a *OnCallActions) ExecuteGetOnCall(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	scheduleID := getStr(args, "schedule_id")
	policyID := getStr(args, "escalation_policy_id")
	timeZone := getStr(args, "timezone")

	if scheduleID == "" && policyID == "" {
		return types.NewErrorResult("missing_parameter", "either schedule_id or escalation_policy_id is required").
			WithParameter("schedule_id", nil).
			WithParameter("escalation_policy_id", nil).
			WithSuggestions([]string{
				"Use action=list_schedules to find schedule IDs",
				"Use action=list_escalation_policies to find policy IDs",
			}), nil
	}

	if timeZone == "" {
		timeZone = "UTC"
	}

	var oncalls []OnCall
	var err error

	if scheduleID != "" {
		oncalls, err = a.client.GetCurrentOnCall(ctx, scheduleID, timeZone)
	} else {
		oncalls, err = a.client.GetCurrentOnCallForPolicy(ctx, policyID, timeZone)
	}

	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get on-call: %v", err),
		}, nil
	}

	if len(oncalls) == 0 {
		target := scheduleID
		if target == "" {
			target = policyID
		}
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("No one is currently on-call for %s", target),
			Data:    map[string]interface{}{"oncalls": []interface{}{}},
		}, nil
	}

	summaries := ToSummaries(oncalls)
	items := make([]interface{}, len(summaries))
	for i, s := range summaries {
		items[i] = summaryToMap(s)
	}

	// Build user-friendly content
	var content string
	if len(summaries) == 1 {
		s := summaries[0]
		content = fmt.Sprintf("On-call: %s", s.UserName)
		if s.UserEmail != "" {
			content += fmt.Sprintf(" (%s)", s.UserEmail)
		}
		if s.ScheduleName != "" {
			content += fmt.Sprintf(" for %s", s.ScheduleName)
		}
		if s.PolicyName != "" {
			content += fmt.Sprintf(" via %s", s.PolicyName)
		}
	} else {
		content = fmt.Sprintf("%d users currently on-call", len(summaries))
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"oncalls":  items,
			"count":    len(items),
			"timezone": timeZone,
		},
	}, nil
}

// ExecuteListSchedules handles the list_schedules action.
func (a *OnCallActions) ExecuteListSchedules(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query := getStr(args, "query")
	limit := getInt(args, "limit", 25)
	if limit > 100 {
		limit = 100
	}

	schedules, err := a.client.ListSchedules(ctx, ListSchedulesOptions{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list schedules: %v", err),
		}, nil
	}

	if len(schedules) == 0 {
		content := "No schedules found"
		if query != "" {
			content = fmt.Sprintf("No schedules found matching %q", query)
		}
		return &types.ToolResult{
			Success: true,
			Content: content,
			Data:    map[string]interface{}{"schedules": []interface{}{}},
		}, nil
	}

	summaries := SchedulesToSummaries(schedules)
	items := make([]interface{}, len(summaries))
	for i, s := range summaries {
		items[i] = scheduleSummaryToMap(s)
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d schedule(s)", len(schedules)),
		Data: map[string]interface{}{
			"schedules": items,
			"count":     len(items),
		},
	}, nil
}

// ExecuteGetSchedule handles the get_schedule action.
func (a *OnCallActions) ExecuteGetSchedule(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	scheduleID := getStr(args, "schedule_id")
	if scheduleID == "" {
		return types.NewErrorResult("missing_parameter", "schedule_id is required").
			WithParameter("schedule_id", nil).
			WithSuggestions([]string{"Use action=list_schedules to find schedule IDs"}), nil
	}

	timeZone := getStr(args, "timezone")
	if timeZone == "" {
		timeZone = "UTC"
	}

	// Get schedule with next 7 days of rendered entries
	now := time.Now()
	opts := GetScheduleOptions{
		TimeZone: timeZone,
		Since:    now,
		Until:    now.Add(7 * 24 * time.Hour),
	}

	schedule, err := a.client.GetSchedule(ctx, scheduleID, opts)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get schedule: %v", err),
		}, nil
	}

	data := map[string]interface{}{
		"id":          schedule.ID,
		"name":        schedule.Name,
		"description": schedule.Description,
		"time_zone":   schedule.TimeZone,
		"html_url":    schedule.HTMLURL,
	}

	// Add users
	if len(schedule.Users) > 0 {
		users := make([]interface{}, len(schedule.Users))
		for i, u := range schedule.Users {
			users[i] = map[string]interface{}{
				"id":   u.ID,
				"name": u.Summary,
			}
		}
		data["users"] = users
	}

	// Add layers
	if len(schedule.ScheduleLayers) > 0 {
		layers := make([]interface{}, len(schedule.ScheduleLayers))
		for i, l := range schedule.ScheduleLayers {
			layers[i] = map[string]interface{}{
				"id":   l.ID,
				"name": l.Name,
			}
		}
		data["layers"] = layers
	}

	// Add escalation policies using this schedule
	if len(schedule.EscalationPolicies) > 0 {
		policies := make([]interface{}, len(schedule.EscalationPolicies))
		for i, p := range schedule.EscalationPolicies {
			policies[i] = map[string]interface{}{
				"id":   p.ID,
				"name": p.Summary,
			}
		}
		data["escalation_policies"] = policies
	}

	// Add rendered schedule entries (next 7 days)
	if schedule.FinalSchedule != nil && len(schedule.FinalSchedule.RenderedScheduleEntries) > 0 {
		entries := make([]interface{}, len(schedule.FinalSchedule.RenderedScheduleEntries))
		for i, e := range schedule.FinalSchedule.RenderedScheduleEntries {
			entries[i] = map[string]interface{}{
				"start":     e.Start.Format(time.RFC3339),
				"end":       e.End.Format(time.RFC3339),
				"user_id":   e.User.ID,
				"user_name": e.User.Summary,
			}
		}
		data["upcoming_shifts"] = entries
		data["coverage_percentage"] = schedule.FinalSchedule.RenderedCoveragePercentage
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Schedule: %s (%d users, %d layers)", schedule.Name, len(schedule.Users), len(schedule.ScheduleLayers)),
		Data:    data,
	}, nil
}

// ExecuteListEscalationPolicies handles the list_escalation_policies action.
func (a *OnCallActions) ExecuteListEscalationPolicies(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query := getStr(args, "query")
	limit := getInt(args, "limit", 25)
	if limit > 100 {
		limit = 100
	}

	policies, err := a.client.ListEscalationPolicies(ctx, ListEscalationPoliciesOptions{
		Query: query,
		Limit: limit,
	})
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list escalation policies: %v", err),
		}, nil
	}

	if len(policies) == 0 {
		content := "No escalation policies found"
		if query != "" {
			content = fmt.Sprintf("No escalation policies found matching %q", query)
		}
		return &types.ToolResult{
			Success: true,
			Content: content,
			Data:    map[string]interface{}{"escalation_policies": []interface{}{}},
		}, nil
	}

	summaries := EscalationPoliciesToSummaries(policies)
	items := make([]interface{}, len(summaries))
	for i, s := range summaries {
		items[i] = policySummaryToMap(s)
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d escalation policy(ies)", len(policies)),
		Data: map[string]interface{}{
			"escalation_policies": items,
			"count":               len(items),
		},
	}, nil
}

// ExecuteGetEscalationPolicy handles the get_escalation_policy action.
func (a *OnCallActions) ExecuteGetEscalationPolicy(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	policyID := getStr(args, "escalation_policy_id")
	if policyID == "" {
		return types.NewErrorResult("missing_parameter", "escalation_policy_id is required").
			WithParameter("escalation_policy_id", nil).
			WithSuggestions([]string{"Use action=list_escalation_policies to find policy IDs"}), nil
	}

	includeOnCall := getBool(args, "include_oncall")

	opts := GetEscalationPolicyOptions{}
	if includeOnCall {
		opts.Include = []string{"current_oncall"}
	}

	policy, err := a.client.GetEscalationPolicy(ctx, policyID, opts)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get escalation policy: %v", err),
		}, nil
	}

	data := map[string]interface{}{
		"id":          policy.ID,
		"name":        policy.Name,
		"description": policy.Description,
		"num_loops":   policy.NumLoops,
		"html_url":    policy.HTMLURL,
	}

	// Add escalation rules with targets
	if len(policy.EscalationRules) > 0 {
		rules := make([]interface{}, len(policy.EscalationRules))
		for i, r := range policy.EscalationRules {
			targets := make([]interface{}, len(r.Targets))
			for j, t := range r.Targets {
				targets[j] = map[string]interface{}{
					"id":   t.ID,
					"type": t.Type,
					"name": t.Summary,
				}
			}
			rules[i] = map[string]interface{}{
				"level":           i + 1,
				"delay_minutes":   r.EscalationDelayInMinutes,
				"targets":         targets,
			}
		}
		data["escalation_rules"] = rules
	}

	// Add services
	if len(policy.Services) > 0 {
		services := make([]interface{}, len(policy.Services))
		for i, s := range policy.Services {
			services[i] = map[string]interface{}{
				"id":   s.ID,
				"name": s.Summary,
			}
		}
		data["services"] = services
	}

	// Add teams
	if len(policy.Teams) > 0 {
		teams := make([]interface{}, len(policy.Teams))
		for i, t := range policy.Teams {
			teams[i] = map[string]interface{}{
				"id":   t.ID,
				"name": t.Summary,
			}
		}
		data["teams"] = teams
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Escalation Policy: %s (%d rules, loops %d times)", policy.Name, len(policy.EscalationRules), policy.NumLoops),
		Data:    data,
	}, nil
}

// GetOnCallActionDocs returns ActionDoc entries for on-call related actions.
func GetOnCallActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"get_oncall": {
			Description:    "Get who is currently on-call for a schedule or escalation policy",
			OptionalParams: []string{"schedule_id", "escalation_policy_id", "timezone"},
			Returns:        "on-call user(s) with name, email, schedule/policy info, shift times",
		},
		"list_schedules": {
			Description:    "List all on-call schedules",
			OptionalParams: []string{"query", "limit"},
			Returns:        "array of schedules with id, name, time_zone, user_count",
		},
		"get_schedule": {
			Description:    "Get detailed schedule information including upcoming shifts",
			RequiredParams: []string{"schedule_id"},
			OptionalParams: []string{"timezone"},
			Returns:        "schedule details with users, layers, upcoming shifts for 7 days",
		},
		"list_escalation_policies": {
			Description:    "List all escalation policies",
			OptionalParams: []string{"query", "limit"},
			Returns:        "array of policies with id, name, rule_count, service_count",
		},
		"get_escalation_policy": {
			Description:    "Get detailed escalation policy with rules and targets",
			RequiredParams: []string{"escalation_policy_id"},
			OptionalParams: []string{"include_oncall"},
			Returns:        "policy details with escalation rules, targets, services, teams",
		},
	}
}

// Helper functions

func getStr(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return defaultVal
}

func getBool(args map[string]interface{}, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func summaryToMap(s OnCallSummary) map[string]interface{} {
	m := map[string]interface{}{
		"user_name": s.UserName,
		"user_id":   s.UserID,
	}
	if s.UserEmail != "" {
		m["user_email"] = s.UserEmail
	}
	if s.ScheduleName != "" {
		m["schedule_name"] = s.ScheduleName
		m["schedule_id"] = s.ScheduleID
	}
	if s.PolicyName != "" {
		m["policy_name"] = s.PolicyName
		m["policy_id"] = s.PolicyID
	}
	if s.EscalationLevel > 0 {
		m["escalation_level"] = s.EscalationLevel
	}
	if s.StartTime != "" {
		m["start_time"] = s.StartTime
	}
	if s.EndTime != "" {
		m["end_time"] = s.EndTime
	}
	return m
}

func scheduleSummaryToMap(s ScheduleSummary) map[string]interface{} {
	m := map[string]interface{}{
		"id":         s.ID,
		"name":       s.Name,
		"user_count": s.UserCount,
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if s.TimeZone != "" {
		m["time_zone"] = s.TimeZone
	}
	if s.LayerCount > 0 {
		m["layer_count"] = s.LayerCount
	}
	if s.PolicyCount > 0 {
		m["policy_count"] = s.PolicyCount
	}
	if s.HTMLURL != "" {
		m["html_url"] = s.HTMLURL
	}
	return m
}

func policySummaryToMap(s EscalationPolicySummary) map[string]interface{} {
	m := map[string]interface{}{
		"id":         s.ID,
		"name":       s.Name,
		"num_loops":  s.NumLoops,
		"rule_count": s.RuleCount,
	}
	if s.Description != "" {
		m["description"] = s.Description
	}
	if s.ServiceCount > 0 {
		m["service_count"] = s.ServiceCount
	}
	if s.TeamCount > 0 {
		m["team_count"] = s.TeamCount
	}
	if s.HTMLURL != "" {
		m["html_url"] = s.HTMLURL
	}
	return m
}
