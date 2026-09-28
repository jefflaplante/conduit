package gateway

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/scheduler"
)

// conduit-2six: scheduler job-failure observability wiring. The scheduler
// cannot import the gateway or heartbeat (heartbeat imports scheduler), so
// the gateway injects a scheduler.FailureNotifier that routes job-health
// notices to the owner's alert target through the heartbeat delivery path
// (DeliveryRegistry → alert_history; quiet-hours deferral).

// cronFailureLogPath is the workspace-relative JSONL log the scheduler appends
// a status:"error" record to for every failed run. It is the same file cron
// job prompts write their own entries to.
var cronFailureLogPath = filepath.Join("memory", "cron-log.jsonl")

// ownerNotifier is the slice of heartbeat.GatewayIntegration used here.
type ownerNotifier interface {
	NotifyOwner(ctx context.Context, n heartbeat.OwnerNotice) (bool, error)
}

// jobHealthNotifier implements scheduler.FailureNotifier.
type jobHealthNotifier struct {
	// hb is set once during gateway construction, before the scheduler
	// starts (and so before any run can notify).
	hb     ownerNotifier
	target string
	loc    *time.Location
}

func newJobHealthNotifier(hbCfg config.AgentHeartbeatConfig) *jobHealthNotifier {
	return &jobHealthNotifier{target: ownerAlertTarget(hbCfg), loc: hbCfg.GetLocation()}
}

// schedulerHealthOptions returns the scheduler options that enable the
// failure log and the consecutive-failure notices.
func schedulerHealthOptions(workspaceDir string, hbCfg config.AgentHeartbeatConfig, n *jobHealthNotifier) []scheduler.Option {
	return []scheduler.Option{
		scheduler.WithFailureLog(filepath.Join(workspaceDir, cronFailureLogPath)),
		scheduler.WithFailureNotifier(n, hbCfg.JobFailureAlertThreshold),
	}
}

// NotifyJobHealth implements scheduler.FailureNotifier.
func (n *jobHealthNotifier) NotifyJobHealth(ctx context.Context, ev scheduler.JobHealthEvent) error {
	if n.hb == nil {
		return fmt.Errorf("heartbeat delivery not wired")
	}
	notice := heartbeat.OwnerNotice{
		Target:   n.target,
		Message:  ev.Message(n.loc),
		Type:     "cron_job_failing",
		Source:   "scheduler:" + ev.JobID,
		Severity: heartbeat.AlertSeverityWarning,
	}
	if ev.Kind == scheduler.JobHealthRecovered {
		notice.Type = "cron_job_recovered"
		notice.Severity = heartbeat.AlertSeverityInfo
	}
	deferred, err := n.hb.NotifyOwner(ctx, notice)
	if deferred {
		log.Printf("[Scheduler] Job %s %s notice deferred until quiet hours end", ev.JobID, ev.Kind)
	}
	return err
}

// ownerAlertTarget returns the owner's alert target: agent_heartbeat
// alert_targets[0] when it is a Telegram target with a chat_id, formatted
// "telegram:<chat_id>"; "" otherwise. Same rule the agent heartbeat job uses.
func ownerAlertTarget(hb config.AgentHeartbeatConfig) string {
	if len(hb.AlertTargets) == 0 {
		return ""
	}
	first := hb.AlertTargets[0]
	if first.Type != "telegram" {
		return ""
	}
	if chatID, ok := first.Config["chat_id"]; ok && chatID != "" {
		return "telegram:" + chatID
	}
	return ""
}

// jobHealthSource is the optional scheduler capability used for visibility.
type jobHealthSource interface {
	JobHealth() map[string]scheduler.JobHealthStatus
}

// schedulerJobHealth returns per-job failure state, or nil when the
// scheduler does not expose it.
func (g *Gateway) schedulerJobHealth() map[string]scheduler.JobHealthStatus {
	if g.scheduler == nil {
		return nil
	}
	if hs, ok := g.scheduler.(jobHealthSource); ok {
		return hs.JobHealth()
	}
	return nil
}

// failingJobsSummary is a one-line list of jobs with a failure streak, e.g.
// "wildlife_daily (3), rem_sleep_nightly (1)"; "" when none are failing.
func (g *Gateway) failingJobsSummary() string {
	health := g.schedulerJobHealth()
	ids := make([]string, 0, len(health))
	for id, h := range health {
		if h.ConsecutiveFailures > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := health[ids[i]].ConsecutiveFailures, health[ids[j]].ConsecutiveFailures
		if a != b {
			return a > b
		}
		return ids[i] < ids[j]
	})
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (%d)", id, health[id].ConsecutiveFailures)
	}
	return strings.Join(parts, ", ")
}
