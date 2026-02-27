package heartbeat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AlertSeverity represents the severity level of an alert
type AlertSeverity string

const (
	AlertSeverityCritical AlertSeverity = "critical"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityInfo     AlertSeverity = "info"
)

// String returns the string representation of AlertSeverity
func (s AlertSeverity) String() string {
	return string(s)
}

// IsValid checks if the AlertSeverity is valid
func (s AlertSeverity) IsValid() bool {
	switch s {
	case AlertSeverityCritical, AlertSeverityWarning, AlertSeverityInfo:
		return true
	default:
		return false
	}
}

// Priority returns the numerical priority of the severity (higher = more urgent)
func (s AlertSeverity) Priority() int {
	switch s {
	case AlertSeverityCritical:
		return 10
	case AlertSeverityWarning:
		return 5
	case AlertSeverityInfo:
		return 1
	default:
		return 0
	}
}

// ShouldRespectQuietHours returns true if this severity should respect quiet hours
func (s AlertSeverity) ShouldRespectQuietHours() bool {
	// Critical alerts ignore quiet hours
	return s != AlertSeverityCritical
}

// MarshalJSON implements json.Marshaler
func (s AlertSeverity) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// UnmarshalJSON implements json.Unmarshaler
func (s *AlertSeverity) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}

	severity := AlertSeverity(str)
	if !severity.IsValid() {
		return fmt.Errorf("invalid alert severity: %s", str)
	}

	*s = severity
	return nil
}

// AlertStatus represents the processing status of an alert
type AlertStatus string

const (
	AlertStatusPending    AlertStatus = "pending"
	AlertStatusSent       AlertStatus = "sent"
	AlertStatusFailed     AlertStatus = "failed"
	AlertStatusSuppressed AlertStatus = "suppressed" // Suppressed due to quiet hours or rate limiting
)

// String returns the string representation of AlertStatus
func (s AlertStatus) String() string {
	return string(s)
}

// IsValid checks if the AlertStatus is valid
func (s AlertStatus) IsValid() bool {
	switch s {
	case AlertStatusPending, AlertStatusSent, AlertStatusFailed, AlertStatusSuppressed:
		return true
	default:
		return false
	}
}

// MarshalJSON implements json.Marshaler
func (s AlertStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// UnmarshalJSON implements json.Unmarshaler
func (s *AlertStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}

	status := AlertStatus(str)
	if !status.IsValid() {
		return fmt.Errorf("invalid alert status: %s", str)
	}

	*s = status
	return nil
}

// Alert represents a single alert to be processed and delivered
type Alert struct {
	// Identification
	ID        string `json:"id"`
	Source    string `json:"source"`    // Component that generated the alert
	Component string `json:"component"` // System component related to the alert
	Type      string `json:"type"`      // Alert type (e.g., "disk_space", "memory_usage")

	// Content
	Title   string `json:"title"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`

	// Classification
	Severity AlertSeverity `json:"severity"`
	Category string        `json:"category,omitempty"` // "system", "application", "network", etc.
	Tags     []string      `json:"tags,omitempty"`

	// Timing
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Processing
	Status     AlertStatus `json:"status"`
	RetryCount int         `json:"retry_count"`
	MaxRetries int         `json:"max_retries"`
	LastError  string      `json:"last_error,omitempty"`

	// Delivery tracking
	SentAt      *time.Time `json:"sent_at,omitempty"`
	Targets     []string   `json:"targets,omitempty"`      // Target names that should receive this alert
	DeliveredTo []string   `json:"delivered_to,omitempty"` // Targets that successfully received the alert

	// Rate limiting and deduplication
	DeduplicationKey string    `json:"deduplication_key,omitempty"` // Key for grouping similar alerts
	SuppressionKey   string    `json:"suppression_key,omitempty"`   // Key for suppressing repeated alerts
	LastSeenAt       time.Time `json:"last_seen_at,omitempty"`      // Last time this alert was seen (for deduplication)
	Count            int       `json:"count,omitempty"`             // Number of times this alert was seen

	// Metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	Links    []AlertLink            `json:"links,omitempty"` // Related links (logs, dashboards, etc.)
}

// AlertLink represents a related link for an alert
type AlertLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type,omitempty"` // "logs", "dashboard", "runbook", etc.
}

// Validate validates the alert
func (a Alert) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("alert ID cannot be empty")
	}

	if a.Source == "" {
		return fmt.Errorf("alert source cannot be empty")
	}

	if a.Title == "" {
		return fmt.Errorf("alert title cannot be empty")
	}

	if a.Message == "" {
		return fmt.Errorf("alert message cannot be empty")
	}

	if !a.Severity.IsValid() {
		return fmt.Errorf("invalid alert severity: %s", a.Severity)
	}

	if !a.Status.IsValid() {
		return fmt.Errorf("invalid alert status: %s", a.Status)
	}

	if a.RetryCount < 0 {
		return fmt.Errorf("retry count cannot be negative (got %d)", a.RetryCount)
	}

	if a.MaxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative (got %d)", a.MaxRetries)
	}

	if a.RetryCount > a.MaxRetries {
		return fmt.Errorf("retry count (%d) cannot exceed max retries (%d)", a.RetryCount, a.MaxRetries)
	}

	if a.CreatedAt.IsZero() {
		return fmt.Errorf("created_at cannot be zero")
	}

	// Validate expiration time
	if a.ExpiresAt != nil && a.ExpiresAt.Before(a.CreatedAt) {
		return fmt.Errorf("expires_at cannot be before created_at")
	}

	// Validate links
	for i, link := range a.Links {
		if err := link.Validate(); err != nil {
			return fmt.Errorf("invalid link %d: %w", i, err)
		}
	}

	if a.Count < 0 {
		return fmt.Errorf("count cannot be negative (got %d)", a.Count)
	}

	return nil
}

// Validate validates an alert link
func (l AlertLink) Validate() error {
	if l.Title == "" {
		return fmt.Errorf("link title cannot be empty")
	}

	if l.URL == "" {
		return fmt.Errorf("link URL cannot be empty")
	}

	// Basic URL validation
	if !strings.HasPrefix(l.URL, "http://") && !strings.HasPrefix(l.URL, "https://") && !strings.HasPrefix(l.URL, "/") {
		return fmt.Errorf("invalid URL format: %s", l.URL)
	}

	return nil
}

// IsExpired checks if the alert has expired
func (a Alert) IsExpired() bool {
	if a.ExpiresAt == nil {
		return false
	}

	return time.Now().After(*a.ExpiresAt)
}

// CanRetry checks if the alert can be retried
func (a Alert) CanRetry() bool {
	return a.Status == AlertStatusFailed && a.RetryCount < a.MaxRetries
}

// ShouldSuppressDuringQuietHours checks if this alert should be suppressed during quiet hours
func (a Alert) ShouldSuppressDuringQuietHours() bool {
	return a.Severity.ShouldRespectQuietHours()
}

// GetDeduplicationKey returns the key used for alert deduplication
func (a Alert) GetDeduplicationKey() string {
	if a.DeduplicationKey != "" {
		return a.DeduplicationKey
	}

	// Default deduplication key: source + component + type
	parts := []string{a.Source}
	if a.Component != "" {
		parts = append(parts, a.Component)
	}
	if a.Type != "" {
		parts = append(parts, a.Type)
	}

	return strings.Join(parts, ":")
}

// GetSuppressionKey returns the key used for alert suppression
func (a Alert) GetSuppressionKey() string {
	if a.SuppressionKey != "" {
		return a.SuppressionKey
	}

	// Default suppression key: same as deduplication key + severity
	return fmt.Sprintf("%s:%s", a.GetDeduplicationKey(), a.Severity)
}

// AlertProcessor interface defines how alerts should be processed and delivered
type AlertProcessor interface {
	// ProcessAlert processes a single alert (validation, routing, delivery)
	ProcessAlert(alert Alert) error

	// ShouldProcessAlert determines if an alert should be processed based on current conditions
	ShouldProcessAlert(alert Alert) (bool, string) // bool: should process, string: reason if not

	// GetTargetsForAlert returns the list of targets that should receive this alert
	GetTargetsForAlert(alert Alert) []string

	// DeliverAlert delivers an alert to a specific target
	DeliverAlert(alert Alert, target string) error

	// SuppressAlert marks an alert as suppressed
	SuppressAlert(alert Alert, reason string) error
}

// AlertDeliveryResult represents the result of attempting to deliver an alert
type AlertDeliveryResult struct {
	Alert     Alert         `json:"alert"`
	Target    string        `json:"target"`
	Success   bool          `json:"success"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
	Duration  time.Duration `json:"duration"`
}

// AlertQueue represents a queue of alerts to be processed
type AlertQueue struct {
	Alerts   []Alert   `json:"alerts"`
	LastSync time.Time `json:"last_sync"`
	Version  int       `json:"version"`

	// Deduplication and suppression tracking
	DeduplicationMap map[string]time.Time `json:"deduplication_map,omitempty"` // Key -> last seen time
	SuppressionMap   map[string]time.Time `json:"suppression_map,omitempty"`   // Key -> suppression expiry
}

// Validate validates the alert queue
func (q AlertQueue) Validate() error {
	alertIDs := make(map[string]bool)

	for i, alert := range q.Alerts {
		if err := alert.Validate(); err != nil {
			return fmt.Errorf("alert %d (%s) validation failed: %w", i, alert.ID, err)
		}

		// Check for duplicate IDs
		if alertIDs[alert.ID] {
			return fmt.Errorf("duplicate alert ID: %s", alert.ID)
		}
		alertIDs[alert.ID] = true
	}

	return nil
}

// GetPendingAlerts returns alerts that are pending delivery
func (q AlertQueue) GetPendingAlerts() []Alert {
	var pending []Alert

	for _, alert := range q.Alerts {
		if alert.Status == AlertStatusPending && !alert.IsExpired() {
			pending = append(pending, alert)
		}
	}

	return pending
}

// GetAlertsByStatus returns alerts with a specific status
func (q AlertQueue) GetAlertsByStatus(status AlertStatus) []Alert {
	var filtered []Alert

	for _, alert := range q.Alerts {
		if alert.Status == status {
			filtered = append(filtered, alert)
		}
	}

	return filtered
}

// GetAlertsBySeverity returns alerts with a specific severity
func (q AlertQueue) GetAlertsBySeverity(severity AlertSeverity) []Alert {
	var filtered []Alert

	for _, alert := range q.Alerts {
		if alert.Severity == severity {
			filtered = append(filtered, alert)
		}
	}

	return filtered
}

// AddAlert adds a new alert to the queue
func (q *AlertQueue) AddAlert(alert Alert) error {
	if err := alert.Validate(); err != nil {
		return fmt.Errorf("invalid alert: %w", err)
	}

	// Check for duplicate ID
	for _, existing := range q.Alerts {
		if existing.ID == alert.ID {
			return fmt.Errorf("alert with ID %s already exists", alert.ID)
		}
	}

	q.Alerts = append(q.Alerts, alert)
	q.Version++

	return nil
}

// UpdateAlertStatus updates the status of an alert
func (q *AlertQueue) UpdateAlertStatus(alertID string, status AlertStatus) error {
	for i, alert := range q.Alerts {
		if alert.ID == alertID {
			q.Alerts[i].Status = status

			if status == AlertStatusSent {
				now := time.Now()
				q.Alerts[i].SentAt = &now
			}

			q.Version++
			return nil
		}
	}

	return fmt.Errorf("alert not found: %s", alertID)
}

// RemoveExpiredAlerts removes alerts that have expired or been successfully sent
func (q *AlertQueue) RemoveExpiredAlerts() {
	var activeAlerts []Alert

	for _, alert := range q.Alerts {
		// Keep alert if it's not expired and not successfully sent
		if !alert.IsExpired() && alert.Status != AlertStatusSent {
			activeAlerts = append(activeAlerts, alert)
		}
	}

	if len(activeAlerts) != len(q.Alerts) {
		q.Alerts = activeAlerts
		q.Version++
	}
}

// IsSuppressed checks if an alert should be suppressed based on deduplication/suppression rules
func (q *AlertQueue) IsSuppressed(alert Alert) bool {
	now := time.Now()

	// Check suppression map
	if q.SuppressionMap != nil {
		if suppressUntil, exists := q.SuppressionMap[alert.GetSuppressionKey()]; exists {
			return now.Before(suppressUntil)
		}
	}

	return false
}

// SuppressAlert suppresses an alert for a given duration
func (q *AlertQueue) SuppressAlert(alert Alert, duration time.Duration) {
	if q.SuppressionMap == nil {
		q.SuppressionMap = make(map[string]time.Time)
	}

	suppressUntil := time.Now().Add(duration)
	q.SuppressionMap[alert.GetSuppressionKey()] = suppressUntil
}

// CleanupExpiredSuppression removes expired suppression entries
func (q *AlertQueue) CleanupExpiredSuppression() {
	if q.SuppressionMap == nil {
		return
	}

	now := time.Now()
	for key, expiry := range q.SuppressionMap {
		if now.After(expiry) {
			delete(q.SuppressionMap, key)
		}
	}

	// Clean up deduplication map entries older than 24 hours
	if q.DeduplicationMap != nil {
		cutoff := now.Add(-24 * time.Hour)
		for key, lastSeen := range q.DeduplicationMap {
			if lastSeen.Before(cutoff) {
				delete(q.DeduplicationMap, key)
			}
		}
	}
}
