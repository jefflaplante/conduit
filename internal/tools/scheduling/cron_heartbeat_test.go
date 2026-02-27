package scheduling

import (
	"context"
	"testing"

	"conduit/internal/tools/types"
)

// Ensure context is used
var _ = context.Background

// mockGatewayService implements the necessary parts of GatewayService for testing
type mockGatewayService struct {
	jobs []types.SchedulerJob
}

func (m *mockGatewayService) ListJobs() []*types.SchedulerJob {
	result := make([]*types.SchedulerJob, len(m.jobs))
	for i := range m.jobs {
		result[i] = &m.jobs[i]
	}
	return result
}

func (m *mockGatewayService) EnableJob(jobID string) error {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = true
			return nil
		}
	}
	return nil
}

func (m *mockGatewayService) DisableJob(jobID string) error {
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = false
			return nil
		}
	}
	return nil
}

func (m *mockGatewayService) GetHeartbeatJobCount() int {
	count := 0
	for _, job := range m.jobs {
		if isHeartbeatJob(&job) && job.Enabled {
			count++
		}
	}
	return count
}

// Mock other required methods
func (m *mockGatewayService) ScheduleJob(job *types.SchedulerJob) error { return nil }
func (m *mockGatewayService) CancelJob(jobID string) error              { return nil }
func (m *mockGatewayService) RunJobNow(jobID string) error              { return nil }
func (m *mockGatewayService) GetSchedulerStatus() map[string]interface{} {
	return map[string]interface{}{
		"enabled":      true,
		"total_jobs":   len(m.jobs),
		"go_jobs":      len(m.jobs),
		"system_jobs":  0,
		"cron_entries": len(m.jobs),
	}
}

// Additional GatewayService interface methods
func (m *mockGatewayService) SendToSession(ctx context.Context, sessionKey, label, message string) error {
	return nil
}
func (m *mockGatewayService) SpawnSubAgent(ctx context.Context, task, agentId, model, label string, timeoutSeconds int) (string, error) {
	return "", nil
}
func (m *mockGatewayService) SpawnSubAgentWithCallback(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, parentChannelID, parentUserID string, announce bool) (string, error) {
	return "", nil
}
func (m *mockGatewayService) GetSessionStatus(ctx context.Context, sessionKey string) (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockGatewayService) GetGatewayStatus() (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockGatewayService) RestartGateway(ctx context.Context) error {
	return nil
}
func (m *mockGatewayService) GetChannelStatus() (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockGatewayService) EnableChannel(ctx context.Context, channelID string) error {
	return nil
}
func (m *mockGatewayService) DisableChannel(ctx context.Context, channelID string) error {
	return nil
}
func (m *mockGatewayService) GetConfiguration() (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockGatewayService) UpdateConfiguration(ctx context.Context, config map[string]interface{}) error {
	return nil
}
func (m *mockGatewayService) GetMetrics() (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockGatewayService) GetVersion() string {
	return "test"
}

func TestHeartbeatJobDetection(t *testing.T) {
	testCases := []struct {
		name     string
		job      types.SchedulerJob
		expected bool
	}{
		{
			name: "heartbeat job by ID prefix",
			job: types.SchedulerJob{
				ID:      "heartbeat_main",
				Name:    "Main heartbeat",
				Command: "Check system status",
				Enabled: true,
			},
			expected: true,
		},
		{
			name: "heartbeat job by command content",
			job: types.SchedulerJob{
				ID:      "system_check",
				Name:    "System check",
				Command: "Run heartbeat check for monitoring",
				Enabled: true,
			},
			expected: true,
		},
		{
			name: "heartbeat job by name",
			job: types.SchedulerJob{
				ID:      "monitor_1",
				Name:    "Daily heartbeat report",
				Command: "Generate monitoring report",
				Enabled: true,
			},
			expected: true,
		},
		{
			name: "regular job",
			job: types.SchedulerJob{
				ID:      "backup_job",
				Name:    "Database backup",
				Command: "Run backup script",
				Enabled: true,
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isHeartbeatJob(&tc.job)
			if result != tc.expected {
				t.Errorf("Expected isHeartbeatJob to return %v for job %s, got %v",
					tc.expected, tc.job.ID, result)
			}
		})
	}
}

func TestCronToolHeartbeatList(t *testing.T) {
	mockGateway := &mockGatewayService{
		jobs: []types.SchedulerJob{
			{
				ID:       "heartbeat_main",
				Name:     "Main heartbeat",
				Command:  "Check HEARTBEAT.md",
				Enabled:  true,
				Schedule: "0 */5 * * * *",
			},
			{
				ID:       "regular_job",
				Name:     "Regular task",
				Command:  "Do something else",
				Enabled:  true,
				Schedule: "0 9 * * *",
			},
			{
				ID:       "heartbeat_backup",
				Name:     "Backup heartbeat",
				Command:  "Secondary heartbeat check",
				Enabled:  false,
				Schedule: "*/10 * * * *",
			},
		},
	}

	services := &types.ToolServices{
		Gateway: mockGateway,
	}

	tool := NewCronTool(services)

	ctx := context.Background()
	args := map[string]interface{}{
		"action": "heartbeat_list",
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Failed to execute heartbeat_list: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Check that only heartbeat jobs are in the result
	jobs, ok := result.Data["jobs"].([]*types.SchedulerJob)
	if !ok {
		t.Fatal("Expected jobs data in result")
	}

	if len(jobs) != 2 {
		t.Errorf("Expected 2 heartbeat jobs, got %d", len(jobs))
	}

	// Verify the jobs are heartbeat jobs
	for _, job := range jobs {
		if !isHeartbeatJob(job) {
			t.Errorf("Non-heartbeat job returned: %s", job.ID)
		}
	}
}

func TestCronToolHeartbeatEnable(t *testing.T) {
	mockGateway := &mockGatewayService{
		jobs: []types.SchedulerJob{
			{
				ID:      "heartbeat_main",
				Name:    "Main heartbeat",
				Enabled: false,
			},
			{
				ID:      "heartbeat_backup",
				Name:    "Backup heartbeat",
				Enabled: false,
			},
			{
				ID:      "regular_job",
				Name:    "Regular task",
				Enabled: false,
			},
		},
	}

	services := &types.ToolServices{
		Gateway: mockGateway,
	}

	tool := NewCronTool(services)

	ctx := context.Background()
	args := map[string]interface{}{
		"action": "heartbeat_enable",
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Failed to execute heartbeat_enable: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Check that 2 jobs were enabled
	enabledCount := result.Data["enabled_count"].(int)
	if enabledCount != 2 {
		t.Errorf("Expected 2 jobs enabled, got %d", enabledCount)
	}

	// Verify heartbeat jobs are now enabled
	for _, job := range mockGateway.jobs {
		if isHeartbeatJob(&job) && !job.Enabled {
			t.Errorf("Heartbeat job %s should be enabled but isn't", job.ID)
		}
	}

	// Verify regular job is still disabled
	for _, job := range mockGateway.jobs {
		if !isHeartbeatJob(&job) && job.Enabled {
			t.Errorf("Regular job %s should not be enabled", job.ID)
		}
	}
}

func TestCronToolHeartbeatStatus(t *testing.T) {
	mockGateway := &mockGatewayService{
		jobs: []types.SchedulerJob{
			{
				ID:      "heartbeat_main",
				Name:    "Main heartbeat",
				Enabled: true,
			},
			{
				ID:      "heartbeat_backup",
				Name:    "Backup heartbeat",
				Enabled: false,
			},
			{
				ID:      "regular_job",
				Name:    "Regular task",
				Enabled: true,
			},
		},
	}

	services := &types.ToolServices{
		Gateway: mockGateway,
	}

	tool := NewCronTool(services)

	ctx := context.Background()
	args := map[string]interface{}{
		"action": "heartbeat_status",
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Failed to execute heartbeat_status: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Check the status data
	data := result.Data
	totalJobs := data["total_jobs"].(int)
	enabledJobs := data["enabled_jobs"].(int)
	disabledJobs := data["disabled_jobs"].(int)
	healthy := data["healthy"].(bool)

	if totalJobs != 2 {
		t.Errorf("Expected 2 total heartbeat jobs, got %d", totalJobs)
	}

	if enabledJobs != 1 {
		t.Errorf("Expected 1 enabled heartbeat job, got %d", enabledJobs)
	}

	if disabledJobs != 1 {
		t.Errorf("Expected 1 disabled heartbeat job, got %d", disabledJobs)
	}

	if !healthy {
		t.Error("System should be healthy with at least one enabled job")
	}
}

func TestCronToolHeartbeatDisable(t *testing.T) {
	mockGateway := &mockGatewayService{
		jobs: []types.SchedulerJob{
			{
				ID:      "heartbeat_main",
				Name:    "Main heartbeat",
				Enabled: true,
			},
			{
				ID:      "heartbeat_backup",
				Name:    "Backup heartbeat",
				Enabled: true,
			},
			{
				ID:      "regular_job",
				Name:    "Regular task",
				Enabled: true,
			},
		},
	}

	services := &types.ToolServices{
		Gateway: mockGateway,
	}

	tool := NewCronTool(services)

	ctx := context.Background()
	args := map[string]interface{}{
		"action": "heartbeat_disable",
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Failed to execute heartbeat_disable: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %s", result.Error)
	}

	// Check that 2 jobs were disabled
	disabledCount := result.Data["disabled_count"].(int)
	if disabledCount != 2 {
		t.Errorf("Expected 2 jobs disabled, got %d", disabledCount)
	}

	// Verify heartbeat jobs are now disabled
	for _, job := range mockGateway.jobs {
		if isHeartbeatJob(&job) && job.Enabled {
			t.Errorf("Heartbeat job %s should be disabled but isn't", job.ID)
		}
	}

	// Verify regular job is still enabled
	for _, job := range mockGateway.jobs {
		if !isHeartbeatJob(&job) && !job.Enabled {
			t.Errorf("Regular job %s should still be enabled", job.ID)
		}
	}
}
