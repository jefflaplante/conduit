package heartbeat

import (
	"fmt"
	"time"

	"conduit/internal/config"
)

// AlertProcessorImpl implements the AlertProcessor interface for processing and delivering alerts
// It coordinates between the SharedAlertQueue and AlertSeverityRouter
type AlertProcessorImpl struct {
	queue  *SharedAlertQueue
	router *AlertSeverityRouter
	config *config.AgentHeartbeatConfig

	// Delivery function - injected for flexibility (testing, different channels)
	deliveryFunc func(alert Alert, target config.AlertTarget) error
}

// NewAlertProcessor creates a new alert processor with the given configuration
func NewAlertProcessor(
	queuePath string,
	config *config.AgentHeartbeatConfig,
	deliveryFunc func(alert Alert, target config.AlertTarget) error,
) *AlertProcessorImpl {
	return &AlertProcessorImpl{
		queue:        NewSharedAlertQueue(queuePath),
		router:       NewAlertSeverityRouter(config),
		config:       config,
		deliveryFunc: deliveryFunc,
	}
}

// ProcessAlert processes a single alert (validation, routing, delivery)
func (p *AlertProcessorImpl) ProcessAlert(alert Alert) error {
	// Validate alert first
	if err := alert.Validate(); err != nil {
		return fmt.Errorf("invalid alert: %w", err)
	}

	// Check if we should process this alert
	shouldProcess, reason := p.ShouldProcessAlert(alert)
	if !shouldProcess {
		if err := p.SuppressAlert(alert, reason); err != nil {
			return fmt.Errorf("failed to suppress alert: %w", err)
		}
		return fmt.Errorf("alert suppressed: %s", reason)
	}

	// Get routing decision
	decision := p.router.ShouldDeliverAlert(alert)
	if !decision.ShouldDeliver {
		if err := p.SuppressAlert(alert, decision.Reason); err != nil {
			return fmt.Errorf("failed to suppress alert: %w", err)
		}
		return fmt.Errorf("alert suppressed: %s", decision.Reason)
	}

	// Get targets for delivery
	targets := p.router.GetDeliveryTargets(alert)
	if len(targets) == 0 {
		reason := "No delivery targets configured for this alert severity"
		if err := p.SuppressAlert(alert, reason); err != nil {
			return fmt.Errorf("failed to suppress alert: %w", err)
		}
		return fmt.Errorf("alert suppressed: %s", reason)
	}

	// Attempt delivery to all targets
	var deliveryErrors []string
	var successfulTargets []string

	for _, target := range targets {
		if err := p.DeliverAlert(alert, target.Name); err != nil {
			deliveryErrors = append(deliveryErrors, fmt.Sprintf("%s: %v", target.Name, err))
			continue
		}
		successfulTargets = append(successfulTargets, target.Name)
	}

	// Update alert based on delivery results
	if len(successfulTargets) > 0 {
		// At least one delivery succeeded
		alert.Status = AlertStatusSent
		alert.DeliveredTo = successfulTargets
		now := time.Now()
		alert.SentAt = &now

		// Update alert in queue
		if err := p.queue.UpdateAlertStatus(alert.ID, AlertStatusSent); err != nil {
			return fmt.Errorf("failed to update alert status after successful delivery: %w", err)
		}

		return nil
	}

	// All deliveries failed
	alert.Status = AlertStatusFailed
	alert.LastError = fmt.Sprintf("All delivery attempts failed: %v", deliveryErrors)
	alert.RetryCount++

	// Update alert in queue
	if err := p.queue.UpdateAlertStatus(alert.ID, AlertStatusFailed); err != nil {
		return fmt.Errorf("failed to update alert status after delivery failure: %w", err)
	}

	return fmt.Errorf("failed to deliver alert to any target: %v", deliveryErrors)
}

// ShouldProcessAlert determines if an alert should be processed based on current conditions
func (p *AlertProcessorImpl) ShouldProcessAlert(alert Alert) (bool, string) {
	// Check if alert has expired
	if alert.IsExpired() {
		return false, "Alert has expired"
	}

	// Check queue-level suppression
	queue, err := p.queue.LoadQueue()
	if err != nil {
		// If we can't load queue, err on the side of processing
		return true, ""
	}

	if queue.IsSuppressed(alert) {
		return false, "Alert is suppressed due to deduplication or rate limiting"
	}

	// Check if this is a retry and enough time has passed
	if alert.Status == AlertStatusFailed {
		shouldRetry, reason := p.router.ShouldRetryAlert(alert)
		return shouldRetry, reason
	}

	return true, ""
}

// GetTargetsForAlert returns the list of targets that should receive this alert
func (p *AlertProcessorImpl) GetTargetsForAlert(alert Alert) []string {
	targets := p.router.GetDeliveryTargets(alert)
	var targetNames []string

	for _, target := range targets {
		targetNames = append(targetNames, target.Name)
	}

	return targetNames
}

// DeliverAlert delivers an alert to a specific target
func (p *AlertProcessorImpl) DeliverAlert(alert Alert, targetName string) error {
	// Find the target configuration
	var targetConfig *config.AlertTarget
	for _, target := range p.config.AlertTargets {
		if target.Name == targetName {
			targetConfig = &target
			break
		}
	}

	if targetConfig == nil {
		return fmt.Errorf("target '%s' not found in configuration", targetName)
	}

	// Use the injected delivery function
	if p.deliveryFunc == nil {
		return fmt.Errorf("no delivery function configured")
	}

	start := time.Now()
	err := p.deliveryFunc(alert, *targetConfig)
	duration := time.Since(start)

	// Log delivery attempt (in a real implementation, this would use proper logging)
	_ = AlertDeliveryResult{
		Alert:   alert,
		Target:  targetName,
		Success: err == nil,
		Error: func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
		Timestamp: start,
		Duration:  duration,
	}

	return err
}

// SuppressAlert marks an alert as suppressed
func (p *AlertProcessorImpl) SuppressAlert(alert Alert, reason string) error {
	// Update alert status
	alert.Status = AlertStatusSuppressed
	alert.LastError = reason

	// Add to queue if not already present
	if err := p.queue.AddAlert(alert); err != nil {
		// If alert already exists, try to update status
		return p.queue.UpdateAlertStatus(alert.ID, AlertStatusSuppressed)
	}

	return nil
}

// ProcessPendingAlerts processes all pending alerts in the queue
func (p *AlertProcessorImpl) ProcessPendingAlerts() error {
	pendingAlerts, err := p.queue.GetPendingAlerts()
	if err != nil {
		return fmt.Errorf("failed to get pending alerts: %w", err)
	}

	if len(pendingAlerts) == 0 {
		return nil // Nothing to process
	}

	var processingErrors []string

	// Process each pending alert
	for _, alert := range pendingAlerts {
		if err := p.ProcessAlert(alert); err != nil {
			processingErrors = append(processingErrors, fmt.Sprintf("Alert %s: %v", alert.ID, err))
			continue
		}
	}

	// Clean up processed alerts
	if err := p.queue.RemoveProcessedAlerts(); err != nil {
		processingErrors = append(processingErrors, fmt.Sprintf("Cleanup failed: %v", err))
	}

	// Return errors if any occurred
	if len(processingErrors) > 0 {
		return fmt.Errorf("processing errors: %v", processingErrors)
	}

	return nil
}

// AddAlert adds a new alert to the processing queue
func (p *AlertProcessorImpl) AddAlert(alert Alert) error {
	// Set default values
	if alert.Status == "" {
		alert.Status = AlertStatusPending
	}

	if alert.CreatedAt.IsZero() {
		alert.CreatedAt = time.Now()
	}

	if alert.MaxRetries == 0 {
		alert.MaxRetries = p.config.AlertRetryPolicy.MaxRetries
	}

	// Validate before adding
	if err := alert.Validate(); err != nil {
		return fmt.Errorf("invalid alert: %w", err)
	}

	return p.queue.AddAlert(alert)
}

// GetQueueStats returns statistics about the alert queue
func (p *AlertProcessorImpl) GetQueueStats() (QueueStats, error) {
	return p.queue.GetQueueStats()
}

// GetRoutingInfo returns information about routing configuration
func (p *AlertProcessorImpl) GetRoutingInfo() RoutingSummary {
	return p.router.GetRoutingSummary()
}

// GetQuietHoursInfo returns information about quiet hours status
func (p *AlertProcessorImpl) GetQuietHoursInfo() QuietHoursInfo {
	return p.router.GetQuietHoursInfo()
}

// IsHealthy checks if the processor and its components are healthy
func (p *AlertProcessorImpl) IsHealthy() error {
	// Check queue health
	if err := p.queue.IsHealthy(); err != nil {
		return fmt.Errorf("queue unhealthy: %w", err)
	}

	// Check router configuration
	if err := p.router.ValidateRoutingConfig(); err != nil {
		return fmt.Errorf("router configuration invalid: %w", err)
	}

	// Check that we have a delivery function
	if p.deliveryFunc == nil {
		return fmt.Errorf("no delivery function configured")
	}

	return nil
}

// ProcessingStats represents statistics about alert processing
type ProcessingStats struct {
	QueueStats       QueueStats     `json:"queue_stats"`
	RoutingInfo      RoutingSummary `json:"routing_info"`
	QuietHoursInfo   QuietHoursInfo `json:"quiet_hours_info"`
	LastProcessedAt  time.Time      `json:"last_processed_at,omitempty"`
	ProcessingErrors []string       `json:"processing_errors,omitempty"`
}

// GetProcessingStats returns comprehensive statistics about alert processing
func (p *AlertProcessorImpl) GetProcessingStats() (ProcessingStats, error) {
	queueStats, err := p.GetQueueStats()
	if err != nil {
		return ProcessingStats{}, fmt.Errorf("failed to get queue stats: %w", err)
	}

	return ProcessingStats{
		QueueStats:     queueStats,
		RoutingInfo:    p.GetRoutingInfo(),
		QuietHoursInfo: p.GetQuietHoursInfo(),
	}, nil
}

// ValidateConfiguration validates the entire processor configuration
func (p *AlertProcessorImpl) ValidateConfiguration() error {
	if p.config == nil {
		return fmt.Errorf("processor configuration cannot be nil")
	}

	if err := p.config.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	if err := p.router.ValidateRoutingConfig(); err != nil {
		return fmt.Errorf("invalid routing configuration: %w", err)
	}

	return nil
}

// CleanupExpiredAlerts removes expired alerts and cleans up suppression data
func (p *AlertProcessorImpl) CleanupExpiredAlerts() error {
	return p.queue.RemoveProcessedAlerts()
}

// DefaultTelegramDeliveryFunc provides a default implementation for Telegram delivery
// This is a placeholder - in the real implementation, this would integrate with the message tool
func DefaultTelegramDeliveryFunc(alert Alert, target config.AlertTarget) error {
	if target.Type != "telegram" {
		return fmt.Errorf("unsupported target type: %s", target.Type)
	}

	// In a real implementation, this would:
	// 1. Format the alert message appropriately
	// 2. Use the message tool to send to Telegram
	// 3. Handle rate limiting and errors

	// For now, return success to avoid blocking
	// Real implementation would be:
	/*
		formattedMessage := formatAlertForTelegram(alert)
		return sendTelegramMessage(target.Config["chat_id"], formattedMessage)
	*/

	return nil
}

// formatAlertForTelegram formats an alert for Telegram delivery
func formatAlertForTelegram(alert Alert) string {
	severityIcon := map[AlertSeverity]string{
		AlertSeverityCritical: "🚨",
		AlertSeverityWarning:  "⚠️",
		AlertSeverityInfo:     "ℹ️",
	}

	icon := severityIcon[alert.Severity]
	if icon == "" {
		icon = "📢"
	}

	message := fmt.Sprintf("%s *%s*\n\n", icon, alert.Title)
	message += fmt.Sprintf("*Source:* %s\n", alert.Source)
	message += fmt.Sprintf("*Severity:* %s\n", alert.Severity)
	message += fmt.Sprintf("*Message:* %s\n", alert.Message)

	if alert.Details != "" {
		message += fmt.Sprintf("\n*Details:* %s\n", alert.Details)
	}

	if alert.Component != "" {
		message += fmt.Sprintf("*Component:* %s\n", alert.Component)
	}

	if len(alert.Tags) > 0 {
		message += fmt.Sprintf("*Tags:* %v\n", alert.Tags)
	}

	message += fmt.Sprintf("\n*Time:* %s", alert.CreatedAt.Format("2006-01-02 15:04:05 MST"))

	return message
}
