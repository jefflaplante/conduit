package gateway

import (
	"database/sql"
	"log/slog"
	"net/http"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/logging"
	"conduit/internal/middleware"
)

// AuthService owns the token-storage, HTTP auth middleware, and WebSocket
// authenticator subsystems that were previously inlined into the Gateway
// struct. It centralises construction for the authentication bits so Gateway
// can stay focused on request routing and channel orchestration.
//
// Fields are exported so sibling files in the gateway package (and tests) can
// continue to touch the underlying subsystems directly. Cross-package
// consumers should go through *Gateway, which exposes the relevant operations
// via its own method set.
//
// AuthService has no background goroutines, so it does not provide
// Start/Stop lifecycle methods — the request-path middlewares are purely
// reactive.
type AuthService struct {
	// AuthStorage is the backing token store. It owns token creation,
	// validation, revocation, and the revocation callback registry.
	AuthStorage *auth.TokenStorage

	// AuthMiddleware is the net/http middleware that authenticates every
	// incoming HTTP request against AuthStorage, with configurable skip paths
	// for public endpoints.
	AuthMiddleware *middleware.AuthMiddleware

	// WSAuthenticator handles WebSocket upgrade authentication. It reads the
	// subprotocol/auth header before the upgrade completes and either permits
	// the upgrade (returning AuthInfo) or rejects it via an HTTP response.
	WSAuthenticator *middleware.WebSocketAuthenticator
}

// NewAuthService constructs the authentication subsystem. It wires the
// token-storage, HTTP auth middleware (with diagnostics-based skip-path
// computation), and the WebSocket authenticator together.
//
// The revocation callback that closes active WebSocket connections for a
// revoked token lives on *Gateway (it needs access to the client map); the
// caller should register it via AuthService.AuthStorage.OnRevoke(...) once
// the Gateway struct exists.
func NewAuthService(cfg *config.Config, logger *slog.Logger, db *sql.DB) (*AuthService, error) {
	// Initialize token storage using the shared database.
	authStorage := auth.NewTokenStorage(db, cfg.Auth.TokenSecret)

	// Build auth skip paths based on diagnostics config.
	// By default, require auth for /metrics, /diagnostics, /prometheus.
	// /health is configurable for load balancer compatibility.
	var authSkipPaths []string
	if cfg.Diagnostics.IsHealthPublic() {
		authSkipPaths = append(authSkipPaths, "/health")
	}
	if !cfg.Diagnostics.RequireAuth {
		// Legacy behavior: all diagnostic endpoints are public.
		authSkipPaths = append(authSkipPaths, "/metrics", "/diagnostics", "/prometheus")
	}

	// Create auth middleware.
	authMiddleware := middleware.NewAuthMiddleware(authStorage, middleware.AuthMiddlewareConfig{
		SkipPaths: authSkipPaths,
		Logger:    logger,
		OnAuthError: func(r *http.Request, err middleware.AuthError) {
			logging.Warn(r.Context(), "authentication failed",
				"method", r.Method,
				"path", r.URL.Path,
				"code", err.Code)
		},
	})

	// Create WebSocket authenticator.
	wsAuthenticator := middleware.NewWebSocketAuthenticator(authStorage, logger)

	return &AuthService{
		AuthStorage:     authStorage,
		AuthMiddleware:  authMiddleware,
		WSAuthenticator: wsAuthenticator,
	}, nil
}
