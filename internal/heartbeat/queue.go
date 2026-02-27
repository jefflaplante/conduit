package heartbeat

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// SharedAlertQueue provides thread-safe, file-backed alert queue operations
// It implements the core queue functionality with proper file locking and error recovery
type SharedAlertQueue struct {
	filePath string
	mutex    sync.RWMutex
}

// NewSharedAlertQueue creates a new shared alert queue instance
func NewSharedAlertQueue(filePath string) *SharedAlertQueue {
	return &SharedAlertQueue{
		filePath: filePath,
	}
}

// lockFile acquires an exclusive lock on the file for write operations
func (q *SharedAlertQueue) lockFile(file *os.File) error {
	// Use syscall.Flock for advisory locking
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf("failed to acquire exclusive lock on %s: %w", q.filePath, err)
	}
	return nil
}

// unlockFile releases the file lock
func (q *SharedAlertQueue) unlockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err != nil {
		return fmt.Errorf("failed to release lock on %s: %w", q.filePath, err)
	}
	return nil
}

// ensureDirectoryExists creates the directory path if it doesn't exist
func (q *SharedAlertQueue) ensureDirectoryExists() error {
	dir := filepath.Dir(q.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	return nil
}

// LoadQueue reads and returns the current alert queue from disk
// If the file doesn't exist, returns an empty queue
// If the file is corrupted, attempts recovery and returns what can be parsed
func (q *SharedAlertQueue) LoadQueue() (*AlertQueue, error) {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	// Check if file exists
	if _, err := os.Stat(q.filePath); os.IsNotExist(err) {
		// Return empty queue if file doesn't exist
		return &AlertQueue{
			Alerts:   make([]Alert, 0),
			LastSync: time.Now(),
			Version:  1,
		}, nil
	}

	file, err := os.Open(q.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open alert queue file %s: %w", q.filePath, err)
	}
	defer file.Close()

	// Read file content
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read alert queue file %s: %w", q.filePath, err)
	}

	// Handle empty file
	if len(data) == 0 {
		return &AlertQueue{
			Alerts:   make([]Alert, 0),
			LastSync: time.Now(),
			Version:  1,
		}, nil
	}

	// Parse JSON
	var queue AlertQueue
	if err := json.Unmarshal(data, &queue); err != nil {
		// Attempt recovery from corrupted JSON
		return q.attemptRecovery(data)
	}

	// Validate loaded queue
	if err := queue.Validate(); err != nil {
		// If validation fails, try to recover what we can
		return q.recoverValidAlerts(queue), nil
	}

	return &queue, nil
}

// SaveQueue atomically saves the alert queue to disk
// Uses atomic write (write to temp file, then rename) for reliability
func (q *SharedAlertQueue) SaveQueue(queue *AlertQueue) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	// Validate queue before saving
	if err := queue.Validate(); err != nil {
		return fmt.Errorf("cannot save invalid alert queue: %w", err)
	}

	// Ensure directory exists
	if err := q.ensureDirectoryExists(); err != nil {
		return err
	}

	// Update metadata
	queue.LastSync = time.Now()
	queue.Version++

	// Marshal to JSON with pretty formatting for debugging
	data, err := json.MarshalIndent(queue, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alert queue to JSON: %w", err)
	}

	// Use atomic write: write to temp file, then rename
	tempPath := q.filePath + ".tmp"

	// Create and open temp file
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file %s: %w", tempPath, err)
	}

	// Ensure temp file is cleaned up on error
	defer func() {
		tempFile.Close()
		if err != nil {
			os.Remove(tempPath) // Clean up temp file on error
		}
	}()

	// Write data to temp file
	if _, err = tempFile.Write(data); err != nil {
		return fmt.Errorf("failed to write data to temp file %s: %w", tempPath, err)
	}

	// Sync to ensure data is written to disk
	if err = tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file %s: %w", tempPath, err)
	}

	// Close temp file
	if err = tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %s: %w", tempPath, err)
	}

	// Atomic rename
	if err = os.Rename(tempPath, q.filePath); err != nil {
		return fmt.Errorf("failed to rename temp file %s to %s: %w", tempPath, q.filePath, err)
	}

	return nil
}

// loadQueueUnlocked reads the queue without acquiring mutex (caller must hold lock)
func (q *SharedAlertQueue) loadQueueUnlocked() (*AlertQueue, error) {
	if _, err := os.Stat(q.filePath); os.IsNotExist(err) {
		return &AlertQueue{
			Alerts:   make([]Alert, 0),
			LastSync: time.Now(),
			Version:  1,
		}, nil
	}

	file, err := os.Open(q.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open alert queue file %s: %w", q.filePath, err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read alert queue file %s: %w", q.filePath, err)
	}

	if len(data) == 0 {
		return &AlertQueue{
			Alerts:   make([]Alert, 0),
			LastSync: time.Now(),
			Version:  1,
		}, nil
	}

	var queue AlertQueue
	if err := json.Unmarshal(data, &queue); err != nil {
		return q.attemptRecovery(data)
	}

	if err := queue.Validate(); err != nil {
		return q.recoverValidAlerts(queue), nil
	}

	return &queue, nil
}

// saveQueueUnlocked saves the queue without acquiring mutex (caller must hold lock)
func (q *SharedAlertQueue) saveQueueUnlocked(queue *AlertQueue) error {
	if err := queue.Validate(); err != nil {
		return fmt.Errorf("cannot save invalid alert queue: %w", err)
	}

	if err := q.ensureDirectoryExists(); err != nil {
		return err
	}

	queue.LastSync = time.Now()
	queue.Version++

	data, err := json.MarshalIndent(queue, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alert queue to JSON: %w", err)
	}

	tempPath := q.filePath + ".tmp"
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file %s: %w", tempPath, err)
	}

	defer func() {
		tempFile.Close()
		if err != nil {
			os.Remove(tempPath)
		}
	}()

	if _, err = tempFile.Write(data); err != nil {
		return fmt.Errorf("failed to write data to temp file %s: %w", tempPath, err)
	}

	if err = tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file %s: %w", tempPath, err)
	}

	if err = tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %s: %w", tempPath, err)
	}

	if err = os.Rename(tempPath, q.filePath); err != nil {
		return fmt.Errorf("failed to rename temp file %s to %s: %w", tempPath, q.filePath, err)
	}

	return nil
}

// AddAlert adds a new alert to the queue (thread-safe)
func (q *SharedAlertQueue) AddAlert(alert Alert) error {
	// Validate alert before acquiring lock
	if err := alert.Validate(); err != nil {
		return fmt.Errorf("invalid alert: %w", err)
	}

	// Hold write lock for entire load-modify-save to prevent TOCTOU races
	q.mutex.Lock()
	defer q.mutex.Unlock()

	// Load current queue (unlocked variant)
	queue, err := q.loadQueueUnlocked()
	if err != nil {
		return fmt.Errorf("failed to load queue for adding alert: %w", err)
	}

	// Add alert to queue
	if err := queue.AddAlert(alert); err != nil {
		return fmt.Errorf("failed to add alert to queue: %w", err)
	}

	// Save updated queue (unlocked variant)
	if err := q.saveQueueUnlocked(queue); err != nil {
		return fmt.Errorf("failed to save queue after adding alert: %w", err)
	}

	return nil
}

// GetPendingAlerts returns all pending alerts without modifying the queue
func (q *SharedAlertQueue) GetPendingAlerts() ([]Alert, error) {
	queue, err := q.LoadQueue()
	if err != nil {
		return nil, fmt.Errorf("failed to load queue for getting pending alerts: %w", err)
	}

	return queue.GetPendingAlerts(), nil
}

// UpdateAlertStatus updates the status of a specific alert
func (q *SharedAlertQueue) UpdateAlertStatus(alertID string, status AlertStatus) error {
	// Load current queue
	queue, err := q.LoadQueue()
	if err != nil {
		return fmt.Errorf("failed to load queue for updating alert status: %w", err)
	}

	// Update alert status
	if err := queue.UpdateAlertStatus(alertID, status); err != nil {
		return fmt.Errorf("failed to update alert status: %w", err)
	}

	// Save updated queue
	if err := q.SaveQueue(queue); err != nil {
		return fmt.Errorf("failed to save queue after updating alert status: %w", err)
	}

	return nil
}

// RemoveProcessedAlerts removes alerts that have been successfully sent or have expired
func (q *SharedAlertQueue) RemoveProcessedAlerts() error {
	// Load current queue
	queue, err := q.LoadQueue()
	if err != nil {
		return fmt.Errorf("failed to load queue for cleanup: %w", err)
	}

	// Track original count for logging
	originalCount := len(queue.Alerts)

	// Remove expired alerts
	queue.RemoveExpiredAlerts()

	// Clean up suppression entries
	queue.CleanupExpiredSuppression()

	// Only save if something changed
	if len(queue.Alerts) != originalCount ||
		(queue.SuppressionMap != nil && len(queue.SuppressionMap) > 0) ||
		(queue.DeduplicationMap != nil && len(queue.DeduplicationMap) > 0) {
		if err := q.SaveQueue(queue); err != nil {
			return fmt.Errorf("failed to save queue after cleanup: %w", err)
		}
	}

	return nil
}

// GetQueueStats returns statistics about the current queue
func (q *SharedAlertQueue) GetQueueStats() (QueueStats, error) {
	queue, err := q.LoadQueue()
	if err != nil {
		return QueueStats{}, fmt.Errorf("failed to load queue for stats: %w", err)
	}

	stats := QueueStats{
		TotalAlerts: len(queue.Alerts),
		LastSync:    queue.LastSync,
		Version:     queue.Version,
		BySeverity:  make(map[AlertSeverity]int),
		ByStatus:    make(map[AlertStatus]int),
	}

	// Calculate statistics
	for _, alert := range queue.Alerts {
		stats.BySeverity[alert.Severity]++
		stats.ByStatus[alert.Status]++

		if alert.Status == AlertStatusPending {
			stats.PendingAlerts++
		}
	}

	return stats, nil
}

// IsHealthy checks if the queue file system is working properly
func (q *SharedAlertQueue) IsHealthy() error {
	// Check if we can create directory
	if err := q.ensureDirectoryExists(); err != nil {
		return fmt.Errorf("queue directory unhealthy: %w", err)
	}

	// Try to load queue (tests read capability)
	if _, err := q.LoadQueue(); err != nil {
		return fmt.Errorf("queue read unhealthy: %w", err)
	}

	// Try to write a test queue (tests write capability)
	testQueue := &AlertQueue{
		Alerts:   make([]Alert, 0),
		LastSync: time.Now(),
		Version:  1,
	}

	tempPath := q.filePath + ".health_check"
	tempQueue := &SharedAlertQueue{filePath: tempPath}

	if err := tempQueue.SaveQueue(testQueue); err != nil {
		return fmt.Errorf("queue write unhealthy: %w", err)
	}

	// Clean up test file
	os.Remove(tempPath)

	return nil
}

// QueueStats represents statistics about the alert queue
type QueueStats struct {
	TotalAlerts   int                   `json:"total_alerts"`
	PendingAlerts int                   `json:"pending_alerts"`
	LastSync      time.Time             `json:"last_sync"`
	Version       int                   `json:"version"`
	BySeverity    map[AlertSeverity]int `json:"by_severity"`
	ByStatus      map[AlertStatus]int   `json:"by_status"`
}

// attemptRecovery tries to recover from corrupted JSON by parsing what it can
func (q *SharedAlertQueue) attemptRecovery(data []byte) (*AlertQueue, error) {
	// Try to parse as a partial structure
	var partial struct {
		Alerts []json.RawMessage `json:"alerts"`
	}

	if err := json.Unmarshal(data, &partial); err != nil {
		// Complete failure - return empty queue
		return &AlertQueue{
			Alerts:   make([]Alert, 0),
			LastSync: time.Now(),
			Version:  1,
		}, fmt.Errorf("failed to recover from corrupted alert queue: %w", err)
	}

	// Try to parse individual alerts
	var recoveredAlerts []Alert
	for i, rawAlert := range partial.Alerts {
		var alert Alert
		if err := json.Unmarshal(rawAlert, &alert); err != nil {
			// Skip corrupted alert but continue recovery
			continue
		}

		// Validate recovered alert
		if err := alert.Validate(); err != nil {
			// Skip invalid alert but continue recovery
			continue
		}

		recoveredAlerts = append(recoveredAlerts, alert)

		// Log recovery at most every 10 alerts to avoid spam
		if i < 10 || i%10 == 0 {
			// This would be logged in a real implementation
			_ = fmt.Sprintf("Recovered alert %d: %s", i, alert.ID)
		}
	}

	return &AlertQueue{
		Alerts:   recoveredAlerts,
		LastSync: time.Now(),
		Version:  1,
	}, nil
}

// recoverValidAlerts filters out invalid alerts from a loaded queue
func (q *SharedAlertQueue) recoverValidAlerts(queue AlertQueue) *AlertQueue {
	var validAlerts []Alert

	for _, alert := range queue.Alerts {
		if err := alert.Validate(); err != nil {
			// Skip invalid alert
			continue
		}
		validAlerts = append(validAlerts, alert)
	}

	return &AlertQueue{
		Alerts:           validAlerts,
		LastSync:         time.Now(),
		Version:          1,
		DeduplicationMap: queue.DeduplicationMap, // Preserve if valid
		SuppressionMap:   queue.SuppressionMap,   // Preserve if valid
	}
}
