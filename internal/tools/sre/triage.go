package sre

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/tools/types"
)

// TriageResult contains the complete triage information for an incident.
type TriageResult struct {
	IncidentID      string                 `json:"incident_id"`
	IncidentTitle   string                 `json:"incident_title"`
	IncidentStatus  string                 `json:"incident_status"`
	IncidentUrgency string                 `json:"incident_urgency"`
	Service         *ServiceContext        `json:"service,omitempty"`
	Metrics         *MetricsContext        `json:"metrics,omitempty"`
	Logs            *LogsContext           `json:"logs,omitempty"`
	K8sContext      *K8sContext            `json:"k8s_context,omitempty"`
	Suggestions     []string               `json:"suggestions"`
	Timeline        []TimelineEvent        `json:"timeline"`
	RawData         map[string]interface{} `json:"raw_data,omitempty"`
}

// ServiceContext contains extracted service information.
type ServiceContext struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
}

// MetricsContext contains summarized metrics data.
type MetricsContext struct {
	TimeRange  string                   `json:"time_range"`
	QueryCount int                      `json:"query_count"`
	Anomalies  []string                 `json:"anomalies,omitempty"`
	KeyMetrics []map[string]interface{} `json:"key_metrics,omitempty"`
}

// LogsContext contains summarized log data.
type LogsContext struct {
	TimeRange  string   `json:"time_range"`
	TotalCount int      `json:"total_count"`
	ErrorCount int      `json:"error_count"`
	TopErrors  []string `json:"top_errors,omitempty"`
}

// K8sContext contains Kubernetes status for the service.
type K8sContext struct {
	Cluster     string                   `json:"cluster"`
	Namespace   string                   `json:"namespace"`
	Pods        []map[string]interface{} `json:"pods,omitempty"`
	Events      []map[string]interface{} `json:"events,omitempty"`
	PodCount    int                      `json:"pod_count"`
	HealthyPods int                      `json:"healthy_pods"`
}

// TimelineEvent represents an event in the incident timeline.
type TimelineEvent struct {
	Time   string `json:"time"`
	Source string `json:"source"`
	Event  string `json:"event"`
}

// triageIncident performs comprehensive incident triage.
func (t *SRETool) triageIncident(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := getStringArg(args, "incident_id", "")
	if incidentID == "" {
		return types.NewErrorResult("missing_parameter", "incident_id is required for triage_incident action").
			WithParameter("incident_id", nil).
			WithSuggestions([]string{
				"Use PagerDuty tool with action=list_incidents to find incident IDs",
			}), nil
	}

	includeK8s := getBoolArg(args, "include_k8s", t.k8sConfig != nil)
	includeLogs := getBoolArg(args, "include_logs", true)
	namespace := getStringArg(args, "namespace", "")
	cluster := getStringArg(args, "cluster", "")

	result := &TriageResult{
		IncidentID: incidentID,
		Timeline:   make([]TimelineEvent, 0),
		RawData:    make(map[string]interface{}),
	}

	var content strings.Builder
	content.WriteString(fmt.Sprintf("=== Incident Triage: %s ===\n\n", incidentID))

	// Step 1: Get PagerDuty incident details
	pdResult, err := t.toolExecutor.ExecuteTool(ctx, "PagerDuty", map[string]interface{}{
		"action":      "get_incident",
		"incident_id": incidentID,
	})
	if err != nil || !pdResult.Success {
		errMsg := "failed to get incident"
		if err != nil {
			errMsg = err.Error()
		} else if pdResult.Error != "" {
			errMsg = pdResult.Error
		}
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("PagerDuty: %s", errMsg),
		}, nil
	}

	// Extract incident info
	result.RawData["pagerduty"] = pdResult.Data
	if data := pdResult.Data; data != nil {
		result.IncidentTitle = getString(data, "title")
		result.IncidentStatus = getString(data, "status")
		result.IncidentUrgency = getString(data, "urgency")

		// Extract service info
		if svcData, ok := data["service"].(map[string]interface{}); ok {
			result.Service = &ServiceContext{
				Name: getString(svcData, "name"),
				ID:   getString(svcData, "id"),
			}
		}

		// Add to timeline
		if createdAt := getString(data, "created_at"); createdAt != "" {
			result.Timeline = append(result.Timeline, TimelineEvent{
				Time:   createdAt,
				Source: "PagerDuty",
				Event:  fmt.Sprintf("Incident triggered: %s", result.IncidentTitle),
			})
		}
	}

	content.WriteString("## PagerDuty Incident\n")
	content.WriteString(fmt.Sprintf("Title: %s\n", result.IncidentTitle))
	content.WriteString(fmt.Sprintf("Status: %s\n", result.IncidentStatus))
	content.WriteString(fmt.Sprintf("Urgency: %s\n", result.IncidentUrgency))
	if result.Service != nil {
		content.WriteString(fmt.Sprintf("Service: %s\n", result.Service.Name))
	}
	content.WriteString("\n")

	// Determine service name for queries
	serviceName := ""
	if result.Service != nil {
		serviceName = result.Service.Name
	}

	// Step 2: Query Datadog metrics for the service
	if serviceName != "" {
		content.WriteString("## Datadog Metrics\n")
		metricsCtx, metricsContent := t.gatherMetrics(ctx, serviceName)
		result.Metrics = metricsCtx
		content.WriteString(metricsContent)
		content.WriteString("\n")
	}

	// Step 3: Search Datadog logs for errors
	if includeLogs && serviceName != "" {
		content.WriteString("## Datadog Logs\n")
		logsCtx, logsContent := t.gatherLogs(ctx, serviceName)
		result.Logs = logsCtx
		content.WriteString(logsContent)
		content.WriteString("\n")
	}

	// Step 4: Check Kubernetes if available and requested
	if includeK8s && t.k8sConfig != nil && serviceName != "" {
		content.WriteString("## Kubernetes Status\n")
		k8sCtx, k8sContent := t.gatherK8sContext(ctx, serviceName, namespace, cluster)
		result.K8sContext = k8sCtx
		content.WriteString(k8sContent)
		content.WriteString("\n")
	}

	// Step 5: Generate suggestions based on findings
	result.Suggestions = t.generateSuggestions(result)
	if len(result.Suggestions) > 0 {
		content.WriteString("## Suggested Next Steps\n")
		for i, suggestion := range result.Suggestions {
			content.WriteString(fmt.Sprintf("%d. %s\n", i+1, suggestion))
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"incident_id":      result.IncidentID,
			"incident_title":   result.IncidentTitle,
			"incident_status":  result.IncidentStatus,
			"incident_urgency": result.IncidentUrgency,
			"service":          result.Service,
			"metrics":          result.Metrics,
			"logs":             result.Logs,
			"k8s_context":      result.K8sContext,
			"suggestions":      result.Suggestions,
			"timeline":         result.Timeline,
		},
	}, nil
}

// gatherMetrics queries Datadog for relevant metrics.
func (t *SRETool) gatherMetrics(ctx context.Context, serviceName string) (*MetricsContext, string) {
	metricsCtx := &MetricsContext{
		TimeRange:  "1h",
		Anomalies:  make([]string, 0),
		KeyMetrics: make([]map[string]interface{}, 0),
	}
	var content strings.Builder

	// Query common service metrics
	metricsQueries := []struct {
		name  string
		query string
	}{
		{"CPU Usage", fmt.Sprintf("avg:system.cpu.user{service:%s}", serviceName)},
		{"Memory Usage", fmt.Sprintf("avg:system.mem.used{service:%s}", serviceName)},
		{"Request Rate", fmt.Sprintf("sum:trace.http.request.hits{service:%s}.as_rate()", serviceName)},
		{"Error Rate", fmt.Sprintf("sum:trace.http.request.errors{service:%s}.as_rate()", serviceName)},
		{"Latency p99", fmt.Sprintf("avg:trace.http.request.duration.by.service.99p{service:%s}", serviceName)},
	}

	for _, mq := range metricsQueries {
		result, err := t.toolExecutor.ExecuteTool(ctx, "Datadog", map[string]interface{}{
			"action": "query_metrics",
			"query":  mq.query,
			"from":   "-3600", // Last hour
		})
		if err != nil || !result.Success {
			continue
		}

		metricsCtx.QueryCount++

		// Extract series data
		if result.Data != nil {
			if series, ok := result.Data["series"].([]interface{}); ok && len(series) > 0 {
				metricsCtx.KeyMetrics = append(metricsCtx.KeyMetrics, map[string]interface{}{
					"name":         mq.name,
					"query":        mq.query,
					"series_count": len(series),
				})
				content.WriteString(fmt.Sprintf("- %s: %d data series found\n", mq.name, len(series)))
			}
		}
	}

	if metricsCtx.QueryCount == 0 {
		content.WriteString("No metric data available for this service\n")
	}

	return metricsCtx, content.String()
}

// gatherLogs searches Datadog logs for errors.
func (t *SRETool) gatherLogs(ctx context.Context, serviceName string) (*LogsContext, string) {
	logsCtx := &LogsContext{
		TimeRange: "30m",
		TopErrors: make([]string, 0),
	}
	var content strings.Builder

	// Search for errors in the last 30 minutes
	result, err := t.toolExecutor.ExecuteTool(ctx, "Datadog", map[string]interface{}{
		"action":  "search_logs",
		"service": serviceName,
		"status":  "error",
		"from":    "-30m",
		"limit":   50,
	})
	if err != nil || !result.Success {
		content.WriteString("Unable to query logs\n")
		return logsCtx, content.String()
	}

	if result.Data != nil {
		if count, ok := result.Data["count"].(int); ok {
			logsCtx.TotalCount = count
			logsCtx.ErrorCount = count
		}
		if countFloat, ok := result.Data["count"].(float64); ok {
			logsCtx.TotalCount = int(countFloat)
			logsCtx.ErrorCount = int(countFloat)
		}

		// Extract top error messages
		if logs, ok := result.Data["logs"].([]interface{}); ok {
			seen := make(map[string]bool)
			for _, log := range logs {
				if logMap, ok := log.(map[string]interface{}); ok {
					if msg := getString(logMap, "message"); msg != "" {
						// Truncate and dedupe
						if len(msg) > 100 {
							msg = msg[:100] + "..."
						}
						if !seen[msg] && len(logsCtx.TopErrors) < 5 {
							logsCtx.TopErrors = append(logsCtx.TopErrors, msg)
							seen[msg] = true
						}
					}
				}
			}
		}
	}

	content.WriteString(fmt.Sprintf("Found %d error logs in the last 30 minutes\n", logsCtx.ErrorCount))
	if len(logsCtx.TopErrors) > 0 {
		content.WriteString("Top errors:\n")
		for _, err := range logsCtx.TopErrors {
			content.WriteString(fmt.Sprintf("  - %s\n", err))
		}
	}

	return logsCtx, content.String()
}

// gatherK8sContext retrieves Kubernetes pod and event information.
func (t *SRETool) gatherK8sContext(ctx context.Context, serviceName, namespace, cluster string) (*K8sContext, string) {
	k8sCtx := &K8sContext{
		Cluster:   cluster,
		Namespace: namespace,
		Pods:      make([]map[string]interface{}, 0),
		Events:    make([]map[string]interface{}, 0),
	}
	var content strings.Builder

	// If namespace not provided, try common patterns
	if namespace == "" {
		namespace = serviceName // Often namespace = service name
	}
	k8sCtx.Namespace = namespace

	// Build args for K8s tool
	k8sArgs := map[string]interface{}{
		"action":         "get",
		"resource":       "pods",
		"namespace":      namespace,
		"label_selector": fmt.Sprintf("app=%s", serviceName),
	}
	if cluster != "" {
		k8sArgs["cluster"] = cluster
	}

	// Get pods
	result, err := t.toolExecutor.ExecuteTool(ctx, "Kubernetes", k8sArgs)
	if err != nil || !result.Success {
		content.WriteString(fmt.Sprintf("Unable to get K8s pods (namespace: %s)\n", namespace))

		// Try without label selector
		delete(k8sArgs, "label_selector")
		result, err = t.toolExecutor.ExecuteTool(ctx, "Kubernetes", k8sArgs)
		if err != nil || !result.Success {
			return k8sCtx, content.String()
		}
	}

	// Extract cluster from response if available
	if result.Data != nil {
		if items, ok := result.Data["items"].([]interface{}); ok {
			k8sCtx.PodCount = len(items)
			for _, item := range items {
				if pod, ok := item.(map[string]interface{}); ok {
					k8sCtx.Pods = append(k8sCtx.Pods, pod)
					// Count healthy pods (simplified - phase == Running)
					if phase := getString(pod, "phase"); phase == "Running" {
						k8sCtx.HealthyPods++
					}
				}
			}
		}
	}

	content.WriteString(fmt.Sprintf("Pods: %d total, %d healthy\n", k8sCtx.PodCount, k8sCtx.HealthyPods))

	// Get recent events
	eventsArgs := map[string]interface{}{
		"action":    "events",
		"namespace": namespace,
	}
	if cluster != "" {
		eventsArgs["cluster"] = cluster
	}

	eventsResult, err := t.toolExecutor.ExecuteTool(ctx, "Kubernetes", eventsArgs)
	if err == nil && eventsResult.Success && eventsResult.Data != nil {
		if events, ok := eventsResult.Data["events"].([]interface{}); ok {
			warningCount := 0
			for _, event := range events {
				if eventMap, ok := event.(map[string]interface{}); ok {
					k8sCtx.Events = append(k8sCtx.Events, eventMap)
					if getString(eventMap, "type") == "Warning" {
						warningCount++
					}
				}
			}
			if warningCount > 0 {
				content.WriteString(fmt.Sprintf("Events: %d total, %d warnings\n", len(events), warningCount))
			}
		}
	}

	return k8sCtx, content.String()
}

// generateSuggestions creates actionable suggestions based on triage findings.
func (t *SRETool) generateSuggestions(result *TriageResult) []string {
	suggestions := make([]string, 0)

	// Based on incident status
	if result.IncidentStatus == "triggered" {
		suggestions = append(suggestions, "Acknowledge the incident: PagerDuty action=acknowledge")
	}

	// Based on K8s context
	if result.K8sContext != nil {
		if result.K8sContext.PodCount > 0 && result.K8sContext.HealthyPods < result.K8sContext.PodCount {
			unhealthy := result.K8sContext.PodCount - result.K8sContext.HealthyPods
			suggestions = append(suggestions, fmt.Sprintf("Check unhealthy pods (%d/%d): Kubernetes action=describe resource=pods namespace=%s",
				unhealthy, result.K8sContext.PodCount, result.K8sContext.Namespace))
			suggestions = append(suggestions, fmt.Sprintf("View pod logs: Kubernetes action=logs namespace=%s",
				result.K8sContext.Namespace))
		}
		if len(result.K8sContext.Events) > 0 {
			suggestions = append(suggestions, "Review K8s events for root cause indicators")
		}
	}

	// Based on logs
	if result.Logs != nil && result.Logs.ErrorCount > 10 {
		suggestions = append(suggestions, fmt.Sprintf("High error rate (%d errors in 30m) - review log patterns in Datadog",
			result.Logs.ErrorCount))
	}

	// Based on urgency
	if result.IncidentUrgency == "high" {
		if result.Service != nil {
			suggestions = append(suggestions, fmt.Sprintf("Consider scaling %s if load-related: Kubernetes action=scale",
				result.Service.Name))
		}
	}

	// Always suggest adding a note
	if result.IncidentStatus != "resolved" {
		suggestions = append(suggestions, "Document findings: PagerDuty action=add_note incident_id="+result.IncidentID)
	}

	return suggestions
}

// getString safely extracts a string from a map.
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
