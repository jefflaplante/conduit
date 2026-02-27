package heartbeat

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAlertSeverityValidation(t *testing.T) {
	tests := []struct {
		name       string
		severity   AlertSeverity
		isValid    bool
		priority   int
		quietHours bool
	}{
		{"critical", AlertSeverityCritical, true, 10, false},
		{"warning", AlertSeverityWarning, true, 5, true},
		{"info", AlertSeverityInfo, true, 1, true},
		{"invalid", AlertSeverity("invalid"), false, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.severity.IsValid() != tt.isValid {
				t.Errorf("expected IsValid() = %v, got %v", tt.isValid, tt.severity.IsValid())
			}

			if tt.severity.Priority() != tt.priority {
				t.Errorf("expected Priority() = %d, got %d", tt.priority, tt.severity.Priority())
			}

			if tt.severity.ShouldRespectQuietHours() != tt.quietHours {
				t.Errorf("expected ShouldRespectQuietHours() = %v, got %v", tt.quietHours, tt.severity.ShouldRespectQuietHours())
			}
		})
	}
}

func TestAlertSeverityJSONSerialization(t *testing.T) {
	tests := []struct {
		name     string
		severity AlertSeverity
	}{
		{"critical", AlertSeverityCritical},
		{"warning", AlertSeverityWarning},
		{"info", AlertSeverityInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.severity)
			if err != nil {
				t.Fatalf("failed to marshal AlertSeverity: %v", err)
			}

			// Unmarshal
			var unmarshaled AlertSeverity
			if err := json.Unmarshal(data, &unmarshaled); err != nil {
				t.Fatalf("failed to unmarshal AlertSeverity: %v", err)
			}

			if unmarshaled != tt.severity {
				t.Errorf("expected %s, got %s", tt.severity, unmarshaled)
			}
		})
	}

	// Test invalid JSON
	invalidJSON := `"invalid_severity"`
	var severity AlertSeverity
	if err := json.Unmarshal([]byte(invalidJSON), &severity); err == nil {
		t.Error("expected error when unmarshaling invalid alert severity")
	}
}

func TestAlertStatusValidation(t *testing.T) {
	tests := []struct {
		name    string
		status  AlertStatus
		isValid bool
	}{
		{"pending", AlertStatusPending, true},
		{"sent", AlertStatusSent, true},
		{"failed", AlertStatusFailed, true},
		{"suppressed", AlertStatusSuppressed, true},
		{"invalid", AlertStatus("invalid"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.status.IsValid() != tt.isValid {
				t.Errorf("expected IsValid() = %v, got %v", tt.isValid, tt.status.IsValid())
			}
		})
	}
}

func TestAlertValidation(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	tests := []struct {
		name    string
		alert   Alert
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid alert",
			alert: Alert{
				ID:         "alert-1",
				Source:     "test-service",
				Component:  "component-a",
				Type:       "disk_space",
				Title:      "Disk Space Low",
				Message:    "Disk space is running low",
				Severity:   AlertSeverityCritical,
				Category:   "system",
				CreatedAt:  now,
				ExpiresAt:  &future,
				Status:     AlertStatusPending,
				RetryCount: 0,
				MaxRetries: 3,
				Tags:       []string{"disk", "storage"},
				Links: []AlertLink{
					{Title: "Dashboard", URL: "https://example.com/dashboard", Type: "dashboard"},
				},
			},
			wantErr: false,
		},
		{
			name: "empty ID",
			alert: Alert{
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "alert ID cannot be empty",
		},
		{
			name: "empty source",
			alert: Alert{
				ID:        "alert-1",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "alert source cannot be empty",
		},
		{
			name: "empty title",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "alert title cannot be empty",
		},
		{
			name: "empty message",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "alert message cannot be empty",
		},
		{
			name: "invalid severity",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverity("invalid"),
				CreatedAt: now,
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "invalid alert severity",
		},
		{
			name: "invalid status",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatus("invalid"),
			},
			wantErr: true,
			errMsg:  "invalid alert status",
		},
		{
			name: "negative retry count",
			alert: Alert{
				ID:         "alert-1",
				Source:     "test-service",
				Title:      "Test Alert",
				Message:    "Test message",
				Severity:   AlertSeverityInfo,
				CreatedAt:  now,
				Status:     AlertStatusPending,
				RetryCount: -1,
				MaxRetries: 3,
			},
			wantErr: true,
			errMsg:  "retry count cannot be negative",
		},
		{
			name: "retry count exceeds max",
			alert: Alert{
				ID:         "alert-1",
				Source:     "test-service",
				Title:      "Test Alert",
				Message:    "Test message",
				Severity:   AlertSeverityInfo,
				CreatedAt:  now,
				Status:     AlertStatusPending,
				RetryCount: 5,
				MaxRetries: 3,
			},
			wantErr: true,
			errMsg:  "retry count (5) cannot exceed max retries (3)",
		},
		{
			name: "expires before created",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				ExpiresAt: func() *time.Time { t := now.Add(-time.Hour); return &t }(), // Before created time
				Status:    AlertStatusPending,
			},
			wantErr: true,
			errMsg:  "expires_at cannot be before created_at",
		},
		{
			name: "invalid link",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
				Links: []AlertLink{
					{Title: "", URL: "https://example.com"}, // Empty title
				},
			},
			wantErr: true,
			errMsg:  "invalid link",
		},
		{
			name: "negative count",
			alert: Alert{
				ID:        "alert-1",
				Source:    "test-service",
				Title:     "Test Alert",
				Message:   "Test message",
				Severity:  AlertSeverityInfo,
				CreatedAt: now,
				Status:    AlertStatusPending,
				Count:     -1,
			},
			wantErr: true,
			errMsg:  "count cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.alert.Validate()

			if tt.wantErr {
				if err == nil {
					t.Error("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestAlertLinkValidation(t *testing.T) {
	tests := []struct {
		name    string
		link    AlertLink
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid HTTP link",
			link: AlertLink{
				Title: "Dashboard",
				URL:   "https://example.com/dashboard",
				Type:  "dashboard",
			},
			wantErr: false,
		},
		{
			name: "valid relative link",
			link: AlertLink{
				Title: "Local Dashboard",
				URL:   "/dashboard",
				Type:  "dashboard",
			},
			wantErr: false,
		},
		{
			name: "empty title",
			link: AlertLink{
				Title: "",
				URL:   "https://example.com",
			},
			wantErr: true,
			errMsg:  "link title cannot be empty",
		},
		{
			name: "empty URL",
			link: AlertLink{
				Title: "Dashboard",
				URL:   "",
			},
			wantErr: true,
			errMsg:  "link URL cannot be empty",
		},
		{
			name: "invalid URL format",
			link: AlertLink{
				Title: "Dashboard",
				URL:   "invalid-url",
			},
			wantErr: true,
			errMsg:  "invalid URL format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.link.Validate()

			if tt.wantErr {
				if err == nil {
					t.Error("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestAlertBehaviors(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	t.Run("IsExpired", func(t *testing.T) {
		tests := []struct {
			name     string
			alert    Alert
			expected bool
		}{
			{
				name: "expired alert",
				alert: Alert{
					ExpiresAt: &past,
				},
				expected: true,
			},
			{
				name: "not expired alert",
				alert: Alert{
					ExpiresAt: &future,
				},
				expected: false,
			},
			{
				name:     "no expiration",
				alert:    Alert{},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.alert.IsExpired() != tt.expected {
					t.Errorf("expected IsExpired() = %v, got %v", tt.expected, tt.alert.IsExpired())
				}
			})
		}
	})

	t.Run("CanRetry", func(t *testing.T) {
		tests := []struct {
			name     string
			alert    Alert
			expected bool
		}{
			{
				name: "can retry",
				alert: Alert{
					Status:     AlertStatusFailed,
					RetryCount: 1,
					MaxRetries: 3,
				},
				expected: true,
			},
			{
				name: "cannot retry - wrong status",
				alert: Alert{
					Status:     AlertStatusSent,
					RetryCount: 1,
					MaxRetries: 3,
				},
				expected: false,
			},
			{
				name: "cannot retry - max retries reached",
				alert: Alert{
					Status:     AlertStatusFailed,
					RetryCount: 3,
					MaxRetries: 3,
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.alert.CanRetry() != tt.expected {
					t.Errorf("expected CanRetry() = %v, got %v", tt.expected, tt.alert.CanRetry())
				}
			})
		}
	})

	t.Run("ShouldSuppressDuringQuietHours", func(t *testing.T) {
		tests := []struct {
			name     string
			alert    Alert
			expected bool
		}{
			{
				name: "critical - should not suppress",
				alert: Alert{
					Severity: AlertSeverityCritical,
				},
				expected: false,
			},
			{
				name: "warning - should suppress",
				alert: Alert{
					Severity: AlertSeverityWarning,
				},
				expected: true,
			},
			{
				name: "info - should suppress",
				alert: Alert{
					Severity: AlertSeverityInfo,
				},
				expected: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.alert.ShouldSuppressDuringQuietHours() != tt.expected {
					t.Errorf("expected ShouldSuppressDuringQuietHours() = %v, got %v", tt.expected, tt.alert.ShouldSuppressDuringQuietHours())
				}
			})
		}
	})

	t.Run("GetDeduplicationKey", func(t *testing.T) {
		tests := []struct {
			name     string
			alert    Alert
			expected string
		}{
			{
				name: "with custom key",
				alert: Alert{
					DeduplicationKey: "custom-key",
				},
				expected: "custom-key",
			},
			{
				name: "default key - full",
				alert: Alert{
					Source:    "service-a",
					Component: "component-b",
					Type:      "disk_space",
				},
				expected: "service-a:component-b:disk_space",
			},
			{
				name: "default key - no component",
				alert: Alert{
					Source: "service-a",
					Type:   "disk_space",
				},
				expected: "service-a:disk_space",
			},
			{
				name: "default key - source only",
				alert: Alert{
					Source: "service-a",
				},
				expected: "service-a",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.alert.GetDeduplicationKey() != tt.expected {
					t.Errorf("expected GetDeduplicationKey() = %s, got %s", tt.expected, tt.alert.GetDeduplicationKey())
				}
			})
		}
	})

	t.Run("GetSuppressionKey", func(t *testing.T) {
		tests := []struct {
			name     string
			alert    Alert
			expected string
		}{
			{
				name: "with custom key",
				alert: Alert{
					SuppressionKey: "custom-suppression-key",
				},
				expected: "custom-suppression-key",
			},
			{
				name: "default key",
				alert: Alert{
					Source:   "service-a",
					Type:     "disk_space",
					Severity: AlertSeverityCritical,
				},
				expected: "service-a:disk_space:critical",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.alert.GetSuppressionKey() != tt.expected {
					t.Errorf("expected GetSuppressionKey() = %s, got %s", tt.expected, tt.alert.GetSuppressionKey())
				}
			})
		}
	})
}

func TestAlertQueue(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	alert1 := Alert{
		ID:        "alert-1",
		Source:    "service-a",
		Title:     "Alert 1",
		Message:   "Message 1",
		Severity:  AlertSeverityCritical,
		CreatedAt: now,
		Status:    AlertStatusPending,
	}

	alert2 := Alert{
		ID:        "alert-2",
		Source:    "service-b",
		Title:     "Alert 2",
		Message:   "Message 2",
		Severity:  AlertSeverityWarning,
		CreatedAt: now,
		ExpiresAt: &future,
		Status:    AlertStatusSent,
	}

	alert3 := Alert{
		ID:        "alert-3",
		Source:    "service-c",
		Title:     "Alert 3",
		Message:   "Message 3",
		Severity:  AlertSeverityInfo,
		CreatedAt: now,
		ExpiresAt: func() *time.Time { t := now.Add(-time.Hour); return &t }(), // Expired
		Status:    AlertStatusPending,
	}

	t.Run("AddAlert", func(t *testing.T) {
		queue := &AlertQueue{}

		// Add valid alert
		if err := queue.AddAlert(alert1); err != nil {
			t.Errorf("unexpected error adding alert: %v", err)
		}

		if len(queue.Alerts) != 1 {
			t.Errorf("expected 1 alert in queue, got %d", len(queue.Alerts))
		}

		// Add duplicate ID
		if err := queue.AddAlert(alert1); err == nil {
			t.Error("expected error adding duplicate alert")
		}
	})

	t.Run("GetPendingAlerts", func(t *testing.T) {
		queue := AlertQueue{
			Alerts: []Alert{alert1, alert2, alert3},
		}

		pending := queue.GetPendingAlerts()

		// Should only return alert1 (pending and not expired)
		if len(pending) != 1 {
			t.Errorf("expected 1 pending alert, got %d", len(pending))
		}

		if pending[0].ID != "alert-1" {
			t.Errorf("expected pending alert ID 'alert-1', got %s", pending[0].ID)
		}
	})

	t.Run("GetAlertsByStatus", func(t *testing.T) {
		queue := AlertQueue{
			Alerts: []Alert{alert1, alert2, alert3},
		}

		pending := queue.GetAlertsByStatus(AlertStatusPending)
		if len(pending) != 2 {
			t.Errorf("expected 2 pending alerts, got %d", len(pending))
		}

		sent := queue.GetAlertsByStatus(AlertStatusSent)
		if len(sent) != 1 {
			t.Errorf("expected 1 sent alert, got %d", len(sent))
		}
	})

	t.Run("GetAlertsBySeverity", func(t *testing.T) {
		queue := AlertQueue{
			Alerts: []Alert{alert1, alert2, alert3},
		}

		critical := queue.GetAlertsBySeverity(AlertSeverityCritical)
		if len(critical) != 1 {
			t.Errorf("expected 1 critical alert, got %d", len(critical))
		}

		if critical[0].ID != "alert-1" {
			t.Errorf("expected critical alert ID 'alert-1', got %s", critical[0].ID)
		}
	})

	t.Run("UpdateAlertStatus", func(t *testing.T) {
		queue := AlertQueue{
			Alerts:  []Alert{alert1},
			Version: 1,
		}

		// Update existing alert
		if err := queue.UpdateAlertStatus("alert-1", AlertStatusSent); err != nil {
			t.Errorf("unexpected error updating alert status: %v", err)
		}

		if queue.Alerts[0].Status != AlertStatusSent {
			t.Errorf("expected status Sent, got %s", queue.Alerts[0].Status)
		}

		if queue.Alerts[0].SentAt == nil {
			t.Error("expected SentAt to be set")
		}

		if queue.Version != 2 {
			t.Errorf("expected version 2, got %d", queue.Version)
		}

		// Update non-existing alert
		if err := queue.UpdateAlertStatus("non-existent", AlertStatusFailed); err == nil {
			t.Error("expected error updating non-existent alert")
		}
	})

	t.Run("RemoveExpiredAlerts", func(t *testing.T) {
		queue := AlertQueue{
			Alerts: []Alert{alert1, alert2, alert3},
		}

		queue.RemoveExpiredAlerts()

		// Should remove expired and sent alerts
		expectedCount := 1 // Only alert1 should remain (pending and not expired)
		if len(queue.Alerts) != expectedCount {
			t.Errorf("expected %d alerts after cleanup, got %d", expectedCount, len(queue.Alerts))
		}

		// Check that only alert1 remains
		if len(queue.Alerts) > 0 && queue.Alerts[0].ID != "alert-1" {
			t.Errorf("expected remaining alert ID 'alert-1', got %s", queue.Alerts[0].ID)
		}
	})

	t.Run("Suppression", func(t *testing.T) {
		queue := &AlertQueue{}

		// Test IsSuppressed - should return false initially
		if queue.IsSuppressed(alert1) {
			t.Error("expected alert not to be suppressed initially")
		}

		// Suppress alert
		duration := time.Hour
		queue.SuppressAlert(alert1, duration)

		// Test IsSuppressed - should return true now
		if !queue.IsSuppressed(alert1) {
			t.Error("expected alert to be suppressed after suppression")
		}

		// Test CleanupExpiredSuppression
		queue.SuppressionMap[alert1.GetSuppressionKey()] = now.Add(-time.Hour) // Expired
		queue.CleanupExpiredSuppression()

		if queue.IsSuppressed(alert1) {
			t.Error("expected alert not to be suppressed after cleanup")
		}
	})
}

func TestAlertJSONSerialization(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	alert := Alert{
		ID:               "test-alert",
		Source:           "test-service",
		Component:        "test-component",
		Type:             "disk_space",
		Title:            "Test Alert",
		Message:          "Test message",
		Details:          "Detailed information",
		Severity:         AlertSeverityCritical,
		Category:         "system",
		Tags:             []string{"disk", "storage"},
		CreatedAt:        now,
		ExpiresAt:        &future,
		Status:           AlertStatusPending,
		RetryCount:       1,
		MaxRetries:       3,
		LastError:        "test error",
		Targets:          []string{"telegram", "email"},
		DeliveredTo:      []string{"telegram"},
		DeduplicationKey: "custom-dedup-key",
		SuppressionKey:   "custom-suppression-key",
		LastSeenAt:       now,
		Count:            5,
		Metadata:         map[string]interface{}{"key": "value"},
		Links: []AlertLink{
			{Title: "Dashboard", URL: "https://example.com/dashboard", Type: "dashboard"},
			{Title: "Logs", URL: "https://example.com/logs", Type: "logs"},
		},
	}

	// Marshal
	data, err := json.Marshal(alert)
	if err != nil {
		t.Fatalf("failed to marshal alert: %v", err)
	}

	// Unmarshal
	var unmarshaled Alert
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal alert: %v", err)
	}

	// Validate round trip
	if err := unmarshaled.Validate(); err != nil {
		t.Errorf("unmarshaled alert validation failed: %v", err)
	}

	// Check critical fields
	if unmarshaled.ID != alert.ID {
		t.Errorf("ID mismatch: expected %s, got %s", alert.ID, unmarshaled.ID)
	}

	if unmarshaled.Severity != alert.Severity {
		t.Errorf("Severity mismatch: expected %s, got %s", alert.Severity, unmarshaled.Severity)
	}

	if unmarshaled.Status != alert.Status {
		t.Errorf("Status mismatch: expected %s, got %s", alert.Status, unmarshaled.Status)
	}

	if len(unmarshaled.Links) != len(alert.Links) {
		t.Errorf("Links count mismatch: expected %d, got %d", len(alert.Links), len(unmarshaled.Links))
	}

	if len(unmarshaled.Tags) != len(alert.Tags) {
		t.Errorf("Tags count mismatch: expected %d, got %d", len(alert.Tags), len(unmarshaled.Tags))
	}
}
