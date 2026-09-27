package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"conduit/internal/config"
	"conduit/internal/scheduler"
)

// conduit-31jg.33: durable deferral of quiet-aware heartbeat actions.
//
// Previously a quiet-aware action that hit quiet hours was logged ("Could
// schedule for later execution here") and dropped. Now it is written to a
// file-backed SharedAlertQueue (atomic temp+rename writes, corrupt-file
// recovery) and delivered by FlushDeferred, which runs at the start of every
// heartbeat cycle. Delivery therefore happens on the first cycle after quiet
// hours end (at most interval_minutes late) and survives restarts.
//
// The file is deliberately separate from alert_queue_path (pending.json):
// that queue is shared with external scripts and the HEARTBEAT.md prompt, so
// its semantics are not ours to change.

const (
	deferredQueueFile = "deferred.json"
	deferredSource    = "heartbeat-deferred"
	deferredTTL       = 72 * time.Hour
	deferredMaxTries  = 5
	metaDeferredJob   = "job_id"
	metaDeliverAfter  = "deliver_after"
)

// SetAgentHeartbeatConfig installs the agent_heartbeat config used for quiet
// hours (timezone + window) and places the deferred-action queue next to the
// configured alert queue inside the workspace.
func (g *GatewayIntegration) SetAgentHeartbeatConfig(cfg config.AgentHeartbeatConfig) {
	g.deferMu.Lock()
	defer g.deferMu.Unlock()
	c := cfg
	g.hbCfg = &c
	g.deferred = NewSharedAlertQueue(deferredQueuePath(g.workspaceDir, cfg.AlertQueuePath))
}

func deferredQueuePath(workspaceDir, alertQueuePath string) string {
	dir := filepath.Join("memory", "alerts")
	if alertQueuePath != "" {
		dir = filepath.Dir(alertQueuePath)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workspaceDir, dir)
	}
	return filepath.Join(dir, deferredQueueFile)
}

// quietConfig returns the configured agent_heartbeat settings, or the
// package defaults when SetAgentHeartbeatConfig was never called.
func (g *GatewayIntegration) quietConfig() config.AgentHeartbeatConfig {
	g.deferMu.Lock()
	defer g.deferMu.Unlock()
	if g.hbCfg != nil {
		return *g.hbCfg
	}
	return config.DefaultAgentHeartbeatConfig()
}

func (g *GatewayIntegration) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// deferAction persists a quiet-aware action for delivery after quiet hours.
func (g *GatewayIntegration) deferAction(action HeartbeatAction, job *scheduler.Job, seq int) error {
	g.deferMu.Lock()
	q := g.deferred
	g.deferMu.Unlock()
	if q == nil {
		return fmt.Errorf("no deferred queue configured")
	}
	if action.Content == "" {
		return nil
	}

	now := g.clock()
	// Resolve the target now so later job edits cannot redirect it.
	action.Target = g.resolveTarget(action.Target, job.Target)
	payload, err := json.Marshal(action)
	if err != nil {
		return fmt.Errorf("marshal deferred action: %w", err)
	}
	expires := now.Add(deferredTTL)
	deliverAfter := g.quietConfig().NextQuietEnd(now)
	return q.AddAlert(Alert{
		ID:         fmt.Sprintf("deferred-%d-%d", now.UnixNano(), seq),
		Source:     deferredSource,
		Component:  job.ID,
		Type:       string(action.Type),
		Title:      "Deferred heartbeat " + string(action.Type),
		Message:    action.Content,
		Details:    string(payload),
		Severity:   AlertSeverityInfo,
		Status:     AlertStatusPending,
		MaxRetries: deferredMaxTries,
		CreatedAt:  now,
		ExpiresAt:  &expires,
		Targets:    []string{action.Target},
		Metadata: map[string]interface{}{
			metaDeferredJob:  job.ID,
			metaDeliverAfter: deliverAfter.Format(time.RFC3339),
		},
	})
}

// FlushDeferred delivers persisted deferred actions when outside quiet hours.
// It returns the number delivered. Failed deliveries stay queued (up to
// deferredMaxTries); expired or exhausted entries are pruned.
func (g *GatewayIntegration) FlushDeferred(ctx context.Context) (int, error) {
	g.flushMu.Lock()
	defer g.flushMu.Unlock()

	g.deferMu.Lock()
	q := g.deferred
	g.deferMu.Unlock()
	if q == nil {
		return 0, nil
	}
	if g.quietConfig().IsQuietTime(g.clock()) {
		return 0, nil
	}

	pending, err := q.GetPendingAlerts()
	if err != nil {
		return 0, fmt.Errorf("load deferred queue: %w", err)
	}

	delivered := 0
	for _, a := range pending {
		if a.Source != deferredSource {
			continue
		}
		var action HeartbeatAction
		if err := json.Unmarshal([]byte(a.Details), &action); err != nil {
			log.Printf("[HeartbeatIntegration] Dropping unreadable deferred action %s: %v", a.ID, err)
			_ = q.UpdateAlert(a.ID, func(x *Alert) { x.Status = AlertStatusFailed; x.LastError = err.Error() })
			continue
		}
		job := &scheduler.Job{ID: a.Component, Target: action.Target}
		// deliverOnce: the durable queue is the retry mechanism here
		// (conduit-31jg.59); background retries would risk duplicates.
		if err := g.executeAction(ctx, action, job, deliverOnce); err != nil {
			log.Printf("[HeartbeatIntegration] Deferred action %s delivery failed: %v", a.ID, err)
			_ = q.UpdateAlert(a.ID, func(x *Alert) {
				x.RecordFailedAttempt(err.Error(), g.clock())
				if x.RetryCount < x.MaxRetries {
					x.Status = AlertStatusPending // try again next cycle
				}
			})
			continue
		}
		if err := q.UpdateAlertStatus(a.ID, AlertStatusSent); err != nil {
			log.Printf("[HeartbeatIntegration] Deferred action %s delivered but not marked sent: %v", a.ID, err)
		}
		delivered++
		log.Printf("[HeartbeatIntegration] Delivered deferred action %s after quiet hours", a.ID)
	}

	if err := q.RemoveProcessedAlerts(); err != nil {
		log.Printf("[HeartbeatIntegration] Deferred queue cleanup failed: %v", err)
	}
	return delivered, nil
}
