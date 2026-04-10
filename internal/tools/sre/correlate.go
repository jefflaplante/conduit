//go:build with_sre

package sre

import (
	"context"
	"fmt"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// CorrelationResult contains cross-referenced data from multiple sources.
type CorrelationResult struct {
	Service      string                   `json:"service"`
	TimeRange    string                   `json:"time_range"`
	Incidents    []map[string]interface{} `json:"incidents"`
	Monitors     []map[string]interface{} `json:"monitors"`
	LogPatterns  []string                 `json:"log_patterns"`
	MetricSpikes []string                 `json:"metric_spikes"`
	Correlations []string                 `json:"correlations"`
	Summary      string                   `json:"summary"`
}

// InvestigationSuggestion contains a recommended investigation action.
type InvestigationSuggestion struct {
	Category    string                 `json:"category"` // "k8s", "ssh", "datadog"
	Action      string                 `json:"action"`
	Description string                 `json:"description"`
	Tool        string                 `json:"tool"`
	Args        map[string]interface{} `json:"args"`
	Command     string                 `json:"command,omitempty"` // For display
}

// correlate performs cross-source correlation for a service.
func (t *SRETool) correlate(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	service := toolargs.GetString(args, "service", "")
	if service == "" {
		return types.NewErrorResult("missing_parameter", "service is required for correlate action").
			WithParameter("service", nil).
			WithSuggestions([]string{
				"Provide the service name to correlate data for",
			}), nil
	}

	timeRange := toolargs.GetString(args, "time_range", "1h")

	result := &CorrelationResult{
		Service:      service,
		TimeRange:    timeRange,
		Incidents:    make([]map[string]interface{}, 0),
		Monitors:     make([]map[string]interface{}, 0),
		LogPatterns:  make([]string, 0),
		MetricSpikes: make([]string, 0),
		Correlations: make([]string, 0),
	}

	var content strings.Builder
	content.WriteString(fmt.Sprintf("=== Correlation Analysis: %s (last %s) ===\n\n", service, timeRange))

	// Step 1: Get PagerDuty incidents for this service
	content.WriteString("## PagerDuty Incidents\n")
	incidents := t.getIncidentsForService(ctx, service)
	result.Incidents = incidents
	if len(incidents) > 0 {
		content.WriteString(fmt.Sprintf("Found %d incidents:\n", len(incidents)))
		for _, inc := range incidents {
			content.WriteString(fmt.Sprintf("  - [%s] %s (%s)\n",
				getString(inc, "status"),
				getString(inc, "title"),
				getString(inc, "created_at")))
		}
	} else {
		content.WriteString("No recent incidents\n")
	}
	content.WriteString("\n")

	// Step 2: Get Datadog monitors/alerts
	content.WriteString("## Datadog Monitors\n")
	monitors := t.getMonitorsForService(ctx, service)
	result.Monitors = monitors
	if len(monitors) > 0 {
		content.WriteString(fmt.Sprintf("Found %d triggered monitors:\n", len(monitors)))
		for _, mon := range monitors {
			content.WriteString(fmt.Sprintf("  - [%s] %s\n",
				getString(mon, "status"),
				getString(mon, "name")))
		}
	} else {
		content.WriteString("No triggered monitors\n")
	}
	content.WriteString("\n")

	// Step 3: Search logs for error patterns
	content.WriteString("## Log Patterns\n")
	patterns := t.getLogPatterns(ctx, service, timeRange)
	result.LogPatterns = patterns
	if len(patterns) > 0 {
		content.WriteString("Error patterns:\n")
		for _, pattern := range patterns {
			content.WriteString(fmt.Sprintf("  - %s\n", pattern))
		}
	} else {
		content.WriteString("No significant error patterns\n")
	}
	content.WriteString("\n")

	// Step 4: Identify correlations
	result.Correlations = t.identifyCorrelations(result)
	if len(result.Correlations) > 0 {
		content.WriteString("## Identified Correlations\n")
		for _, corr := range result.Correlations {
			content.WriteString(fmt.Sprintf("- %s\n", corr))
		}
		content.WriteString("\n")
	}

	// Generate summary
	result.Summary = t.generateCorrelationSummary(result)
	content.WriteString("## Summary\n")
	content.WriteString(result.Summary)

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"service":      result.Service,
			"time_range":   result.TimeRange,
			"incidents":    result.Incidents,
			"monitors":     result.Monitors,
			"log_patterns": result.LogPatterns,
			"correlations": result.Correlations,
			"summary":      result.Summary,
		},
	}, nil
}

// getIncidentsForService fetches recent incidents related to a service.
func (t *SRETool) getIncidentsForService(ctx context.Context, service string) []map[string]interface{} {
	incidents := make([]map[string]interface{}, 0)

	// Query PagerDuty for incidents
	result, err := t.toolExecutor.ExecuteTool(ctx, "PagerDuty", map[string]interface{}{
		"action": "list_incidents",
		"limit":  25,
	})
	if err != nil || !result.Success {
		return incidents
	}

	if result.Data == nil {
		return incidents
	}

	// Filter by service (case-insensitive partial match)
	serviceLower := strings.ToLower(service)
	if incidentList, ok := result.Data["incidents"].([]interface{}); ok {
		for _, inc := range incidentList {
			if incMap, ok := inc.(map[string]interface{}); ok {
				// Check if service matches
				if svcData, ok := incMap["service"].(map[string]interface{}); ok {
					svcName := strings.ToLower(getString(svcData, "name"))
					if strings.Contains(svcName, serviceLower) || strings.Contains(serviceLower, svcName) {
						incidents = append(incidents, incMap)
					}
				}
				// Also check title for service name
				title := strings.ToLower(getString(incMap, "title"))
				if strings.Contains(title, serviceLower) {
					incidents = append(incidents, incMap)
				}
			}
		}
	}

	return incidents
}

// getMonitorsForService fetches Datadog monitors for a service.
func (t *SRETool) getMonitorsForService(ctx context.Context, service string) []map[string]interface{} {
	monitors := make([]map[string]interface{}, 0)

	// Query Datadog monitors (using DatadogMonitor tool)
	result, err := t.toolExecutor.ExecuteTool(ctx, "DatadogMonitor", map[string]interface{}{
		"action": "list",
	})
	if err != nil || !result.Success {
		return monitors
	}

	if result.Data == nil {
		return monitors
	}

	// Filter by service tag or name
	serviceLower := strings.ToLower(service)
	if monitorList, ok := result.Data["monitors"].([]interface{}); ok {
		for _, mon := range monitorList {
			if monMap, ok := mon.(map[string]interface{}); ok {
				// Check name
				name := strings.ToLower(getString(monMap, "name"))
				if strings.Contains(name, serviceLower) {
					monitors = append(monitors, monMap)
					continue
				}
				// Check tags
				if tags, ok := monMap["tags"].([]interface{}); ok {
					for _, tag := range tags {
						if tagStr, ok := tag.(string); ok {
							if strings.Contains(strings.ToLower(tagStr), serviceLower) {
								monitors = append(monitors, monMap)
								break
							}
						}
					}
				}
			}
		}
	}

	return monitors
}

// getLogPatterns identifies common error patterns in logs.
func (t *SRETool) getLogPatterns(ctx context.Context, service, timeRange string) []string {
	patterns := make([]string, 0)

	// Convert time range to from parameter
	fromParam := "-" + timeRange

	result, err := t.toolExecutor.ExecuteTool(ctx, "Datadog", map[string]interface{}{
		"action":  "search_logs",
		"service": service,
		"status":  "error",
		"from":    fromParam,
		"limit":   100,
	})
	if err != nil || !result.Success {
		return patterns
	}

	if result.Data == nil {
		return patterns
	}

	// Extract and group error messages
	errorCounts := make(map[string]int)
	if logs, ok := result.Data["logs"].([]interface{}); ok {
		for _, log := range logs {
			if logMap, ok := log.(map[string]interface{}); ok {
				msg := getString(logMap, "message")
				if msg == "" {
					continue
				}
				// Normalize the message (take first 80 chars, remove specific IDs)
				normalized := normalizeLogMessage(msg)
				errorCounts[normalized]++
			}
		}
	}

	// Return top patterns (count > 1)
	for pattern, count := range errorCounts {
		if count > 1 {
			patterns = append(patterns, fmt.Sprintf("%s (x%d)", pattern, count))
		}
	}

	// Limit to top 5
	if len(patterns) > 5 {
		patterns = patterns[:5]
	}

	return patterns
}

// normalizeLogMessage normalizes a log message for pattern matching.
func normalizeLogMessage(msg string) string {
	// Truncate
	if len(msg) > 80 {
		msg = msg[:80] + "..."
	}
	// Remove common variable parts (UUIDs, timestamps, numbers)
	// This is a simplified version - real implementation would use regex
	return msg
}

// identifyCorrelations looks for relationships between data sources.
func (t *SRETool) identifyCorrelations(result *CorrelationResult) []string {
	correlations := make([]string, 0)

	// Correlation: incidents triggered around same time as monitors
	if len(result.Incidents) > 0 && len(result.Monitors) > 0 {
		correlations = append(correlations,
			fmt.Sprintf("%d incidents and %d triggered monitors detected - likely related",
				len(result.Incidents), len(result.Monitors)))
	}

	// Correlation: error logs match incident descriptions
	if len(result.Incidents) > 0 && len(result.LogPatterns) > 0 {
		correlations = append(correlations,
			"Error patterns in logs may correlate with incident triggers")
	}

	// Correlation: multiple incidents suggest cascading failure
	if len(result.Incidents) > 2 {
		correlations = append(correlations,
			"Multiple incidents detected - possible cascading failure or widespread issue")
	}

	return correlations
}

// generateCorrelationSummary creates a human-readable summary.
func (t *SRETool) generateCorrelationSummary(result *CorrelationResult) string {
	var summary strings.Builder

	totalIssues := len(result.Incidents) + len(result.Monitors)
	if totalIssues == 0 {
		summary.WriteString(fmt.Sprintf("No significant issues found for %s in the last %s.\n",
			result.Service, result.TimeRange))
	} else {
		summary.WriteString(fmt.Sprintf("Service %s has %d active issues:\n",
			result.Service, totalIssues))
		if len(result.Incidents) > 0 {
			summary.WriteString(fmt.Sprintf("- %d PagerDuty incidents\n", len(result.Incidents)))
		}
		if len(result.Monitors) > 0 {
			summary.WriteString(fmt.Sprintf("- %d triggered Datadog monitors\n", len(result.Monitors)))
		}
		if len(result.LogPatterns) > 0 {
			summary.WriteString(fmt.Sprintf("- %d recurring error patterns in logs\n", len(result.LogPatterns)))
		}
	}

	return summary.String()
}

// suggestInvestigation provides recommended next steps based on incident type.
func (t *SRETool) suggestInvestigation(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	incidentID := toolargs.GetString(args, "incident_id", "")
	incidentType := toolargs.GetString(args, "incident_type", "")

	if incidentID == "" && incidentType == "" {
		return types.NewErrorResult("missing_parameter", "Either incident_id or incident_type is required").
			WithParameter("incident_id", nil).
			WithParameter("incident_type", nil).
			WithExamples([]string{
				"incident_type: high_cpu, oom, 5xx_errors, latency, disk_full",
			}), nil
	}

	// If incident_id provided, determine type from incident
	if incidentID != "" && incidentType == "" {
		incidentType = t.inferIncidentType(ctx, incidentID)
	}

	suggestions := t.getSuggestionsForType(incidentType)

	var content strings.Builder
	content.WriteString(fmt.Sprintf("=== Investigation Suggestions for: %s ===\n\n", incidentType))

	categories := map[string][]InvestigationSuggestion{
		"Kubernetes": {},
		"Datadog":    {},
		"SSH":        {},
	}

	for _, s := range suggestions {
		switch s.Category {
		case "k8s":
			categories["Kubernetes"] = append(categories["Kubernetes"], s)
		case "datadog":
			categories["Datadog"] = append(categories["Datadog"], s)
		case "ssh":
			categories["SSH"] = append(categories["SSH"], s)
		}
	}

	for cat, items := range categories {
		if len(items) > 0 {
			content.WriteString(fmt.Sprintf("## %s\n", cat))
			for _, s := range items {
				content.WriteString(fmt.Sprintf("- %s\n", s.Description))
				if s.Command != "" {
					content.WriteString(fmt.Sprintf("  Command: %s\n", s.Command))
				}
			}
			content.WriteString("\n")
		}
	}

	// Format suggestions for data output
	suggestionData := make([]map[string]interface{}, len(suggestions))
	for i, s := range suggestions {
		suggestionData[i] = map[string]interface{}{
			"category":    s.Category,
			"action":      s.Action,
			"description": s.Description,
			"tool":        s.Tool,
			"args":        s.Args,
			"command":     s.Command,
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"incident_type": incidentType,
			"suggestions":   suggestionData,
		},
	}, nil
}

// inferIncidentType tries to determine the incident type from PagerDuty details.
func (t *SRETool) inferIncidentType(ctx context.Context, incidentID string) string {
	result, err := t.toolExecutor.ExecuteTool(ctx, "PagerDuty", map[string]interface{}{
		"action":      "get_incident",
		"incident_id": incidentID,
	})
	if err != nil || !result.Success || result.Data == nil {
		return "unknown"
	}

	title := strings.ToLower(getString(result.Data, "title"))
	details := strings.ToLower(getString(result.Data, "details"))
	combined := title + " " + details

	// Infer type from keywords
	switch {
	case strings.Contains(combined, "cpu"):
		return "high_cpu"
	case strings.Contains(combined, "memory") || strings.Contains(combined, "oom") || strings.Contains(combined, "out of memory"):
		return "oom"
	case strings.Contains(combined, "5xx") || strings.Contains(combined, "500") || strings.Contains(combined, "502") || strings.Contains(combined, "503"):
		return "5xx_errors"
	case strings.Contains(combined, "latency") || strings.Contains(combined, "slow") || strings.Contains(combined, "timeout"):
		return "latency"
	case strings.Contains(combined, "disk") || strings.Contains(combined, "storage"):
		return "disk_full"
	case strings.Contains(combined, "crash") || strings.Contains(combined, "restart"):
		return "crashloop"
	case strings.Contains(combined, "connection") || strings.Contains(combined, "refused"):
		return "connectivity"
	default:
		return "unknown"
	}
}

// getSuggestionsForType returns investigation suggestions for an incident type.
func (t *SRETool) getSuggestionsForType(incidentType string) []InvestigationSuggestion {
	suggestions := make([]InvestigationSuggestion, 0)

	switch incidentType {
	case "high_cpu":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "top_pods",
			Description: "Check CPU usage across pods",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "top", "resource": "pods", "sort_by": "cpu"},
			Command:     "Kubernetes action=top resource=pods sort_by=cpu",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "query_metrics",
			Description: "Query CPU metrics with breakdown",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "query_metrics", "query": "avg:system.cpu.user{*} by {host}"},
			Command:     "Datadog action=query_metrics query='avg:system.cpu.user{*} by {host}'",
		})
		if t.sshConfig != nil && t.sshConfig.Enabled {
			suggestions = append(suggestions, InvestigationSuggestion{
				Category:    "ssh",
				Action:      "exec",
				Description: "Check top processes on affected hosts",
				Tool:        "Ssh",
				Args:        map[string]interface{}{"action": "exec", "command": "top -b -n 1 | head -20"},
				Command:     "Ssh action=exec command='top -b -n 1 | head -20'",
			})
		}

	case "oom":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "top_pods",
			Description: "Check memory usage across pods",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "top", "resource": "pods", "sort_by": "memory"},
			Command:     "Kubernetes action=top resource=pods sort_by=memory",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "events",
			Description: "Check for OOMKilled events",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "events"},
			Command:     "Kubernetes action=events",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_logs",
			Description: "Search for OOM-related logs",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_logs", "query": "OOM OR 'out of memory' OR killed"},
			Command:     "Datadog action=search_logs query='OOM OR out of memory OR killed'",
		})

	case "5xx_errors":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_logs",
			Description: "Search for 5xx error logs",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_logs", "status": "error", "query": "5xx OR 500 OR 502 OR 503"},
			Command:     "Datadog action=search_logs status=error query='5xx OR 500 OR 502 OR 503'",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_traces",
			Description: "Search for error traces",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_traces", "status": "error"},
			Command:     "Datadog action=search_traces status=error",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "logs",
			Description: "Check pod logs for errors",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "logs", "tail_lines": 200},
			Command:     "Kubernetes action=logs tail_lines=200",
		})

	case "latency":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_traces",
			Description: "Search for slow traces (>1s)",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_traces", "min_duration": "1s"},
			Command:     "Datadog action=search_traces min_duration=1s",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "query_metrics",
			Description: "Query p99 latency metrics",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "query_metrics", "query": "avg:trace.http.request.duration.by.service.99p{*}"},
			Command:     "Datadog action=query_metrics query='avg:trace.http.request.duration.by.service.99p{*}'",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "top_pods",
			Description: "Check resource usage for bottlenecks",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "top", "resource": "pods"},
			Command:     "Kubernetes action=top resource=pods",
		})

	case "disk_full":
		if t.sshConfig != nil && t.sshConfig.Enabled {
			suggestions = append(suggestions, InvestigationSuggestion{
				Category:    "ssh",
				Action:      "exec",
				Description: "Check disk usage on hosts",
				Tool:        "Ssh",
				Args:        map[string]interface{}{"action": "exec", "command": "df -h"},
				Command:     "Ssh action=exec command='df -h'",
			})
			suggestions = append(suggestions, InvestigationSuggestion{
				Category:    "ssh",
				Action:      "exec",
				Description: "Find large files",
				Tool:        "Ssh",
				Args:        map[string]interface{}{"action": "exec", "command": "du -h --max-depth=2 / 2>/dev/null | sort -hr | head -20"},
				Command:     "Ssh action=exec command='du -h --max-depth=2 / 2>/dev/null | sort -hr | head -20'",
			})
		}
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "query_metrics",
			Description: "Query disk usage metrics",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "query_metrics", "query": "avg:system.disk.in_use{*} by {host,device}"},
			Command:     "Datadog action=query_metrics query='avg:system.disk.in_use{*} by {host,device}'",
		})

	case "crashloop":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "describe",
			Description: "Describe pods to see crash reason",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "describe", "resource": "pods"},
			Command:     "Kubernetes action=describe resource=pods",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "logs",
			Description: "Get logs from crashed containers (previous)",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "logs", "tail_lines": 500},
			Command:     "Kubernetes action=logs tail_lines=500",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "events",
			Description: "Check events for restart reasons",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "events"},
			Command:     "Kubernetes action=events",
		})

	case "connectivity":
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "get",
			Description: "Check service and endpoints",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "get", "resource": "endpoints"},
			Command:     "Kubernetes action=get resource=endpoints",
		})
		if t.sshConfig != nil && t.sshConfig.Enabled {
			suggestions = append(suggestions, InvestigationSuggestion{
				Category:    "ssh",
				Action:      "exec",
				Description: "Test connectivity from hosts",
				Tool:        "Ssh",
				Args:        map[string]interface{}{"action": "exec", "command": "netstat -an | grep ESTABLISHED | head -20"},
				Command:     "Ssh action=exec command='netstat -an | grep ESTABLISHED | head -20'",
			})
		}
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_logs",
			Description: "Search for connection errors",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_logs", "query": "connection refused OR timeout OR ECONNREFUSED"},
			Command:     "Datadog action=search_logs query='connection refused OR timeout OR ECONNREFUSED'",
		})

	default:
		// Generic suggestions for unknown types
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "get",
			Description: "Check pod status",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "get", "resource": "pods"},
			Command:     "Kubernetes action=get resource=pods",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "datadog",
			Action:      "search_logs",
			Description: "Search recent error logs",
			Tool:        "Datadog",
			Args:        map[string]interface{}{"action": "search_logs", "status": "error", "from": "-30m"},
			Command:     "Datadog action=search_logs status=error from=-30m",
		})
		suggestions = append(suggestions, InvestigationSuggestion{
			Category:    "k8s",
			Action:      "events",
			Description: "Check recent K8s events",
			Tool:        "Kubernetes",
			Args:        map[string]interface{}{"action": "events"},
			Command:     "Kubernetes action=events",
		})
	}

	return suggestions
}

// parseTimeRange converts a time range string to duration.
func parseTimeRange(tr string) time.Duration {
	// Default to 1 hour
	if tr == "" {
		return time.Hour
	}

	// Simple parsing for common formats
	var value int
	var unit byte
	_, err := fmt.Sscanf(tr, "%d%c", &value, &unit)
	if err != nil {
		return time.Hour
	}

	switch unit {
	case 'm':
		return time.Duration(value) * time.Minute
	case 'h':
		return time.Duration(value) * time.Hour
	case 'd':
		return time.Duration(value) * 24 * time.Hour
	default:
		return time.Hour
	}
}
