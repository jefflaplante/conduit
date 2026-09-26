package gateway

import (
	"context"

	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// turns returns the gateway's shared TurnRunner (conduit-31jg.35), built on
// first use so struct-literal gateways in tests get one too. Its running-turn
// registry is g.ws.ActiveRequests, which /stop and the shutdown drain read.
func (g *Gateway) turns() *TurnRunner {
	g.turnRunnerOnce.Do(func() {
		var compactor turnCompactor
		if g.compactionEngine != nil {
			compactor = g.compactionEngine
		}
		g.turnRunner = NewTurnRunner(g.sessions, g.ai, compactor, gatewayTurnHooks{g},
			activeTurnRegistry{
				mu:  &g.ws.ActiveRequestsMu,
				get: func() map[string]context.CancelFunc { return g.ws.ActiveRequests },
			},
			func() monitoring.MetricsCollectorInterface {
				if g.monitoring == nil {
					return nil
				}
				return g.monitoring.MetricsCollector
			},
			g.logger)
		g.turnRunner.draining = func() bool { return g.shutdownMgr != nil && g.shutdownMgr.IsDraining() }
	})
	return g.turnRunner
}

// gatewayTurnHooks exposes the gateway's SPAR reflection wiring to the runner.
type gatewayTurnHooks struct{ g *Gateway }

func (h gatewayTurnHooks) IsFarewell(text string) bool {
	ok, _ := h.g.shouldTriggerReflection(text)
	return ok
}

func (h gatewayTurnHooks) ReflectionPrompt() string { return h.g.reflectHighConfidencePre() }

func (h gatewayTurnHooks) ReflectionEnabled() bool { return h.g.sessionReflector != nil }

func (h gatewayTurnHooks) AfterReflection(ctx context.Context, s *sessions.Session) {
	h.g.reflectHighConfidencePost(ctx, s)
}
