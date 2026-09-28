package heartbeat

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/channels"
	"conduit/internal/scheduler"
)

// conduit-2six: notices for the owner that do not come out of a heartbeat
// run (the scheduler's consecutive-failure / recovered notices) reuse the
// heartbeat delivery path: DeliveryRegistry for live sends (alert_history
// audit, circuit breaker, background retries per alert_retry_policy) and the
// deferred.json queue during quiet hours, flushed like quiet-aware heartbeat
// actions.

// OwnerNotice is one message for the owner's alert target.
type OwnerNotice struct {
	// Target is "channel:user" (e.g. "telegram:12345") or a bare Telegram
	// chat ID.
	Target  string
	Message string
	// Type and Source classify the alert_history row (alert_type, source).
	Type   string
	Source string
	// Severity: critical is delivered immediately even in quiet hours;
	// warning and info are deferred until quiet hours end.
	Severity AlertSeverity
}

// NotifyOwner delivers n, or defers it when it is not critical and the
// configured quiet hours are in effect. deferred reports which happened.
func (g *GatewayIntegration) NotifyOwner(ctx context.Context, n OwnerNotice) (deferred bool, err error) {
	if strings.TrimSpace(n.Target) == "" {
		return false, fmt.Errorf("no alert target configured")
	}
	if n.Message == "" {
		return false, nil
	}
	if n.Severity == "" {
		n.Severity = AlertSeverityInfo
	}
	if n.Source == "" {
		n.Source = "gateway"
	}
	if n.Type == "" {
		n.Type = "owner_notice"
	}

	if n.Severity != AlertSeverityCritical && g.quietConfig().IsQuietTime(g.clock()) {
		prio := TaskPriorityNormal
		if n.Severity == AlertSeverityWarning {
			prio = TaskPriorityHigh // audited as "warning" when flushed
		}
		action := HeartbeatAction{
			Type:     ActionTypeDelivery,
			Target:   n.Target,
			Content:  n.Message,
			Priority: prio,
			Metadata: map[string]interface{}{"quiet_aware": true, "notice_type": n.Type},
		}
		if err := g.deferAction(action, &scheduler.Job{ID: n.Source, Target: n.Target}, 0); err != nil {
			return false, fmt.Errorf("defer notice: %w", err)
		}
		return true, nil
	}

	msg := channels.SanitizeOutgoingText(n.Message)
	meta := alertMeta{Type: n.Type, Severity: n.Severity, Source: n.Source}
	return false, g.dispatch(ctx, n.Target, msg, meta, deliverWithRetry)
}
