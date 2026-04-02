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

// OnCall represents a user who is currently on-call.
type OnCall struct {
	User             OnCallUser       `json:"user"`
	Schedule         *OnCallSchedule  `json:"schedule,omitempty"`
	EscalationPolicy *OnCallEscPolicy `json:"escalation_policy,omitempty"`
	EscalationLevel  int              `json:"escalation_level,omitempty"`
	Start            time.Time        `json:"start,omitempty"`
	End              time.Time        `json:"end,omitempty"`
}

// OnCallUser contains user information for on-call responses.
type OnCallUser struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
}

// OnCallSchedule contains schedule reference in on-call responses.
type OnCallSchedule struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// OnCallEscPolicy contains escalation policy reference in on-call responses.
type OnCallEscPolicy struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	Self    string `json:"self,omitempty"`
	HTMLURL string `json:"html_url,omitempty"`
}

// oncallsResponse wraps the PagerDuty API response for oncalls.
type oncallsResponse struct {
	Oncalls []OnCall `json:"oncalls"`
	Offset  int      `json:"offset,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	More    bool     `json:"more,omitempty"`
	Total   *int     `json:"total,omitempty"`
}

// GetOnCallsOptions contains parameters for the GetOnCalls query.
type GetOnCallsOptions struct {
	ScheduleIDs          []string  // Filter by schedule ID(s)
	EscalationPolicyIDs  []string  // Filter by escalation policy ID(s)
	UserIDs              []string  // Filter by user ID(s)
	Since                time.Time // Start of time range (optional)
	Until                time.Time // End of time range (optional)
	EscalationLevelStart int       // Minimum escalation level (optional)
	EscalationLevelEnd   int       // Maximum escalation level (optional)
	TimeZone             string    // Time zone for results (e.g., "America/New_York")
	Earliest             bool      // If true, return only the earliest on-call per schedule
	Limit                int       // Max results per page (max 100)
	Offset               int       // Pagination offset
}

// GetOnCalls retrieves a list of users who are currently on-call.
func (c *Client) GetOnCalls(ctx context.Context, opts GetOnCallsOptions) ([]OnCall, error) {
	params := url.Values{}

	for _, id := range opts.ScheduleIDs {
		params.Add("schedule_ids[]", id)
	}
	for _, id := range opts.EscalationPolicyIDs {
		params.Add("escalation_policy_ids[]", id)
	}
	for _, id := range opts.UserIDs {
		params.Add("user_ids[]", id)
	}

	if !opts.Since.IsZero() {
		params.Set("since", opts.Since.Format(time.RFC3339))
	}
	if !opts.Until.IsZero() {
		params.Set("until", opts.Until.Format(time.RFC3339))
	}
	if opts.EscalationLevelStart > 0 {
		params.Set("escalation_level_start", fmt.Sprintf("%d", opts.EscalationLevelStart))
	}
	if opts.EscalationLevelEnd > 0 {
		params.Set("escalation_level_end", fmt.Sprintf("%d", opts.EscalationLevelEnd))
	}
	if opts.TimeZone != "" {
		params.Set("time_zone", opts.TimeZone)
	}
	if opts.Earliest {
		params.Set("earliest", "true")
	}
	if opts.Limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", opts.Limit))
	}
	if opts.Offset > 0 {
		params.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}

	path := "/oncalls"
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
		return nil, fmt.Errorf("pagerduty: oncalls request failed: %s (%s)", resp.Status, string(body))
	}

	var result oncallsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("pagerduty: failed to decode oncalls response: %w", err)
	}

	return result.Oncalls, nil
}

// GetCurrentOnCall returns who is currently on-call for a specific schedule.
func (c *Client) GetCurrentOnCall(ctx context.Context, scheduleID string, timeZone string) ([]OnCall, error) {
	now := time.Now()
	opts := GetOnCallsOptions{
		ScheduleIDs: []string{scheduleID},
		Since:       now,
		Until:       now.Add(time.Second), // Tiny window to get current on-call
		Earliest:    true,
		TimeZone:    timeZone,
	}
	return c.GetOnCalls(ctx, opts)
}

// GetCurrentOnCallForPolicy returns who is currently on-call for an escalation policy.
func (c *Client) GetCurrentOnCallForPolicy(ctx context.Context, policyID string, timeZone string) ([]OnCall, error) {
	now := time.Now()
	opts := GetOnCallsOptions{
		EscalationPolicyIDs: []string{policyID},
		Since:               now,
		Until:               now.Add(time.Second),
		TimeZone:            timeZone,
	}
	return c.GetOnCalls(ctx, opts)
}

// OnCallSummary provides a user-friendly summary of on-call information.
type OnCallSummary struct {
	UserName        string `json:"user_name"`
	UserEmail       string `json:"user_email,omitempty"`
	UserID          string `json:"user_id"`
	ScheduleName    string `json:"schedule_name,omitempty"`
	ScheduleID      string `json:"schedule_id,omitempty"`
	PolicyName      string `json:"policy_name,omitempty"`
	PolicyID        string `json:"policy_id,omitempty"`
	EscalationLevel int    `json:"escalation_level,omitempty"`
	StartTime       string `json:"start_time,omitempty"`
	EndTime         string `json:"end_time,omitempty"`
}

// ToSummary converts an OnCall to a user-friendly summary.
func (o *OnCall) ToSummary() OnCallSummary {
	s := OnCallSummary{
		UserName:        o.User.Name,
		UserEmail:       o.User.Email,
		UserID:          o.User.ID,
		EscalationLevel: o.EscalationLevel,
	}

	// Use Summary as fallback for Name
	if s.UserName == "" {
		s.UserName = o.User.Summary
	}

	if o.Schedule != nil {
		s.ScheduleName = o.Schedule.Summary
		s.ScheduleID = o.Schedule.ID
	}

	if o.EscalationPolicy != nil {
		s.PolicyName = o.EscalationPolicy.Summary
		s.PolicyID = o.EscalationPolicy.ID
	}

	if !o.Start.IsZero() {
		s.StartTime = o.Start.Format(time.RFC3339)
	}
	if !o.End.IsZero() {
		s.EndTime = o.End.Format(time.RFC3339)
	}

	return s
}

// ToSummaries converts a slice of OnCall to summaries.
func ToSummaries(oncalls []OnCall) []OnCallSummary {
	summaries := make([]OnCallSummary, len(oncalls))
	for i, oc := range oncalls {
		summaries[i] = oc.ToSummary()
	}
	return summaries
}
