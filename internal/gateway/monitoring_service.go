package gateway

import (
	"context"
	"database/sql"
	"log/slog"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
	"conduit/internal/version"
)

// MonitoringService owns the metrics, heartbeat, event, and token-window
// subsystems that were previously inlined into the Gateway struct. It centralises
// construction and lifecycle for the observability bits so Gateway can stay
// focused on request routing and channel orchestration.
//
// Fields are exported so sibling files in the gateway package (and tests) can
// continue to touch the underlying subsystems directly. Cross-package
// consumers should go through *Gateway, which exposes the relevant operations
// via the types.GatewayService interface (e.g., GetFuelGaugeMap).
type MonitoringService struct {
	// GatewayMetrics is the snapshot-style Prometheus-flavoured counter set
	// surfaced via /metrics and /prometheus.
	GatewayMetrics *monitoring.GatewayMetrics

	// MetricsCollector bundles session-store-backed metrics collection and
	// activity tracking used by the heartbeat service.
	MetricsCollector monitoring.MetricsCollectorInterface

	// HeartbeatService runs the background heartbeat goroutine that emits
	// periodic diagnostic events.
	HeartbeatService *monitoring.HeartbeatService

	// HeartbeatIntegration ties the scheduler to the HEARTBEAT.md execution
	// framework. Wired in after construction via WireHeartbeatIntegration.
	HeartbeatIntegration heartbeat.HeartbeatIntegrationInterface

	// EventStore persists heartbeat/diagnostic events for the /diagnostics
	// endpoint.
	EventStore monitoring.EventStore

	// TokenWindow is the rolling hour/day AI token usage tracker that feeds
	// the fuel gauge.
	TokenWindow *monitoring.TokenWindowTracker

	// DeliveryRegistry routes alert deliveries through registered deliverers
	// and persists every attempt via the alert auditor. Wired in after
	// construction via WireDeliveryRegistry.
	DeliveryRegistry *heartbeat.DeliveryRegistry
}

// NewMonitoringService constructs the monitoring subsystem. It wires the
// rolling-window token tracker as an observer on the AI router's usage tracker
// and builds a heartbeat service when cfg.Heartbeat.Enabled is true.
//
// The heartbeat integration (which needs a scheduler + channel sender not
// yet available at monitoring construction time) and the alert delivery
// registry are attached separately via WireHeartbeatIntegration and
// WireDeliveryRegistry once those dependencies exist.
func NewMonitoringService(cfg *config.Config, logger *slog.Logger, sessionStore *sessions.Store, aiRouter *ai.Router) (*MonitoringService, error) {
	gatewayMetrics := monitoring.NewGatewayMetrics()
	gatewayMetrics.SetVersion(version.Info())

	// Create rolling-window token usage tracker for the fuel gauge and wire
	// it up as an observer on the AI router's usage tracker so every provider
	// response is shadowed into the hour/day window counters.
	tokenWindow := monitoring.NewTokenWindowTracker()
	if aiRouter != nil {
		if ut := aiRouter.GetUsageTracker(); ut != nil {
			ut.SetObserver(tokenWindow)
		}
	}

	// Create event store for heartbeat events
	eventStore := monitoring.NewMemoryEventStore(1000)

	// Create metrics collector
	metricsCollector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   sessionStore,
		GatewayMetrics: gatewayMetrics,
	})

	// Create heartbeat service (optional)
	var heartbeatService *monitoring.HeartbeatService
	if cfg.Heartbeat.Enabled {
		heartbeatService = monitoring.NewHeartbeatService(monitoring.HeartbeatDependencies{
			Config:     cfg.Heartbeat,
			Collector:  metricsCollector,
			EventStore: eventStore,
		})
		logger.Info("heartbeat service configured", "interval_seconds", cfg.Heartbeat.IntervalSeconds)
	} else {
		logger.Debug("heartbeat service disabled in configuration")
	}

	return &MonitoringService{
		GatewayMetrics:   gatewayMetrics,
		MetricsCollector: metricsCollector,
		HeartbeatService: heartbeatService,
		EventStore:       eventStore,
		TokenWindow:      tokenWindow,
	}, nil
}

// WireHeartbeatIntegration attaches the scheduler-backed heartbeat integration
// once scheduler and channel sender dependencies are available.
func (m *MonitoringService) WireHeartbeatIntegration(hbi heartbeat.HeartbeatIntegrationInterface) {
	m.HeartbeatIntegration = hbi
}

// WireDeliveryRegistry creates an alert delivery registry with an auditor
// backed by the given database. This is the audit-trail wiring from
// conduit-1rp3: every delivery attempt is persisted to alert_history.
func (m *MonitoringService) WireDeliveryRegistry(db *sql.DB) {
	m.DeliveryRegistry = heartbeat.NewDeliveryRegistry()
	m.DeliveryRegistry.SetAuditor(heartbeat.NewAlertAuditor(db))
}

// Start begins the heartbeat goroutine if a heartbeat service was configured.
// Safe to call when HeartbeatService is nil (no-op).
func (m *MonitoringService) Start(ctx context.Context) error {
	if m.HeartbeatService != nil {
		if err := m.HeartbeatService.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Stop gracefully halts the heartbeat service. Safe to call when
// HeartbeatService is nil (no-op).
func (m *MonitoringService) Stop() error {
	if m.HeartbeatService != nil {
		return m.HeartbeatService.Stop()
	}
	return nil
}
