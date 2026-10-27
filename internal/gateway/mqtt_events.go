package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/mqttevents"
)

// mqttEventAlertSource is the Source of every alert the MQTT event engine
// queues; the rule ID is the Component.
const mqttEventAlertSource = "mqtt-events"

// newMQTTEventsEngine builds the MQTT event rule engine (bd-39mx) from the
// mqtt config. It returns (nil, nil) when MQTT or its events block is off.
// The caller registers engine.HandleEvent as an mqtt.Service observer and
// owns Start/Stop.
func newMQTTEventsEngine(cfg config.MQTTConfig, sp mqttevents.Spawner, q mqttevents.Queuer, h mqttevents.HistorySource) (*mqttevents.Engine, error) {
	if !cfg.Enabled || cfg.Events == nil || !cfg.Events.Enabled {
		return nil, nil
	}
	eng, err := mqttevents.NewEngine(*cfg.Events, mqttevents.Deps{Spawner: sp, Queuer: q, History: h})
	if err != nil {
		return nil, fmt.Errorf("mqtt events: %w", err)
	}
	return eng, nil
}

// mqttEventAlertQueuePath is the shared alert queue (pending.json) that
// queued events land in: agent_heartbeat.alert_queue_path when set (relative
// to the workspace), else <workspace>/memory/alerts/pending.json — the queue
// alert-flush.sh and the HEARTBEAT.md prompt sweep.
func mqttEventAlertQueuePath(workspaceDir string, hb config.AgentHeartbeatConfig) string {
	p := hb.AlertQueuePath
	if p == "" {
		p = filepath.Join("memory", "alerts", "pending.json")
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workspaceDir, p)
}

// mqttEventSpawnRouting maps a spawn priority to the sub-agent's result
// routing. Only high-priority spawns announce, and only to the owner's
// "<channel>:<user>" alert target; medium/low (or no owner target) run
// silently and are surfaced through the alert queue instead.
func mqttEventSpawnRouting(priority, ownerTarget string) (channelID, userID string, announce bool) {
	if priority != "high" {
		return "", "", false
	}
	ch, user, ok := strings.Cut(ownerTarget, ":")
	if !ok || ch == "" || user == "" {
		return "", "", false
	}
	return ch, user, true
}

// mqttEventSpawner starts event sub-agents on the gateway.
type mqttEventSpawner struct {
	g           *Gateway
	ownerTarget string
}

func (s *mqttEventSpawner) Spawn(ctx context.Context, req mqttevents.SpawnRequest, onDone func()) (string, error) {
	ch, user, announce := mqttEventSpawnRouting(req.Priority, s.ownerTarget)
	return s.g.spawnSubAgent(ctx, req.Task, "", req.Model, req.Label, req.TimeoutS, ch, user, announce, req.Skills, onDone)
}

// mqttEventQueuer writes notices to the heartbeat's shared alert queue.
type mqttEventQueuer struct {
	q   *heartbeat.SharedAlertQueue
	seq atomic.Uint64
}

func (m *mqttEventQueuer) Enqueue(n mqttevents.Notice) error {
	ts := n.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	alert := heartbeat.Alert{
		ID:        fmt.Sprintf("mqtt-%s-%d-%d", n.RuleID, ts.UnixNano(), m.seq.Add(1)),
		Source:    mqttEventAlertSource,
		Component: n.RuleID,
		Type:      "mqtt_event",
		Title:     fmt.Sprintf("MQTT event: rule %s on %s", n.RuleID, n.Topic),
		Message:   n.Message,
		Severity:  mqttEventSeverity(n.Priority),
		Category:  "home",
		Tags:      []string{"mqtt", n.Topic},
		CreatedAt: ts,
		Status:    heartbeat.AlertStatusPending,
	}
	if n.Downgraded != "" {
		alert.Details = "downgraded from spawn: " + n.Downgraded
	}
	return m.q.AddAlert(alert)
}

// mqttEventSeverity maps rule priority to alert severity (design §5):
// high → critical, medium → warning, anything else → info.
func mqttEventSeverity(priority string) heartbeat.AlertSeverity {
	switch priority {
	case "high":
		return heartbeat.AlertSeverityCritical
	case "medium":
		return heartbeat.AlertSeverityWarning
	}
	return heartbeat.AlertSeverityInfo
}

// initMQTTEvents wires the event engine onto the MQTT service. Rule configs
// are validated at load, so a build error here only disables the engine.
func (g *Gateway) initMQTTEvents(cfg *config.Config, workspaceDir string) {
	if g.mqttService == nil {
		return
	}
	queuePath := mqttEventAlertQueuePath(workspaceDir, cfg.AgentHeartbeat)
	eng, err := newMQTTEventsEngine(cfg.MQTT,
		&mqttEventSpawner{g: g, ownerTarget: ownerAlertTarget(cfg.AgentHeartbeat)},
		&mqttEventQueuer{q: heartbeat.NewSharedAlertQueue(queuePath)},
		g.mqttService)
	if err != nil {
		g.logger.Error("MQTT event rules disabled", "error", err)
		return
	}
	if eng == nil {
		return
	}
	g.mqttEvents = eng
	g.mqttService.OnEvent(eng.HandleEvent)
	g.logger.Info("MQTT event rules configured", "rules", len(cfg.MQTT.Events.Rules), "alert_queue", queuePath)
}
