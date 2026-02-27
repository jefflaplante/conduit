package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/monitoring"
	"conduit/internal/scheduler"
)

func TestInitializeAgentHeartbeat(t *testing.T) {
	mockSched := newMockScheduler()
	mockHBI := newMockHeartbeatIntegration(mockSched)
	// Create a mock gateway with minimal setup for testing
	gateway := &Gateway{
		scheduler:            mockSched,
		metricsCollector:     newMockMetricsCollector(),
		heartbeatIntegration: mockHBI,
	}

	tests := []struct {
		name           string
		config         *config.Config
		expectJobCount int
		expectError    bool
	}{
		{
			name: "disabled heartbeat",
			config: &config.Config{
				AgentHeartbeat: config.AgentHeartbeatConfig{
					Enabled: false,
				},
			},
			expectJobCount: 0,
			expectError:    false,
		},
		{
			name: "enabled heartbeat with default settings",
			config: &config.Config{
				AgentHeartbeat: config.AgentHeartbeatConfig{
					Enabled:         true,
					IntervalMinutes: 5,
					AlertTargets: []config.AlertTarget{
						{
							Type: "telegram",
							Config: map[string]string{
								"chat_id": "123456789",
							},
						},
					},
				},
			},
			expectJobCount: 1,
			expectError:    false,
		},
		{
			name: "enabled heartbeat without targets",
			config: &config.Config{
				AgentHeartbeat: config.AgentHeartbeatConfig{
					Enabled:         true,
					IntervalMinutes: 10,
					AlertTargets:    []config.AlertTarget{},
				},
			},
			expectJobCount: 1,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset the mock scheduler and heartbeat integration
			mockSched := newMockScheduler()
			mockHBI := newMockHeartbeatIntegration(mockSched)
			gateway.scheduler = mockSched
			gateway.heartbeatIntegration = mockHBI

			err := gateway.initializeAgentHeartbeat(tt.config)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			}

			// Check job count
			if len(mockSched.jobs) != tt.expectJobCount {
				t.Errorf("Expected %d jobs, got %d", tt.expectJobCount, len(mockSched.jobs))
			}

			// If we expect a job, verify the last scheduled job has correct schedule
			if tt.expectJobCount > 0 && mockHBI.lastScheduledJob != nil {
				expectedSchedule := fmt.Sprintf("0 */%d * * * *", tt.config.AgentHeartbeat.IntervalMinutes)
				if mockHBI.lastScheduledJob.Schedule != expectedSchedule {
					t.Errorf("Expected schedule %s, got %s", expectedSchedule, mockHBI.lastScheduledJob.Schedule)
				}
			}
		})
	}
}

func TestUpdateHeartbeatJobMetrics(t *testing.T) {
	mockMetrics := newMockMetricsCollector()
	mockSched := newMockScheduler()

	// Add test jobs
	mockSched.jobs["heartbeat_main"] = &scheduler.Job{
		ID:      "heartbeat_main",
		Name:    "Main heartbeat",
		Enabled: true,
	}
	mockSched.jobs["heartbeat_secondary"] = &scheduler.Job{
		ID:      "heartbeat_secondary",
		Name:    "Secondary heartbeat",
		Enabled: false,
	}
	mockSched.jobs["regular_job"] = &scheduler.Job{
		ID:      "regular_job",
		Name:    "Regular task",
		Enabled: true,
	}
	mockSched.jobs["another_heartbeat"] = &scheduler.Job{
		ID:      "monitor_task",
		Name:    "Monitor with heartbeat in name",
		Enabled: true,
	}

	gateway := &Gateway{
		scheduler:        mockSched,
		metricsCollector: mockMetrics,
	}

	gateway.updateHeartbeatJobMetrics()

	// Should find 3 heartbeat jobs (2 with heartbeat_ prefix, 1 with heartbeat in name)
	// 2 should be enabled
	if mockMetrics.totalJobs != 3 {
		t.Errorf("Expected 3 total heartbeat jobs, got %d", mockMetrics.totalJobs)
	}

	if mockMetrics.enabledJobs != 2 {
		t.Errorf("Expected 2 enabled heartbeat jobs, got %d", mockMetrics.enabledJobs)
	}
}

// Mock implementations for testing

// mockScheduler implements scheduler.SchedulerInterface
type mockScheduler struct {
	jobs map[string]*scheduler.Job
}

func newMockScheduler() *mockScheduler {
	return &mockScheduler{
		jobs: make(map[string]*scheduler.Job),
	}
}

func (m *mockScheduler) Start() error {
	return nil
}

func (m *mockScheduler) Stop() {}

func (m *mockScheduler) AddJob(job *scheduler.Job) error {
	m.jobs[job.ID] = job
	return nil
}

func (m *mockScheduler) RemoveJob(jobID string) error {
	delete(m.jobs, jobID)
	return nil
}

func (m *mockScheduler) GetJob(jobID string) (*scheduler.Job, error) {
	job, ok := m.jobs[jobID]
	if !ok {
		return nil, nil
	}
	return job, nil
}

func (m *mockScheduler) ListJobs() []*scheduler.Job {
	var result []*scheduler.Job
	for _, job := range m.jobs {
		result = append(result, job)
	}
	return result
}

func (m *mockScheduler) EnableJob(jobID string) error {
	if job, ok := m.jobs[jobID]; ok {
		job.Enabled = true
	}
	return nil
}

func (m *mockScheduler) DisableJob(jobID string) error {
	if job, ok := m.jobs[jobID]; ok {
		job.Enabled = false
	}
	return nil
}

func (m *mockScheduler) RunNow(jobID string) error {
	return nil
}

func (m *mockScheduler) Status() map[string]interface{} {
	return map[string]interface{}{
		"total_jobs": len(m.jobs),
	}
}

// mockMetricsCollector implements monitoring.MetricsCollectorInterface
type mockMetricsCollector struct {
	totalJobs        int
	enabledJobs      int
	wsConnections    int
	activeRequests   int
	queueDepth       int
	lastActivityTime time.Time
}

func newMockMetricsCollector() *mockMetricsCollector {
	return &mockMetricsCollector{
		lastActivityTime: time.Now(),
	}
}

func (m *mockMetricsCollector) UpdateWebSocketConnections(count int) {
	m.wsConnections = count
}

func (m *mockMetricsCollector) UpdateActiveRequests(count int) {
	m.activeRequests = count
}

func (m *mockMetricsCollector) UpdateQueueDepth(depth int) {
	m.queueDepth = depth
}

func (m *mockMetricsCollector) MarkActivity() {
	m.lastActivityTime = time.Now()
}

func (m *mockMetricsCollector) GetLastActivityTime() time.Time {
	return m.lastActivityTime
}

func (m *mockMetricsCollector) IsIdle(duration time.Duration) bool {
	return time.Since(m.lastActivityTime) > duration
}

func (m *mockMetricsCollector) UpdateHeartbeatJobs(total, enabled int) {
	m.totalJobs = total
	m.enabledJobs = enabled
}

func (m *mockMetricsCollector) MarkHeartbeatSuccess() {}

func (m *mockMetricsCollector) MarkHeartbeatError() {}

func (m *mockMetricsCollector) GetHeartbeatMetrics() monitoring.HeartbeatMetrics {
	return monitoring.HeartbeatMetrics{
		TotalJobs:     m.totalJobs,
		EnabledJobs:   m.enabledJobs,
		LastExecution: time.Now(),
		SuccessCount:  10,
		ErrorCount:    1,
		IsHealthy:     m.enabledJobs > 0,
	}
}

func (m *mockMetricsCollector) CollectMetrics(ctx context.Context) (*monitoring.GatewayMetrics, error) {
	return monitoring.NewGatewayMetrics(), nil
}

func (m *mockMetricsCollector) GetDetailedStats(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{}, nil
}

func (m *mockMetricsCollector) DetectStuckSessions(ctx context.Context, threshold time.Duration) ([]string, error) {
	return nil, nil
}

func (m *mockMetricsCollector) ValidateDatabase(ctx context.Context) error {
	return nil
}

// mockHeartbeatIntegration implements heartbeat.HeartbeatIntegrationInterface
type mockHeartbeatIntegration struct {
	scheduler        *mockScheduler
	lastScheduledJob *scheduler.Job
	jobCount         int
}

func newMockHeartbeatIntegration(sched *mockScheduler) *mockHeartbeatIntegration {
	return &mockHeartbeatIntegration{
		scheduler: sched,
	}
}

func (m *mockHeartbeatIntegration) ExecuteHeartbeat(ctx context.Context, job *scheduler.Job) error {
	return nil
}

func (m *mockHeartbeatIntegration) ScheduleHeartbeatJob(schedule, target, model string, enabled bool) error {
	job := &scheduler.Job{
		ID:       fmt.Sprintf("heartbeat_%d", time.Now().UnixNano()),
		Name:     "Heartbeat Task Execution",
		Schedule: schedule,
		Type:     scheduler.JobTypeGo,
		Command:  "heartbeat",
		Model:    model,
		Target:   target,
		Enabled:  enabled,
	}
	m.lastScheduledJob = job
	m.jobCount++
	return m.scheduler.AddJob(job)
}

func (m *mockHeartbeatIntegration) GetHeartbeatJobCount() int {
	return m.jobCount
}

func (m *mockHeartbeatIntegration) RemoveHeartbeatJobs() error {
	m.jobCount = 0
	return nil
}

func TestHeartbeatScheduleFormat(t *testing.T) {
	testCases := []struct {
		intervalMinutes int
		expectedCron    string
	}{
		{1, "0 */1 * * * *"},
		{5, "0 */5 * * * *"},
		{10, "0 */10 * * * *"},
		{15, "0 */15 * * * *"},
		{30, "0 */30 * * * *"},
		{60, "0 */60 * * * *"},
	}

	for _, tc := range testCases {
		t.Run(tc.expectedCron, func(t *testing.T) {
			mockSched := newMockScheduler()
			mockHBI := newMockHeartbeatIntegration(mockSched)
			gateway := &Gateway{
				scheduler:            mockSched,
				metricsCollector:     newMockMetricsCollector(),
				heartbeatIntegration: mockHBI,
			}

			cfg := &config.Config{
				AgentHeartbeat: config.AgentHeartbeatConfig{
					Enabled:         true,
					IntervalMinutes: tc.intervalMinutes,
				},
			}

			err := gateway.initializeAgentHeartbeat(cfg)
			if err != nil {
				t.Fatalf("Failed to initialize heartbeat: %v", err)
			}

			// Check that the schedule was set correctly
			job := mockHBI.lastScheduledJob
			if job == nil {
				t.Fatal("Expected heartbeat job to be created")
			}

			if job.Schedule != tc.expectedCron {
				t.Errorf("Expected schedule %s, got %s", tc.expectedCron, job.Schedule)
			}
		})
	}
}

func TestHeartbeatTargetConfiguration(t *testing.T) {
	testCases := []struct {
		name           string
		alertTargets   []config.AlertTarget
		expectedTarget string
	}{
		{
			name:           "no targets",
			alertTargets:   []config.AlertTarget{},
			expectedTarget: "",
		},
		{
			name: "telegram target",
			alertTargets: []config.AlertTarget{
				{
					Type: "telegram",
					Config: map[string]string{
						"chat_id": "123456789",
					},
				},
			},
			expectedTarget: "telegram:123456789",
		},
		{
			name: "multiple targets - uses first",
			alertTargets: []config.AlertTarget{
				{
					Type: "telegram",
					Config: map[string]string{
						"chat_id": "111111111",
					},
				},
				{
					Type: "telegram",
					Config: map[string]string{
						"chat_id": "222222222",
					},
				},
			},
			expectedTarget: "telegram:111111111",
		},
		{
			name: "non-telegram target",
			alertTargets: []config.AlertTarget{
				{
					Type: "email",
					Config: map[string]string{
						"address": "test@example.com",
					},
				},
			},
			expectedTarget: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockSched := newMockScheduler()
			mockHBI := newMockHeartbeatIntegration(mockSched)
			gateway := &Gateway{
				scheduler:            mockSched,
				metricsCollector:     newMockMetricsCollector(),
				heartbeatIntegration: mockHBI,
			}

			cfg := &config.Config{
				AgentHeartbeat: config.AgentHeartbeatConfig{
					Enabled:         true,
					IntervalMinutes: 5,
					AlertTargets:    tc.alertTargets,
				},
			}

			err := gateway.initializeAgentHeartbeat(cfg)
			if err != nil {
				t.Fatalf("Failed to initialize heartbeat: %v", err)
			}

			// Check that the target was set correctly
			job := mockHBI.lastScheduledJob
			if job == nil {
				t.Fatal("Expected heartbeat job to be created")
			}

			if job.Target != tc.expectedTarget {
				t.Errorf("Expected target '%s', got '%s'", tc.expectedTarget, job.Target)
			}
		})
	}
}
