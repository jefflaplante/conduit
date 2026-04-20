package gateway

import (
	"context"
	"testing"

	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/scheduler"
	toolstypes "conduit/internal/tools/types"
)

// Tests in this file reuse mockScheduler from heartbeat_integration_test.go.

// ---- scheduler_ops tests ----

func TestScheduleJob_NilScheduler(t *testing.T) {
	gw := &Gateway{}
	err := gw.ScheduleJob(&toolstypes.SchedulerJob{ID: "j1"})
	if err == nil {
		t.Error("expected error when scheduler is nil")
	}
}

func TestScheduleJob_Success(t *testing.T) {
	m := newMockScheduler()
	gw := &Gateway{scheduler: m}

	job := &toolstypes.SchedulerJob{
		ID:       "job1",
		Name:     "Test Job",
		Schedule: "*/5 * * * *",
		Type:     "shell",
		Command:  "echo hi",
		Model:    "haiku",
		Target:   "tui",
		Enabled:  true,
		OneShot:  false,
		Skills:   []string{"coding"},
	}
	if err := gw.ScheduleJob(job); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if _, ok := m.jobs["job1"]; !ok {
		t.Errorf("job not added properly: %+v", m.jobs)
	}
}

func TestCancelJob_NilScheduler(t *testing.T) {
	gw := &Gateway{}
	if err := gw.CancelJob("j1"); err == nil {
		t.Error("expected error when scheduler is nil")
	}
}

func TestCancelJob_Success(t *testing.T) {
	m := newMockScheduler()
	m.jobs["j1"] = &scheduler.Job{ID: "j1"}
	gw := &Gateway{scheduler: m}
	if err := gw.CancelJob("j1"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if _, ok := m.jobs["j1"]; ok {
		t.Error("expected job j1 removed")
	}
}

func TestListJobs_NilScheduler(t *testing.T) {
	gw := &Gateway{}
	if jobs := gw.ListJobs(); jobs != nil {
		t.Errorf("expected nil jobs, got %v", jobs)
	}
}

func TestListJobs_Success(t *testing.T) {
	m := newMockScheduler()
	m.jobs["j1"] = &scheduler.Job{ID: "j1", Name: "Job 1", Schedule: "0 * * * *", Type: scheduler.JobTypeGo, Command: "cmd", Enabled: true, Skills: []string{"skill1"}}
	gw := &Gateway{scheduler: m}
	jobs := gw.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].ID != "j1" || jobs[0].Name != "Job 1" {
		t.Errorf("unexpected job: %+v", jobs[0])
	}
	if !jobs[0].Enabled {
		t.Errorf("expected j1 enabled")
	}
}

func TestEnableDisableJob_NilScheduler(t *testing.T) {
	gw := &Gateway{}
	if err := gw.EnableJob("j1"); err == nil {
		t.Error("EnableJob: expected error")
	}
	if err := gw.DisableJob("j1"); err == nil {
		t.Error("DisableJob: expected error")
	}
}

func TestEnableDisableJob_Success(t *testing.T) {
	m := newMockScheduler()
	m.jobs["j1"] = &scheduler.Job{ID: "j1", Enabled: false}
	gw := &Gateway{scheduler: m}
	if err := gw.EnableJob("j1"); err != nil {
		t.Errorf("EnableJob: %v", err)
	}
	if !m.jobs["j1"].Enabled {
		t.Error("expected j1 enabled after EnableJob")
	}
	if err := gw.DisableJob("j1"); err != nil {
		t.Errorf("DisableJob: %v", err)
	}
	if m.jobs["j1"].Enabled {
		t.Error("expected j1 disabled after DisableJob")
	}
}

func TestRunJobNow(t *testing.T) {
	// Nil scheduler
	gw := &Gateway{}
	if err := gw.RunJobNow("j1"); err == nil {
		t.Error("expected error for nil scheduler")
	}

	m := newMockScheduler()
	gw = &Gateway{scheduler: m}
	if err := gw.RunJobNow("j1"); err != nil {
		t.Errorf("RunJobNow: %v", err)
	}
}

func TestGetSchedulerStatus_NilScheduler(t *testing.T) {
	gw := &Gateway{}
	st := gw.GetSchedulerStatus()
	if st["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", st["enabled"])
	}
}

func TestGetSchedulerStatus_WithScheduler(t *testing.T) {
	m := newMockScheduler()
	m.jobs["j1"] = &scheduler.Job{ID: "j1"}
	gw := &Gateway{scheduler: m}
	st := gw.GetSchedulerStatus()
	if st["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", st["enabled"])
	}
	if st["total_jobs"] != 1 {
		t.Errorf("expected total_jobs=1, got %v", st["total_jobs"])
	}
}

func TestScheduleHeartbeatJob_NilIntegration(t *testing.T) {
	gw := &Gateway{}
	if err := gw.ScheduleHeartbeatJob("* * * * *", "telegram", "haiku", true); err == nil {
		t.Error("expected error when heartbeat integration is nil")
	}
}

func TestGetHeartbeatJobCount_NilIntegration(t *testing.T) {
	gw := &Gateway{}
	if n := gw.GetHeartbeatJobCount(); n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestRemoveHeartbeatJobs_NilIntegration(t *testing.T) {
	gw := &Gateway{}
	if err := gw.RemoveHeartbeatJobs(); err == nil {
		t.Error("expected error when heartbeat integration is nil")
	}
}

// ---- channel_ops tests ----

func TestSendMessage_EmptyManager(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	ctx := context.Background()
	err := gw.SendMessage(ctx, "tui_test", "user1", "hello", map[string]string{"k": "v"})
	if err != nil {
		t.Errorf("SendMessage: %v", err)
	}
}

func TestGetChannelStatusMap_NilManager(t *testing.T) {
	gw := &Gateway{}
	m := gw.GetChannelStatusMap()
	if m == nil || len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestGetChannelStatusMap_WithManager(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	m := gw.GetChannelStatusMap()
	if m == nil {
		t.Error("expected non-nil map")
	}
}

func TestGetAvailableTargets_NilManager(t *testing.T) {
	gw := &Gateway{}
	targets := gw.GetAvailableTargets()
	if len(targets) != 1 || targets[0] != "No channels configured" {
		t.Errorf("expected placeholder, got %v", targets)
	}
}

func TestGetAvailableTargets_WithManager(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	targets := gw.GetAvailableTargets()
	if targets == nil {
		t.Error("expected non-nil targets")
	}
}

// ---- status_ops tests ----

func TestGetSessionStatus_Success(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	sess, _ := store.GetOrCreateSession("user1", "ch1")

	status, err := gw.GetSessionStatus(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("GetSessionStatus: %v", err)
	}
	if status["session_key"] != sess.Key {
		t.Errorf("expected session_key=%s, got %v", sess.Key, status["session_key"])
	}
	if status["user_id"] != "user1" {
		t.Errorf("expected user_id=user1, got %v", status["user_id"])
	}
}

func TestGetSessionStatus_NotFound(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	_, err := gw.GetSessionStatus(context.Background(), "no-such-session")
	if err == nil {
		t.Error("expected error for missing session")
	}
}

func TestGetGatewayStatus(t *testing.T) {
	gw := &Gateway{}
	status, err := gw.GetGatewayStatus()
	if err != nil {
		t.Fatalf("GetGatewayStatus: %v", err)
	}
	if status["status"] != "running" {
		t.Errorf("expected running, got %v", status["status"])
	}
}

func TestRestartGateway_NoShutdown(t *testing.T) {
	gw := &Gateway{}
	err := gw.RestartGateway(context.Background())
	if err == nil {
		t.Error("expected error when shutdownMgr is nil")
	}
}

func TestGetChannelStatus(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	status, err := gw.GetChannelStatus()
	if err != nil {
		t.Fatalf("GetChannelStatus: %v", err)
	}
	if status == nil {
		t.Error("expected non-nil status")
	}
}

func TestEnableDisableChannel_NotImplemented(t *testing.T) {
	gw := &Gateway{}
	if err := gw.EnableChannel(context.Background(), "ch1"); err == nil {
		t.Error("expected 'not implemented' error")
	}
	if err := gw.DisableChannel(context.Background(), "ch1"); err == nil {
		t.Error("expected 'not implemented' error")
	}
}

func TestGetConfiguration(t *testing.T) {
	gw := &Gateway{config: &config.Config{AI: config.AIConfig{DefaultProvider: "anthropic"}}}
	cfg, err := gw.GetConfiguration()
	if err != nil {
		t.Fatalf("GetConfiguration: %v", err)
	}
	if _, ok := cfg["ai"]; !ok {
		t.Error("expected ai key")
	}
	if _, ok := cfg["workspace"]; !ok {
		t.Error("expected workspace key")
	}
}

func TestUpdateConfiguration_NotImplemented(t *testing.T) {
	gw := &Gateway{}
	if err := gw.UpdateConfiguration(context.Background(), map[string]interface{}{}); err == nil {
		t.Error("expected 'not implemented' error")
	}
}

func TestGetMetrics(t *testing.T) {
	gw := &Gateway{}
	m, err := gw.GetMetrics()
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}
	if m["uptime"] == nil {
		t.Error("expected uptime key")
	}
}

func TestGetVersion(t *testing.T) {
	gw := &Gateway{}
	v := gw.GetVersion()
	if v == "" {
		t.Error("expected non-empty version")
	}
}

// ensure unused imports don't fail
var _ = channels.NewManager
