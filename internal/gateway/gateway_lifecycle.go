package gateway

import (
	"context"
	"fmt"
	"net/http"

	internalssh "conduit/internal/ssh"
	"conduit/internal/middleware"
	"conduit/internal/tui"
	"conduit/internal/version"
)

// buildHTTPServer constructs the HTTP mux (diagnostics, WebSocket, debug,
// channels, vector API) and wraps it with the request-ID middleware. The
// returned *http.Server is ready to ListenAndServe.
func (g *Gateway) buildHTTPServer() *http.Server {
	mux := http.NewServeMux()

	// Diagnostic endpoints - auth requirement controlled by diagnostics config.
	// Auth middleware skip paths are configured at gateway initialization based
	// on config. Default: /health is public (for load balancers), others
	// require auth.
	mux.Handle("/health", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleHealthEnhanced))))
	mux.Handle("/metrics", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleMetrics))))
	mux.Handle("/diagnostics", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleDiagnostics))))
	mux.Handle("/prometheus", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handlePrometheusMetrics))))

	// WebSocket endpoint with custom authentication and rate limiting.
	mux.Handle("/ws", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleWebSocket)))

	// Protected API endpoints - wrapped with auth middleware and rate limiting.
	// Order: auth middleware first (sets context), then rate limiting (uses
	// context), then handler. POST endpoints also get request body size
	// limiting to prevent OOM attacks.
	mux.Handle("/debug/prompt", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleDebugPrompt))))
	mux.Handle("/api/channels/status", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleChannelStatus))))
	mux.Handle("/api/test/message", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(g.handleTestMessage), MaxRequestBodySize))))

	// Vector API endpoints (registered unconditionally; handlers return 503
	// when disabled).
	vectorAPI := &VectorAPI{vectorService: g.search.VectorService}
	mux.Handle("/api/vector/search", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleSearch), MaxRequestBodySize))))
	mux.Handle("/api/vector/index", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleIndex), MaxRequestBodySize))))
	mux.Handle("/api/vector/delete", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleDelete), MaxRequestBodySize))))
	mux.Handle("/api/vector/status", g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleStatus))))

	// Inject request_id into every HTTP request so auth and rate-limit logs
	// can be correlated across the entire request lifecycle.
	requestIDMiddleware := middleware.NewRequestIDMiddleware()

	return &http.Server{
		Addr:           fmt.Sprintf(":%d", g.config.Port),
		Handler:        requestIDMiddleware.Wrap(mux),
		MaxHeaderBytes: serverMaxHeaderBytes,
		ReadTimeout:    serverReadTimeout,
		WriteTimeout:   serverWriteTimeout,
		IdleTimeout:    serverIdleTimeout,
	}
}

// startSSHServer spins up the embedded SSH/TUI server when enabled in config.
// The server is best-effort; failures to build or listen are logged but do not
// abort gateway startup.
func (g *Gateway) startSSHServer(ctx context.Context) {
	if !g.config.SSH.Enabled {
		return
	}

	// Build shell security config from gateway config (SSH mode).
	shellCfg := &g.config.TUI.ShellEscape
	shellSecurity := tui.ShellSecurityConfig{
		Enabled:          shellCfg.IsShellEscapeEnabled(true), // true = SSH
		CommandAllowlist: shellCfg.CommandAllowlist,
		CommandBlocklist: shellCfg.GetEffectiveBlocklist(),
	}

	sshConfig := internalssh.SSHConfig{
		ListenAddr:         g.config.SSH.ListenAddr,
		HostKeyPath:        g.config.SSH.HostKeyPath,
		AuthorizedKeysPath: g.config.SSH.AuthorizedKeysPath,
		GatewayURL:         fmt.Sprintf("ws://localhost:%d/ws", g.config.Port),
		AssistantName:      g.config.Agent.Name,
		Location:           g.config.GetLocation(),
		ShellSecurity:      shellSecurity,
		ClientFactory: func(sshUser string) tui.GatewayClient {
			toolCount := len(g.tools.GetAvailableTools())
			var skillCount int
			if g.skillsManager != nil {
				if skills, err := g.skillsManager.GetAvailableSkills(context.Background()); err == nil {
					skillCount = len(skills)
				}
			}
			return NewDirectClient(DirectClientConfig{
				ParentCtx:    ctx,
				UserID:       sshUser,
				Sessions:     g.sessions,
				AI:           g.ai,
				Tools:        g.tools,
				Metrics:      g.monitoring.MetricsCollector,
				ModelAliases: g.getModelAliases(),
				AgentName:    g.config.Agent.Name,
				Version:      version.Info(),
				GitCommit:    version.GitCommit,
				UptimeFunc:   func() int64 { return int64(g.monitoring.GatewayMetrics.GetUptime().Seconds()) },
				ToolCount:    toolCount,
				SkillCount:   skillCount,
			})
		},
	}
	sshServer, err := internalssh.NewServer(sshConfig)
	if err != nil {
		g.logger.Warn("failed to create SSH server", "error", err)
		return
	}

	g.sshServer = sshServer
	go func() {
		g.logger.Info("SSH server listening", "address", sshConfig.ListenAddr, "mode", "direct")
		if err := sshServer.ListenAndServe(); err != nil {
			select {
			case <-ctx.Done():
			default:
				g.logger.Error("SSH server error", "error", err)
			}
		}
	}()
}

// stopAll performs the orchestrated shutdown sequence: HTTP server, channels,
// SSH, monitoring, scheduler, WebSocket, rate-limiter, search drain, MCP, MQTT,
// vector, and finally brain. Order is load-bearing and was preserved from the
// original inline shutdown block — see the per-step comments for rationale.
func (g *Gateway) stopAll(shutdownCtx context.Context, server *http.Server) {
	if err := server.Shutdown(shutdownCtx); err != nil {
		g.logger.Error("server shutdown error", "error", err)
	}

	g.stopChannels()

	// Stop SSH server.
	if g.sshServer != nil {
		g.logger.Debug("stopping SSH server")
		g.sshServer.Close()
	}

	// Stop monitoring subsystem (heartbeat service + any future lifecycle).
	if err := g.monitoring.Stop(); err != nil {
		g.logger.Error("error stopping monitoring service", "error", err)
	}

	// Stop scheduler.
	if g.scheduler != nil {
		g.scheduler.Stop()
	}

	// Stop WebSocket service (no-op today; see WebSocketService.Stop).
	// Active-request draining is handled by ShutdownManager before ctx is
	// cancelled, and per-client goroutines exit on ctx.Done via
	// handleClientWrite. Call order preserved so any future drain logic
	// runs before rate limiter shutdown.
	if g.ws != nil {
		g.ws.Stop()
	}

	// Stop rate limiting middleware.
	if g.rateLimitMiddleware != nil {
		g.rateLimitMiddleware.Stop()
	}

	// Drain async message syncer before closing search DB.
	g.search.DrainAsyncSyncer()

	// Stop MCP server and clean up .mcp.json.
	if g.mcpServer != nil {
		if err := g.mcpServer.Stop(shutdownCtx); err != nil {
			g.logger.Error("error stopping MCP server", "error", err)
		}
	}
	if g.mcpConfigMgr != nil {
		if err := g.mcpConfigMgr.Cleanup(); err != nil {
			g.logger.Warn("failed to clean up .mcp.json", "error", err)
		}
	}

	// Stop MQTT service.
	if g.mqttService != nil {
		g.mqttService.Stop()
	}

	// Stop vector indexer before closing the service it references, then
	// close the vector search service (no-op when disabled).
	g.search.StopVector()

	// Close brain service.
	if g.brainService != nil {
		if err := g.brainService.Close(); err != nil {
			g.logger.Error("error closing brain service", "error", err)
		}
	}
}
