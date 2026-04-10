//go:build with_pagerduty

package pagerduty

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Schedule represents a PagerDuty schedule.
type Schedule struct {
	ID                 string          `json:"id"`
	Type               string          `json:"type"`
	Summary            string          `json:"summary"`
	Self               string          `json:"self,omitempty"`
	HTMLURL            string          `json:"html_url,omitempty"`
	Name               string          `json:"name"`
	TimeZone           string          `json:"time_zone,omitempty"`
	Description        string          `json:"description,omitempty"`
	EscalationPolicies []PolicyRef     `json:"escalation_policies,omitempty"`
	Users              []UserRef       `json:"users,omitempty"`
	Teams              []TeamRef       `json:"teams,omitempty"`
	ScheduleLayers     []ScheduleLayer `json:"schedule_layers,omitempty"`
	FinalSchedule      *FinalSchedule  `json:"final_schedule,omitempty"`
}

// TeamRef is a reference to a team.
type TeamRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// ScheduleLayer represents a layer in a schedule.
type ScheduleLayer struct {
	ID                        string    `json:"id,omitempty"`
	Name                      string    `json:"name,omitempty"`
	Start                     time.Time `json:"start,omitempty"`
	End                       time.Time `json:"end,omitempty"`
	RotationVirtualStart      time.Time `json:"rotation_virtual_start,omitempty"`
	RotationTurnLengthSeconds int       `json:"rotation_turn_length_seconds,omitempty"`
	Users                     []UserRef `json:"users,omitempty"`
}

// FinalSchedule contains the rendered schedule with all layers merged.
type FinalSchedule struct {
	Name                       string          `json:"name,omitempty"`
	RenderedScheduleEntries    []RenderedEntry `json:"rendered_schedule_entries,omitempty"`
	RenderedCoveragePercentage float64         `json:"rendered_coverage_percentage,omitempty"`
}

// RenderedEntry is a time period with assigned user in a schedule.
type RenderedEntry struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	User  UserRef   `json:"user"`
}

// schedulesResponse wraps the PagerDuty API response for schedules.
type schedulesResponse struct {
	Schedules []Schedule `json:"schedules"`
	Offset    int        `json:"offset,omitempty"`
	Limit     int        `json:"limit,omitempty"`
	More      bool       `json:"more,omitempty"`
	Total     int        `json:"total,omitempty"`
}

// scheduleResponse wraps the PagerDuty API response for a single schedule.
type scheduleResponse struct {
	Schedule Schedule `json:"schedule"`
}

// ListSchedulesOptions contains parameters for the ListSchedules query.
type ListSchedulesOptions struct {
	Query  string // Filter by name (partial match)
	Limit  int    // Max results per page (max 100)
	Offset int    // Pagination offset
}

// ListSchedules retrieves all schedules.
func (c *Client) ListSchedules(ctx context.Context, opts ListSchedulesOptions) ([]Schedule, error) {
	params := url.Values{}

	if opts.Query != "" {
		params.Set("query", opts.Query)
	}
	if opts.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}

	path := "/schedules"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pagerduty: schedules request failed: %s (%s)", resp.Status, string(body))
	}

	var result schedulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pagerduty: failed to decode schedules response: %w", err)
	}

	return result.Schedules, nil
}

// GetScheduleOptions contains parameters for the GetSchedule query.
type GetScheduleOptions struct {
	TimeZone string    // Time zone for rendered schedule
	Since    time.Time // Start of time range for rendered schedule
	Until    time.Time // End of time range for rendered schedule
}

// GetSchedule retrieves a single schedule by ID.
func (c *Client) GetSchedule(ctx context.Context, scheduleID string, opts GetScheduleOptions) (*Schedule, error) {
	params := url.Values{}

	if opts.TimeZone != "" {
		params.Set("time_zone", opts.TimeZone)
	}
	if !opts.Since.IsZero() {
		params.Set("since", opts.Since.Format(time.RFC3339))
	}
	if !opts.Until.IsZero() {
		params.Set("until", opts.Until.Format(time.RFC3339))
	}

	path := "/schedules/" + scheduleID
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pagerduty: get schedule failed: %s (%s)", resp.Status, string(body))
	}

	var result scheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pagerduty: failed to decode schedule response: %w", err)
	}

	return &result.Schedule, nil
}

// EscalationPolicy represents a PagerDuty escalation policy.
type EscalationPolicy struct {
	ID                         string           `json:"id"`
	Type                       string           `json:"type"`
	Summary                    string           `json:"summary"`
	Self                       string           `json:"self,omitempty"`
	HTMLURL                    string           `json:"html_url,omitempty"`
	Name                       string           `json:"name"`
	Description                string           `json:"description,omitempty"`
	NumLoops                   int              `json:"num_loops,omitempty"`
	OnCallHandoffNotifications string           `json:"on_call_handoff_notifications,omitempty"`
	EscalationRules            []EscalationRule `json:"escalation_rules,omitempty"`
	Services                   []ServiceRef     `json:"services,omitempty"`
	Teams                      []TeamRef        `json:"teams,omitempty"`
}

// EscalationRule defines a rule in an escalation policy.
type EscalationRule struct {
	ID                       string   `json:"id,omitempty"`
	EscalationDelayInMinutes int      `json:"escalation_delay_in_minutes"`
	Targets                  []Target `json:"targets,omitempty"`
}

// Target is a target (user, schedule, or group) in an escalation rule.
type Target struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // user_reference, schedule_reference, etc.
	Summary string `json:"summary,omitempty"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// escalationPoliciesResponse wraps the PagerDuty API response for escalation policies.
type escalationPoliciesResponse struct {
	EscalationPolicies []EscalationPolicy `json:"escalation_policies"`
	Offset             int                `json:"offset,omitempty"`
	Limit              int                `json:"limit,omitempty"`
	More               bool               `json:"more,omitempty"`
	Total              int                `json:"total,omitempty"`
}

// escalationPolicyResponse wraps the PagerDuty API response for a single escalation policy.
type escalationPolicyResponse struct {
	EscalationPolicy EscalationPolicy `json:"escalation_policy"`
}

// ListEscalationPoliciesOptions contains parameters for the ListEscalationPolicies query.
type ListEscalationPoliciesOptions struct {
	Query   string   // Filter by name (partial match)
	UserIDs []string // Filter by user on policy
	TeamIDs []string // Filter by team
	Limit   int      // Max results per page (max 100)
	Offset  int      // Pagination offset
}

// ListEscalationPolicies retrieves all escalation policies.
func (c *Client) ListEscalationPolicies(ctx context.Context, opts ListEscalationPoliciesOptions) ([]EscalationPolicy, error) {
	params := url.Values{}

	if opts.Query != "" {
		params.Set("query", opts.Query)
	}
	for _, id := range opts.UserIDs {
		params.Add("user_ids[]", id)
	}
	for _, id := range opts.TeamIDs {
		params.Add("team_ids[]", id)
	}
	if opts.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}

	path := "/escalation_policies"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pagerduty: escalation_policies request failed: %s (%s)", resp.Status, string(body))
	}

	var result escalationPoliciesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pagerduty: failed to decode escalation_policies response: %w", err)
	}

	return result.EscalationPolicies, nil
}

// GetEscalationPolicyOptions contains parameters for the GetEscalationPolicy query.
type GetEscalationPolicyOptions struct {
	Include []string // Include additional data: services, teams, current_oncall
}

// GetEscalationPolicy retrieves a single escalation policy by ID.
func (c *Client) GetEscalationPolicy(ctx context.Context, policyID string, opts GetEscalationPolicyOptions) (*EscalationPolicy, error) {
	params := url.Values{}

	for _, inc := range opts.Include {
		params.Add("include[]", inc)
	}

	path := "/escalation_policies/" + policyID
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pagerduty: get escalation_policy failed: %s (%s)", resp.Status, string(body))
	}

	var result escalationPolicyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pagerduty: failed to decode escalation_policy response: %w", err)
	}

	return &result.EscalationPolicy, nil
}

// ScheduleSummary provides a user-friendly summary of schedule information.
type ScheduleSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TimeZone    string `json:"time_zone,omitempty"`
	UserCount   int    `json:"user_count"`
	LayerCount  int    `json:"layer_count"`
	PolicyCount int    `json:"policy_count"`
	HTMLURL     string `json:"html_url,omitempty"`
}

// ToSummary converts a Schedule to a user-friendly summary.
func (s *Schedule) ToSummary() ScheduleSummary {
	return ScheduleSummary{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		TimeZone:    s.TimeZone,
		UserCount:   len(s.Users),
		LayerCount:  len(s.ScheduleLayers),
		PolicyCount: len(s.EscalationPolicies),
		HTMLURL:     s.HTMLURL,
	}
}

// SchedulesToSummaries converts a slice of Schedule to summaries.
func SchedulesToSummaries(schedules []Schedule) []ScheduleSummary {
	summaries := make([]ScheduleSummary, len(schedules))
	for i, s := range schedules {
		summaries[i] = s.ToSummary()
	}
	return summaries
}

// EscalationPolicySummary provides a user-friendly summary of policy information.
type EscalationPolicySummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	NumLoops     int    `json:"num_loops"`
	RuleCount    int    `json:"rule_count"`
	ServiceCount int    `json:"service_count"`
	TeamCount    int    `json:"team_count"`
	HTMLURL      string `json:"html_url,omitempty"`
}

// ToSummary converts an EscalationPolicy to a user-friendly summary.
func (p *EscalationPolicy) ToSummary() EscalationPolicySummary {
	return EscalationPolicySummary{
		ID:           p.ID,
		Name:         p.Name,
		Description:  p.Description,
		NumLoops:     p.NumLoops,
		RuleCount:    len(p.EscalationRules),
		ServiceCount: len(p.Services),
		TeamCount:    len(p.Teams),
		HTMLURL:      p.HTMLURL,
	}
}

// EscalationPoliciesToSummaries converts a slice of EscalationPolicy to summaries.
func EscalationPoliciesToSummaries(policies []EscalationPolicy) []EscalationPolicySummary {
	summaries := make([]EscalationPolicySummary, len(policies))
	for i, p := range policies {
		summaries[i] = p.ToSummary()
	}
	return summaries
}
