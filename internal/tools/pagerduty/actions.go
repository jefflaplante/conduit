package pagerduty

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"conduit/internal/tools/types"
)

// --- API Response Types ---

// Incident represents a PagerDuty incident.
type Incident struct {
	ID               string        `json:"id"`
	Type             string        `json:"type"`
	Summary          string        `json:"summary"`
	Title            string        `json:"title"`
	Status           string        `json:"status"`
	Urgency          string        `json:"urgency"`
	HTMLURL          string        `json:"html_url"`
	IncidentNumber   int           `json:"incident_number"`
	CreatedAt        time.Time     `json:"created_at"`
	LastStatusAt     time.Time     `json:"last_status_change_at"`
	Service          *ServiceRef   `json:"service,omitempty"`
	Assignments      []Assignment  `json:"assignments,omitempty"`
	EscalationPolicy *PolicyRef    `json:"escalation_policy,omitempty"`
	Body             *IncidentBody `json:"body,omitempty"`
}

// ServiceRef is a reference to a PagerDuty service.
type ServiceRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	HTMLURL string `json:"html_url"`
}

// PolicyRef is a reference to an escalation policy.
type PolicyRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	HTMLURL string `json:"html_url"`
}

// Assignment represents an incident assignment.
type Assignment struct {
	At       time.Time   `json:"at"`
	Assignee AssigneeRef `json:"assignee"`
}

// AssigneeRef is a reference to an assignee (user).
type AssigneeRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	HTMLURL string `json:"html_url"`
}

// IncidentBody contains the incident details/body.
type IncidentBody struct {
	Type    string `json:"type"`
	Details string `json:"details"`
}

// Note represents a note on an incident.
type Note struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	User      *UserRef  `json:"user,omitempty"`
}

// UserRef is a reference to a user.
type UserRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
	HTMLURL string `json:"html_url"`
}

// --- List Incidents ---

func (t *PagerDutyTool) listIncidents(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	params := url.Values{}

	if status := getStringArg(args, "status", ""); status != "" {
		params.Add("statuses[]", status)
	}
	if urgency := getStringArg(args, "urgency", ""); urgency != "" {
		params.Add("urgencies[]", urgency)
	}
	if serviceID := getStringArg(args, "service_id", ""); serviceID != "" {
		params.Add("service_ids[]", serviceID)
	}

	limit := getIntArg(args, "limit", 25)
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	path := "/incidents"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list incidents: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(body)),
		}, nil
	}

	var result struct {
		Incidents []Incident `json:"incidents"`
		Total     int        `json:"total"`
		More      bool       `json:"more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, nil
	}

	items := make([]interface{}, len(result.Incidents))
	for i, inc := range result.Incidents {
		item := map[string]interface{}{
			"id":              inc.ID,
			"incident_number": inc.IncidentNumber,
			"title":           inc.Title,
			"status":          inc.Status,
			"urgency":         inc.Urgency,
			"created_at":      inc.CreatedAt.Format(time.RFC3339),
			"html_url":        inc.HTMLURL,
		}
		if inc.Service != nil {
			item["service"] = map[string]interface{}{
				"id":   inc.Service.ID,
				"name": inc.Service.Summary,
			}
		}
		items[i] = item
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d incident(s)", len(result.Incidents)),
		Data: map[string]interface{}{
			"incidents": items,
			"total":     result.Total,
			"more":      result.More,
		},
	}, nil
}

// --- Get Incident ---

func (t *PagerDutyTool) getIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "incident_id is required for get_incident action",
		}, nil
	}

	path := fmt.Sprintf("/incidents/%s", incidentID)
	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get incident: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("incident %s not found", incidentID),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(body)),
		}, nil
	}

	var result struct {
		Incident Incident `json:"incident"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, nil
	}

	inc := result.Incident
	data := map[string]interface{}{
		"id":              inc.ID,
		"incident_number": inc.IncidentNumber,
		"title":           inc.Title,
		"summary":         inc.Summary,
		"status":          inc.Status,
		"urgency":         inc.Urgency,
		"created_at":      inc.CreatedAt.Format(time.RFC3339),
		"last_status_at":  inc.LastStatusAt.Format(time.RFC3339),
		"html_url":        inc.HTMLURL,
	}
	if inc.Service != nil {
		data["service"] = map[string]interface{}{
			"id":   inc.Service.ID,
			"name": inc.Service.Summary,
		}
	}
	if inc.EscalationPolicy != nil {
		data["escalation_policy"] = map[string]interface{}{
			"id":   inc.EscalationPolicy.ID,
			"name": inc.EscalationPolicy.Summary,
		}
	}
	if len(inc.Assignments) > 0 {
		assignees := make([]interface{}, len(inc.Assignments))
		for i, a := range inc.Assignments {
			assignees[i] = map[string]interface{}{
				"id":   a.Assignee.ID,
				"name": a.Assignee.Summary,
				"at":   a.At.Format(time.RFC3339),
			}
		}
		data["assignments"] = assignees
	}
	if inc.Body != nil && inc.Body.Details != "" {
		data["details"] = inc.Body.Details
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Incident %s: %s (%s)", inc.ID, inc.Title, inc.Status),
		Data:    data,
	}, nil
}

// --- Acknowledge Incident ---

func (t *PagerDutyTool) acknowledgeIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "incident_id is required for acknowledge action",
		}, nil
	}

	return t.updateIncidentStatus(ctx, incidentID, "acknowledged")
}

// --- Resolve Incident ---

func (t *PagerDutyTool) resolveIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "incident_id is required for resolve action",
		}, nil
	}

	return t.updateIncidentStatus(ctx, incidentID, "resolved")
}

// updateIncidentStatus is a helper to update incident status.
func (t *PagerDutyTool) updateIncidentStatus(ctx context.Context, incidentID, status string) (*types.ToolResult, error) {
	payload := map[string]interface{}{
		"incident": map[string]interface{}{
			"type":   "incident_reference",
			"status": status,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal request: %v", err),
		}, nil
	}

	path := fmt.Sprintf("/incidents/%s", incidentID)
	resp, err := t.client.Do(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to update incident: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("incident %s not found", incidentID),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	var result struct {
		Incident Incident `json:"incident"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Incident %s updated to %s", incidentID, status),
		Data: map[string]interface{}{
			"id":     result.Incident.ID,
			"status": result.Incident.Status,
			"title":  result.Incident.Title,
		},
	}, nil
}

// --- Snooze Incident ---

func (t *PagerDutyTool) snoozeIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "incident_id is required for snooze action",
		}, nil
	}

	duration := getIntArg(args, "snooze_duration", 0)
	if duration <= 0 {
		return &types.ToolResult{
			Success: false,
			Error:   "snooze_duration (in seconds) is required and must be positive",
		}, nil
	}

	payload := map[string]interface{}{
		"duration": duration,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal request: %v", err),
		}, nil
	}

	path := fmt.Sprintf("/incidents/%s/snooze", incidentID)
	resp, err := t.client.Do(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to snooze incident: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("incident %s not found", incidentID),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	wakeTime := time.Now().Add(time.Duration(duration) * time.Second)

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Incident %s snoozed for %d seconds (until %s)", incidentID, duration, wakeTime.Format(time.RFC3339)),
		Data: map[string]interface{}{
			"id":              incidentID,
			"snooze_duration": duration,
			"wake_at":         wakeTime.Format(time.RFC3339),
		},
	}, nil
}

// --- Add Note ---

func (t *PagerDutyTool) addNote(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "incident_id is required for add_note action",
		}, nil
	}

	noteContent := getStringArg(args, "note", "")
	if noteContent == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "note content is required for add_note action",
		}, nil
	}

	payload := map[string]interface{}{
		"note": map[string]interface{}{
			"content": noteContent,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal request: %v", err),
		}, nil
	}

	path := fmt.Sprintf("/incidents/%s/notes", incidentID)
	resp, err := t.client.Do(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to add note: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("incident %s not found", incidentID),
		}, nil
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	var result struct {
		Note Note `json:"note"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Note added to incident %s", incidentID),
		Data: map[string]interface{}{
			"incident_id": incidentID,
			"note_id":     result.Note.ID,
			"content":     result.Note.Content,
			"created_at":  result.Note.CreatedAt.Format(time.RFC3339),
		},
	}, nil
}

// --- Trigger Incident ---

func (t *PagerDutyTool) triggerIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	title := getStringArg(args, "title", "")
	if title == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "title is required for trigger action",
		}, nil
	}

	serviceID := getStringArg(args, "service_id", "")
	if serviceID == "" {
		serviceID = t.config.DefaultServiceID
	}
	if serviceID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "service_id is required (not specified and no default configured)",
		}, nil
	}

	escalationPolicyID := getStringArg(args, "escalation_policy_id", "")
	if escalationPolicyID == "" {
		escalationPolicyID = t.config.DefaultEscalationPolicyID
	}

	incident := map[string]interface{}{
		"type":  "incident",
		"title": title,
		"service": map[string]interface{}{
			"id":   serviceID,
			"type": "service_reference",
		},
	}

	if details := getStringArg(args, "details", ""); details != "" {
		incident["body"] = map[string]interface{}{
			"type":    "incident_body",
			"details": details,
		}
	}

	if escalationPolicyID != "" {
		incident["escalation_policy"] = map[string]interface{}{
			"id":   escalationPolicyID,
			"type": "escalation_policy_reference",
		}
	}

	payload := map[string]interface{}{
		"incident": incident,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal request: %v", err),
		}, nil
	}

	resp, err := t.client.Do(ctx, http.MethodPost, "/incidents", bytes.NewReader(body))
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to trigger incident: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty API error (status %d): %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	var result struct {
		Incident Incident `json:"incident"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to decode response: %v", err),
		}, nil
	}

	inc := result.Incident
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Incident triggered: %s (ID: %s)", inc.Title, inc.ID),
		Data: map[string]interface{}{
			"id":              inc.ID,
			"incident_number": inc.IncidentNumber,
			"title":           inc.Title,
			"status":          inc.Status,
			"urgency":         inc.Urgency,
			"html_url":        inc.HTMLURL,
			"service_id":      serviceID,
		},
	}, nil
}
