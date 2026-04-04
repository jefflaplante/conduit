// Package sre implements the SRE Incident Correlation Engine for cross-tool orchestration.
// It correlates data from PagerDuty incidents, Datadog metrics/logs, Kubernetes,
// and SSH to provide unified incident triage and investigation.
package sre

import (
	"context"
	"fmt"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SRETool provides SRE incident correlation and triage via tool orchestration.
type SRETool struct {
	services     *types.ToolServices
	pdConfig     *config.PagerDutyConfig
	ddConfig     *config.DatadogConfig
	k8sConfig    *config.KubernetesConfig
	sshConfig    *config.RemoteSSHConfig
	toolExecutor types.ToolExecutor
}

// NewSRETool creates a new SRE tool with the given services and configurations.
// It requires at least PagerDuty and Datadog to be configured.
func NewSRETool(services *types.ToolServices, executor types.ToolExecutor) (*SRETool, error) {
	if services == nil || services.ConfigMgr == nil {
		return nil, fmt.Errorf("services and config manager are required")
	}

	cfg := services.ConfigMgr

	// Require at least PagerDuty and Datadog for meaningful correlation
	if !cfg.PagerDuty.Enabled || !cfg.Datadog.Enabled {
		return nil, fmt.Errorf("SRE tool requires both PagerDuty and Datadog to be enabled")
	}

	tool := &SRETool{
		services:     services,
		pdConfig:     &cfg.PagerDuty,
		ddConfig:     &cfg.Datadog,
		toolExecutor: executor,
	}

	// Optional: K8s and SSH for deeper investigation
	if cfg.Kubernetes.Enabled {
		tool.k8sConfig = &cfg.Kubernetes
	}
	if cfg.RemoteSSH.Enabled {
		tool.sshConfig = &cfg.RemoteSSH
	}

	return tool, nil
}

// Name returns the tool name.
func (t *SRETool) Name() string { return "Sre" }

// Description returns a human-readable description of the tool's capabilities.
func (t *SRETool) Description() string {
	desc := `SRE Incident Correlation Engine - Cross-tool orchestration for incident triage and investigation.

ACTIONS:
- triage_incident: Pull context from PagerDuty incident, Datadog metrics/logs, and optionally K8s/SSH
  Input: incident_id (PagerDuty incident ID)
  Returns: Unified incident summary with service context, metrics, logs, and K8s status

- correlate: Cross-reference data sources for a service over a time range
  Input: service (service name), time_range (e.g., "1h", "30m")
  Returns: Correlation summary with incidents, monitors, and anomalies

- suggest_investigation: Get recommended next steps based on incident type
  Input: incident_id (PagerDuty incident ID) or incident_type (e.g., "high_cpu", "oom", "5xx_errors")
  Returns: Suggested K8s commands, SSH commands, or Datadog queries

- status: Show SRE tool configuration and available integrations

This tool ORCHESTRATES calls to other tools (PagerDuty, Datadog, Kubernetes, SSH) -
it does not make direct API calls. Use it for unified incident context gathering.

Example workflow:
1. triage_incident to get full context
2. Use suggested investigations to dig deeper
3. Use individual tools for specific actions (ack incident, scale deployment, etc.)`

	return desc
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *SRETool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The SRE operation to perform",
				"enum":        []string{"triage_incident", "correlate", "suggest_investigation", "status"},
			},
			"incident_id": map[string]interface{}{
				"type":        "string",
				"description": "PagerDuty incident ID for triage_incident or suggest_investigation",
			},
			"incident_type": map[string]interface{}{
				"type":        "string",
				"description": "Type of incident for suggest_investigation (e.g., high_cpu, oom, 5xx_errors, latency, disk_full)",
			},
			"service": map[string]interface{}{
				"type":        "string",
				"description": "Service name for correlate action",
			},
			"time_range": map[string]interface{}{
				"type":        "string",
				"description": "Time range for correlation (e.g., '1h', '30m', '6h'). Default: 1h",
			},
			"include_k8s": map[string]interface{}{
				"type":        "boolean",
				"description": "Include Kubernetes context in triage (default: true if K8s is configured)",
			},
			"include_logs": map[string]interface{}{
				"type":        "boolean",
				"description": "Include Datadog logs in triage (default: true)",
			},
			"namespace": map[string]interface{}{
				"type":        "string",
				"description": "Kubernetes namespace to search (optional, auto-detected from service if possible)",
			},
			"cluster": map[string]interface{}{
				"type":        "string",
				"description": "Kubernetes cluster name (optional if only one cluster configured)",
			},
		},
		"required": []string{"action"},
	}
}

// GetActionDocs returns documentation for each action.
func (t *SRETool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"triage_incident": {
			Description:    "Gather unified context from PagerDuty, Datadog, and optionally K8s for an incident",
			RequiredParams: []string{"incident_id"},
			OptionalParams: []string{"include_k8s", "include_logs", "namespace", "cluster"},
			Returns:        "Unified incident summary with service info, metrics, logs, K8s status, and suggested actions",
		},
		"correlate": {
			Description:    "Cross-reference PagerDuty incidents, Datadog monitors, and logs for a service",
			RequiredParams: []string{"service"},
			OptionalParams: []string{"time_range"},
			Returns:        "Correlation summary showing incidents, triggered monitors, and log patterns",
		},
		"suggest_investigation": {
			Description:    "Get recommended investigation steps based on incident type or specific incident",
			OptionalParams: []string{"incident_id", "incident_type"},
			Returns:        "Suggested commands for K8s, SSH, and Datadog queries based on incident type",
		},
		"status": {
			Description: "Show SRE tool configuration and available integrations",
			Returns:     "Configuration status for PagerDuty, Datadog, K8s, and SSH integrations",
		},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *SRETool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")
	if action == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	switch action {
	case "triage_incident":
		return t.triageIncident(ctx, args)
	case "correlate":
		return t.correlate(ctx, args)
	case "suggest_investigation":
		return t.suggestInvestigation(ctx, args)
	case "status":
		return t.status(ctx, args)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)).
			WithParameter("action", action).
			WithAvailableValues([]string{"triage_incident", "correlate", "suggest_investigation", "status"}), nil
	}
}

// status returns the SRE tool configuration status.
func (t *SRETool) status(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	integrations := map[string]interface{}{
		"pagerduty": map[string]interface{}{
			"enabled": true,
		},
		"datadog": map[string]interface{}{
			"enabled": true,
		},
	}

	k8sStatus := map[string]interface{}{"enabled": false}
	if t.k8sConfig != nil {
		k8sStatus["enabled"] = true
		k8sStatus["cluster_count"] = len(t.k8sConfig.Clusters)
		if len(t.k8sConfig.Clusters) > 0 {
			clusterNames := make([]string, len(t.k8sConfig.Clusters))
			for i, c := range t.k8sConfig.Clusters {
				clusterNames[i] = c.Name
			}
			k8sStatus["clusters"] = clusterNames
		}
	}
	integrations["kubernetes"] = k8sStatus

	sshStatus := map[string]interface{}{"enabled": false}
	if t.sshConfig != nil && t.sshConfig.Enabled {
		sshStatus["enabled"] = true
		sshStatus["host_count"] = len(t.sshConfig.Hosts)
	}
	integrations["ssh"] = sshStatus

	content := "SRE Incident Correlation Engine Status\n\n"
	content += "Integrations:\n"
	content += "  - PagerDuty: enabled\n"
	content += "  - Datadog: enabled\n"
	if t.k8sConfig != nil {
		content += fmt.Sprintf("  - Kubernetes: enabled (%d clusters)\n", len(t.k8sConfig.Clusters))
	} else {
		content += "  - Kubernetes: disabled\n"
	}
	if t.sshConfig != nil && t.sshConfig.Enabled {
		content += fmt.Sprintf("  - SSH: enabled (%d hosts)\n", len(t.sshConfig.Hosts))
	} else {
		content += "  - SSH: disabled\n"
	}

	content += "\nAvailable actions: triage_incident, correlate, suggest_investigation"

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"integrations": integrations,
			"actions":      []string{"triage_incident", "correlate", "suggest_investigation", "status"},
		},
	}, nil
}

