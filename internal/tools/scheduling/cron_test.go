package scheduling

import (
	"context"
	"errors"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullMockGatewayService extends mockGatewayService with error simulation
type fullMockGatewayService struct {
	mockGatewayService
	scheduleErr error
	cancelErr   error
	runErr      error
	enableErr   error
	disableErr  error
}

func (m *fullMockGatewayService) ScheduleJob(job *types.SchedulerJob) error {
	if m.scheduleErr != nil {
		return m.scheduleErr
	}
	m.jobs = append(m.jobs, *job)
	return nil
}

func (m *fullMockGatewayService) CancelJob(jobID string) error {
	if m.cancelErr != nil {
		return m.cancelErr
	}
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
			return nil
		}
	}
	return errors.New("job not found")
}

func (m *fullMockGatewayService) RunJobNow(jobID string) error {
	if m.runErr != nil {
		return m.runErr
	}
	for _, job := range m.jobs {
		if job.ID == jobID {
			return nil
		}
	}
	return errors.New("job not found")
}

func (m *fullMockGatewayService) EnableJob(jobID string) error {
	if m.enableErr != nil {
		return m.enableErr
	}
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = true
			return nil
		}
	}
	return errors.New("job not found")
}

func (m *fullMockGatewayService) DisableJob(jobID string) error {
	if m.disableErr != nil {
		return m.disableErr
	}
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = false
			return nil
		}
	}
	return errors.New("job not found")
}

// TestCronToolBasics tests Name, Description, Parameters methods
func TestCronToolBasics(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{})

	t.Run("Name", func(t *testing.T) {
		assert.Equal(t, "Cron", tool.Name())
	})

	t.Run("Description", func(t *testing.T) {
		desc := tool.Description()
		assert.Contains(t, desc, "Schedule recurring tasks")
		assert.Contains(t, desc, "heartbeat")
		assert.Contains(t, desc, "go")
		assert.Contains(t, desc, "system")
	})

	t.Run("Parameters", func(t *testing.T) {
		params := tool.Parameters()
		assert.NotNil(t, params)

		props, ok := params["properties"].(map[string]interface{})
		require.True(t, ok, "parameters should have properties")

		// Check required parameter
		required, ok := params["required"].([]string)
		require.True(t, ok, "parameters should have required field")
		assert.Contains(t, required, "action")

		// Verify key properties exist
		assert.Contains(t, props, "action")
		assert.Contains(t, props, "schedule")
		assert.Contains(t, props, "command")
		assert.Contains(t, props, "name")
		assert.Contains(t, props, "jobType")
		assert.Contains(t, props, "model")
		assert.Contains(t, props, "target")
		assert.Contains(t, props, "jobId")
		assert.Contains(t, props, "oneshot")
		assert.Contains(t, props, "delayMinutes")
		assert.Contains(t, props, "skills")

		// Verify action enum values
		actionProp, ok := props["action"].(map[string]interface{})
		require.True(t, ok, "action should be a map")
		enumVals, ok := actionProp["enum"].([]string)
		require.True(t, ok, "action should have enum")
		assert.Contains(t, enumVals, "schedule")
		assert.Contains(t, enumVals, "list")
		assert.Contains(t, enumVals, "cancel")
		assert.Contains(t, enumVals, "run")
		assert.Contains(t, enumVals, "enable")
		assert.Contains(t, enumVals, "disable")
		assert.Contains(t, enumVals, "status")
		assert.Contains(t, enumVals, "heartbeat_list")
		assert.Contains(t, enumVals, "heartbeat_enable")
		assert.Contains(t, enumVals, "heartbeat_disable")
		assert.Contains(t, enumVals, "heartbeat_status")
	})
}

// TestCronToolExecuteUnknownAction tests handling of unknown actions
func TestCronToolExecuteUnknownAction(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{Gateway: &fullMockGatewayService{}})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "unknown_action",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action")
}

// TestCronToolScheduleJob tests the schedule action
func TestCronToolScheduleJob(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "test command",
			"schedule": "* * * * *",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("missing command", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: &fullMockGatewayService{}})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"schedule": "* * * * *",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "command parameter is required")
	})

	t.Run("missing schedule and delayMinutes", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: &fullMockGatewayService{}})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":  "schedule",
			"command": "test command",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "either schedule or delayMinutes parameter is required")
	})

	t.Run("invalid jobType", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: &fullMockGatewayService{}})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "test command",
			"schedule": "* * * * *",
			"jobType":  "invalid",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "jobType must be 'go' or 'system'")
	})

	t.Run("successful schedule with cron expression", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "Check system status",
			"schedule": "0 9 * * *",
			"name":     "Daily check",
			"jobType":  "go",
			"model":    "haiku",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Job scheduled")

		// Verify data
		assert.NotEmpty(t, result.Data["jobId"])
		assert.Equal(t, "Daily check", result.Data["name"])
		assert.Equal(t, "0 9 * * *", result.Data["schedule"])
		assert.Equal(t, "go", result.Data["type"])
		assert.Equal(t, false, result.Data["oneshot"])
	})

	t.Run("successful schedule with delayMinutes", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":       "schedule",
			"command":      "Remind me to check email",
			"delayMinutes": 30,
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Reminder set")

		// Verify oneshot is true for delay-based scheduling
		assert.Equal(t, true, result.Data["oneshot"])
		assert.Contains(t, result.Data["name"], "Reminder in 30 minutes")
	})

	t.Run("schedule with skills", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "Check solar production",
			"schedule": "*/30 * * * *",
			"skills":   []interface{}{"solar", "weather"},
		})

		require.NoError(t, err)
		assert.True(t, result.Success)

		// Verify job was added with skills
		assert.Len(t, mockGw.jobs, 1)
		assert.Equal(t, []string{"solar", "weather"}, mockGw.jobs[0].Skills)
	})

	t.Run("system job type", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "/usr/local/bin/backup.sh",
			"schedule": "0 2 * * *",
			"jobType":  "system",
			"name":     "Nightly backup",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Equal(t, "system", result.Data["type"])
	})

	t.Run("schedule job with context user ID", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := types.WithRequestContext(context.Background(), "channel1", "user123", "session1")

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "Test task",
			"schedule": "* * * * *",
			"jobType":  "go",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)

		// Verify target was set from context
		assert.Len(t, mockGw.jobs, 1)
		assert.Equal(t, "user123", mockGw.jobs[0].Target)
	})

	t.Run("schedule job failure", func(t *testing.T) {
		mockGw := &fullMockGatewayService{scheduleErr: errors.New("scheduler error")}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action":   "schedule",
			"command":  "Test task",
			"schedule": "* * * * *",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "failed to schedule job")
	})
}

// TestCronToolListJobs tests the list action
func TestCronToolListJobs(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "list",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("no jobs", func(t *testing.T) {
		mockGw := &fullMockGatewayService{mockGatewayService: mockGatewayService{jobs: []types.SchedulerJob{}}}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "list",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "No scheduled jobs")
		assert.Equal(t, 0, result.Data["count"])
	})

	t.Run("multiple jobs", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{
					{
						ID:       "job1",
						Name:     "First job",
						Schedule: "* * * * *",
						Type:     "go",
						Enabled:  true,
					},
					{
						ID:       "job2",
						Name:     "Second job",
						Schedule: "0 9 * * *",
						Type:     "system",
						Enabled:  false,
					},
					{
						ID:       "job3",
						Name:     "",
						Schedule: "*/5 * * * *",
						Type:     "go",
						Enabled:  true,
						OneShot:  true,
						Skills:   []string{"solar"},
					},
				},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "list",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "3 scheduled job(s)")
		assert.Contains(t, result.Content, "First job")
		assert.Contains(t, result.Content, "Second job")
		assert.Contains(t, result.Content, "[system]")
		assert.Contains(t, result.Content, "(disabled)")
		assert.Contains(t, result.Content, "Unnamed")
		assert.Contains(t, result.Content, "(runs once)")
		assert.Contains(t, result.Content, "skills: solar")
		assert.Equal(t, 3, result.Data["count"])
	})
}

// TestCronToolCancelJob tests the cancel action
func TestCronToolCancelJob(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "cancel",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("missing jobId", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "cancel",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "jobId parameter is required")
	})

	t.Run("successful cancel", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{{ID: "job1", Name: "Test job"}},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "cancel",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Job job1 cancelled")
		assert.Equal(t, "job1", result.Data["jobId"])
	})

	t.Run("cancel failure", func(t *testing.T) {
		mockGw := &fullMockGatewayService{cancelErr: errors.New("cancel failed")}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "cancel",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "failed to cancel job")
	})
}

// TestCronToolRunJob tests the run action
func TestCronToolRunJob(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "run",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("missing jobId", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "run",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "jobId parameter is required")
	})

	t.Run("successful run", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{{ID: "job1", Name: "Test job"}},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "run",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Job job1 triggered")
		assert.Equal(t, "job1", result.Data["jobId"])
	})

	t.Run("run failure", func(t *testing.T) {
		mockGw := &fullMockGatewayService{runErr: errors.New("run failed")}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "run",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "failed to run job")
	})
}

// TestCronToolEnableJob tests the enable action
func TestCronToolEnableJob(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "enable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("missing jobId", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "enable",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "jobId parameter is required")
	})

	t.Run("successful enable", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{{ID: "job1", Name: "Test job", Enabled: false}},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "enable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Job job1 enabled")
		assert.Equal(t, "job1", result.Data["jobId"])
	})

	t.Run("enable failure", func(t *testing.T) {
		mockGw := &fullMockGatewayService{enableErr: errors.New("enable failed")}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "enable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "failed to enable job")
	})
}

// TestCronToolDisableJob tests the disable action
func TestCronToolDisableJob(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "disable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("missing jobId", func(t *testing.T) {
		mockGw := &fullMockGatewayService{}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "disable",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "jobId parameter is required")
	})

	t.Run("successful disable", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{{ID: "job1", Name: "Test job", Enabled: true}},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "disable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Job job1 disabled")
		assert.Equal(t, "job1", result.Data["jobId"])
	})

	t.Run("disable failure", func(t *testing.T) {
		mockGw := &fullMockGatewayService{disableErr: errors.New("disable failed")}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "disable",
			"jobId":  "job1",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "failed to disable job")
	})
}

// TestCronToolGetStatus tests the status action
func TestCronToolGetStatus(t *testing.T) {
	t.Run("missing gateway service", func(t *testing.T) {
		tool := NewCronTool(&types.ToolServices{Gateway: nil})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "status",
		})

		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, "gateway service not available")
	})

	t.Run("successful status", func(t *testing.T) {
		mockGw := &fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{
					{ID: "job1", Type: "go"},
					{ID: "job2", Type: "system"},
				},
			},
		}
		tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
		ctx := context.Background()

		result, err := tool.Execute(ctx, map[string]interface{}{
			"action": "status",
		})

		require.NoError(t, err)
		assert.True(t, result.Success)
		assert.Contains(t, result.Content, "Scheduler Status")
		assert.Contains(t, result.Content, "Enabled:")
		assert.Contains(t, result.Content, "Total Jobs:")
		assert.Contains(t, result.Content, "Go Jobs:")
		assert.Contains(t, result.Content, "System Jobs:")
		assert.Contains(t, result.Content, "Active Cron Entries:")

		// Verify data
		assert.Equal(t, true, result.Data["enabled"])
		assert.Equal(t, 2, result.Data["total_jobs"])
	})
}

// TestTruncate tests the truncate helper function
func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "long string truncated",
			input:    "hello world this is a long string",
			maxLen:   10,
			expected: "hello w...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   5,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := truncate(tc.input, tc.maxLen)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestHeartbeatJobDetectionAgentPrefix tests agent_heartbeat ID prefix detection
func TestHeartbeatJobDetectionAgentPrefix(t *testing.T) {
	job := &types.SchedulerJob{
		ID:      "agent_heartbeat_check",
		Name:    "Agent check",
		Command: "Verify agent status",
		Enabled: true,
	}

	assert.True(t, isHeartbeatJob(job), "Jobs with agent_heartbeat prefix should be detected")
}

// TestCronToolHeartbeatListNoJobs tests heartbeat_list when no heartbeat jobs exist
func TestCronToolHeartbeatListNoJobs(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{ID: "regular_job", Name: "Regular task", Command: "Do something"},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_list",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No heartbeat jobs found")
	assert.Equal(t, 0, result.Data["count"])
}

// TestCronToolHeartbeatListMissingGateway tests heartbeat_list without gateway
func TestCronToolHeartbeatListMissingGateway(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{Gateway: nil})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_list",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "gateway service not available")
}

// TestCronToolHeartbeatEnableMissingGateway tests heartbeat_enable without gateway
func TestCronToolHeartbeatEnableMissingGateway(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{Gateway: nil})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_enable",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "gateway service not available")
}

// TestCronToolHeartbeatDisableMissingGateway tests heartbeat_disable without gateway
func TestCronToolHeartbeatDisableMissingGateway(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{Gateway: nil})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_disable",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "gateway service not available")
}

// TestCronToolHeartbeatStatusMissingGateway tests heartbeat_status without gateway
func TestCronToolHeartbeatStatusMissingGateway(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{Gateway: nil})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_status",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "gateway service not available")
}

// TestCronToolHeartbeatEnableAlreadyEnabled tests enabling already enabled jobs
func TestCronToolHeartbeatEnableAlreadyEnabled(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{ID: "heartbeat_main", Name: "Main heartbeat", Enabled: true},
				{ID: "heartbeat_backup", Name: "Backup heartbeat", Enabled: true},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_enable",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No heartbeat jobs needed enabling")
	assert.Equal(t, 0, result.Data["enabled_count"])
}

// TestCronToolHeartbeatDisableAlreadyDisabled tests disabling already disabled jobs
func TestCronToolHeartbeatDisableAlreadyDisabled(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{ID: "heartbeat_main", Name: "Main heartbeat", Enabled: false},
				{ID: "heartbeat_backup", Name: "Backup heartbeat", Enabled: false},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_disable",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No heartbeat jobs needed disabling")
	assert.Equal(t, 0, result.Data["disabled_count"])
}

// TestCronToolHeartbeatStatusUnhealthy tests status when no jobs are enabled
func TestCronToolHeartbeatStatusUnhealthy(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{ID: "heartbeat_main", Name: "Main heartbeat", Enabled: false},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_status",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 0, result.Data["enabled_jobs"])
	assert.Equal(t, false, result.Data["healthy"])
	assert.Contains(t, result.Content, "No Active Jobs")
}

// TestCronToolHeartbeatStatusNoJobs tests status when no heartbeat jobs exist
func TestCronToolHeartbeatStatusNoJobs(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{ID: "regular_job", Name: "Regular task"},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_status",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 0, result.Data["total_jobs"])
	assert.Equal(t, false, result.Data["healthy"])
}

// TestCronToolHeartbeatListWithTarget tests heartbeat_list showing targets
func TestCronToolHeartbeatListWithTarget(t *testing.T) {
	mockGw := &fullMockGatewayService{
		mockGatewayService: mockGatewayService{
			jobs: []types.SchedulerJob{
				{
					ID:       "heartbeat_main",
					Name:     "Main heartbeat",
					Schedule: "*/5 * * * *",
					Target:   "user123",
					Enabled:  true,
				},
			},
		},
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_list",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Target: user123")
}

// TestCronToolHeartbeatEnableWithErrors tests enable when some jobs fail
type errorOnEnableMockGatewayService struct {
	fullMockGatewayService
	failJobID string
}

func (m *errorOnEnableMockGatewayService) EnableJob(jobID string) error {
	if jobID == m.failJobID {
		return errors.New("enable error for specific job")
	}
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = true
			return nil
		}
	}
	return nil
}

func TestCronToolHeartbeatEnableWithErrors(t *testing.T) {
	mockGw := &errorOnEnableMockGatewayService{
		fullMockGatewayService: fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{
					{ID: "heartbeat_main", Name: "Main heartbeat", Enabled: false},
					{ID: "heartbeat_backup", Name: "Backup heartbeat", Enabled: false},
				},
			},
		},
		failJobID: "heartbeat_backup",
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_enable",
	})

	require.NoError(t, err)
	assert.False(t, result.Success) // Should fail because there were errors
	assert.Equal(t, 1, result.Data["enabled_count"])
	errors := result.Data["errors"].([]string)
	assert.Len(t, errors, 1)
	assert.Contains(t, errors[0], "heartbeat_backup")
}

// errorOnDisableMockGatewayService for testing disable errors
type errorOnDisableMockGatewayService struct {
	fullMockGatewayService
	failJobID string
}

func (m *errorOnDisableMockGatewayService) DisableJob(jobID string) error {
	if jobID == m.failJobID {
		return errors.New("disable error for specific job")
	}
	for i := range m.jobs {
		if m.jobs[i].ID == jobID {
			m.jobs[i].Enabled = false
			return nil
		}
	}
	return nil
}

func TestCronToolHeartbeatDisableWithErrors(t *testing.T) {
	mockGw := &errorOnDisableMockGatewayService{
		fullMockGatewayService: fullMockGatewayService{
			mockGatewayService: mockGatewayService{
				jobs: []types.SchedulerJob{
					{ID: "heartbeat_main", Name: "Main heartbeat", Enabled: true},
					{ID: "heartbeat_backup", Name: "Backup heartbeat", Enabled: true},
				},
			},
		},
		failJobID: "heartbeat_main",
	}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "heartbeat_disable",
	})

	require.NoError(t, err)
	assert.False(t, result.Success) // Should fail because there were errors
	assert.Equal(t, 1, result.Data["disabled_count"])
	errors := result.Data["errors"].([]string)
	assert.Len(t, errors, 1)
	assert.Contains(t, errors[0], "heartbeat_main")
}
