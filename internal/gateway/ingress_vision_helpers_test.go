package gateway

import (
	"log/slog"
	"os"
	"testing"

	"github.com/gorilla/websocket"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// newGatewayFromRouter builds a minimal Gateway around a pre-built router —
// the wiring newTestGatewayWithRouter hard-codes, factored out so the
// photo-ingress tests can register their own providers before the gateway
// caches anything.
func newGatewayFromRouter(t *testing.T, store *sessions.Store, router *ai.Router, cfg config.AIConfig) *Gateway {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		sessions:       store,
		channelManager: newTestChannelManager(t),
		ai:             router,
		logger:         logger,
		ws:             NewWebSocketService(logger, websocket.Upgrader{}, 16),
		config:         &config.Config{AI: cfg},
	}
	return gw
}

var _ = channels.Manager{} // silence unused-import guard during refactors
