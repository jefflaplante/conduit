package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"conduit/internal/gateway/dashboard"
	"conduit/internal/middleware"
	internalssh "conduit/internal/ssh"
	"conduit/internal/tui"
	"conduit/internal/version"
)

// Shutdown/bind budget (conduit-31jg.27). systemd's TimeoutStopSec is 30s
// (deploy/conduit.service); SIGTERM drain (cmd/gateway signalDrainTimeout,
// 15s) + gatewayStopTimeout must stay under it with margin.
const (
	// gatewayStopTimeout bounds stopAll (HTTP shutdown, WS drain, channels,
	// SSH, MCP, ...) once the gateway context is cancelled.
	gatewayStopTimeout = 10 * time.Second

	// httpBindRetryWindow: how long to retry a bind that fails with
	// EADDRINUSE (e.g. a just-exited predecessor on re-exec) before failing
	// startup.
	httpBindRetryWindow = 3 * time.Second
)

// listenHTTP binds addr synchronously. EADDRINUSE is retried for up to
// retryWindow; any other error, or a still-busy port after the window, is
// returned so Start fails loudly instead of running without HTTP.
func listenHTTP(addr string, retryWindow time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(retryWindow)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// buildHTTPServer constructs the HTTP mux (diagnostics, WebSocket, debug,
// channels, vector API) and wraps it with the request-ID middleware. The
// returned *http.Server is ready to ListenAndServe.
func (g *Gateway) buildHTTPServer() *http.Server {
	mux := http.NewServeMux()

	// conduit-31jg.4: IP-keyed pre-auth limiter -> auth -> per-client
	// limiter. Previously auth ran first, so floods of missing/invalid
	// tokens were rejected (after SQLite lookups) without being counted.
	protect := func(h http.Handler) http.Handler {
		return g.rateLimitMiddleware.WrapPreAuth(g.auth.AuthMiddleware.Wrap(g.rateLimitMiddleware.Wrap(h)))
	}

	// Diagnostic endpoints - auth requirement controlled by diagnostics config.
	// Auth middleware skip paths are configured at gateway initialization based
	// on config. Default: /health is public (for load balancers), others
	// require auth.
	mux.Handle("/health", protect(http.HandlerFunc(g.handleHealthEnhanced)))
	mux.Handle("/metrics", protect(http.HandlerFunc(g.handleMetrics)))
	mux.Handle("/diagnostics", protect(http.HandlerFunc(g.handleDiagnostics)))
	mux.Handle("/prometheus", protect(http.HandlerFunc(g.handlePrometheusMetrics)))

	// WebSocket endpoint with custom authentication and rate limiting.
	// conduit-31jg.73: pre-auth IP limiter too, so /ws auth failures (401/403
	// before upgrade) charge the per-IP auth-failure budget.
	// conduit-31jg.85: WS auth runs before the per-client limiter so valid
	// clients get the authenticated tier; handleWebSocket reuses its result.
	mux.Handle("/ws", g.rateLimitMiddleware.WrapWebSocket(g.auth.WSAuthenticator, http.HandlerFunc(g.handleWebSocket)))

	// Protected API endpoints - wrapped with auth middleware and rate limiting.
	// Order (see protect): pre-auth IP limiter, auth (sets context),
	// per-client rate limiting (uses context), handler. POST endpoints also get request body size
	// limiting to prevent OOM attacks.
	mux.Handle("/debug/prompt", protect(http.HandlerFunc(g.handleDebugPrompt)))
	mux.Handle("/api/channels/status", protect(http.HandlerFunc(g.handleChannelStatus)))
	mux.Handle("/api/test/message", protect(
		limitRequestBody(http.HandlerFunc(g.handleTestMessage), MaxRequestBodySize)))

	// Vector API endpoints (registered unconditionally; handlers return 503
	// when disabled).
	vectorAPI := &VectorAPI{vectorService: g.search.VectorService}
	mux.Handle("/api/vector/search", protect(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleSearch), MaxRequestBodySize)))
	mux.Handle("/api/vector/index", protect(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleIndex), MaxRequestBodySize)))
	mux.Handle("/api/vector/delete", protect(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleDelete), MaxRequestBodySize)))
	mux.Handle("/api/vector/status", protect(http.HandlerFunc(vectorAPI.handleStatus)))

	// Brain memory-graph dashboard (gated by config.Brain.DashboardEnabled,
	// enforced inside the handler). The HTML chrome at /dashboard/brain and
	// the static asset bundle at /dashboard/assets/ are public; the JSON
	// data feed at /api/brain/graph is auth-gated.
	mux.Handle("/api/brain/graph", protect(http.HandlerFunc(g.handleBrainGraph)))
	mux.Handle("/dashboard/brain", dashboard.BrainHandler())
	mux.Handle("/dashboard/assets/", http.StripPrefix("/dashboard/assets/", dashboard.AssetsHandler()))

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
				Approvals:    g.approvals, // conduit-31jg.43
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
				Turns:        g.turns(), // conduit-31jg.35: shared turn pipeline
			})
		},
	}
	sshServer, err := internalssh.NewServer(sshConfig)
	if err != nil {
		// SSH is optional: a misconfiguration (e.g. no authorized keys,
		// conduit-31jg.1) disables SSH but must not take down the gateway.
		g.logger.Error("SSH server disabled: failed to create SSH server; continuing without SSH", "error", err)
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

// stopAll performs the orchestrated shutdown sequence: HTTP server, approvals,
// tools, channels, SSH, monitoring, scheduler, WebSocket, rate-limiter, search drain, MCP, MQTT,
// vector, and finally brain. Order is load-bearing and was preserved from the
// original inline shutdown block — see the per-step comments for rationale.
func (g *Gateway) stopAll(shutdownCtx context.Context, server *http.Server) {
	if err := server.Shutdown(shutdownCtx); err != nil {
		g.logger.Error("server shutdown error", "error", err)
	}

	// conduit-31jg.25: Shutdown does not close hijacked WebSocket conns;
	// drain them explicitly (bounded by shutdownCtx).
	if g.ws != nil {
		g.ws.Stop(shutdownCtx)
	}

	// conduit-31jg.43: drop pending approvals (never run) and let in-flight
	// approved actions finish while channels can still report the result.
	if g.approvals != nil {
		g.approvals.Close()
	}

	// conduit-enf0: release tool-held resources (SSH pool connections,
	// persistent sessions, tunnels) once no approved action can still use
	// them.
	if g.tools != nil {
		g.tools.CloseTools()
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

	// Close brain service (cognition; no-op when the brain is disabled).
	if err := g.cognition.Stop(); err != nil {
		g.logger.Error("error closing brain service", "error", err)
	}

	// conduit-2lzv: flush the LLM call log last — turns are drained by now.
	if err := g.ai.CloseCallLog(shutdownCtx); err != nil {
		g.logger.Warn("LLM call log close", "error", err)
	}
}

// setLifecycleCtx stores the gateway-lifecycle context (Start).
func (g *Gateway) setLifecycleCtx(ctx context.Context) {
	g.ctxMu.Lock()
	g.ctx = ctx
	g.ctxMu.Unlock()
}

// lifecycleCtx returns the gateway-lifecycle context bound by Start, or
// context.Background() before Start (conduit-31jg.73: Start used to write
// g.ctx unlocked while WS/wake goroutines read it).
func (g *Gateway) lifecycleCtx() context.Context {
	g.ctxMu.RLock()
	defer g.ctxMu.RUnlock()
	if g.ctx == nil {
		return context.Background()
	}
	return g.ctx
}

// Start starts the gateway server
func (g *Gateway) Start(ctx context.Context) error {
	// Wrap the incoming context so ShutdownManager can cancel it independently
	// of signal-based cancellation from main.go.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g.shutdownMgr.SetCancel(cancel)
	// conduit-31jg.27: let ShutdownManager wait for stopAll to finish.
	defer g.shutdownMgr.TrackGateway()()

	// Store the gateway lifecycle context for WebSocket handlers.
	// HTTP request contexts (r.Context()) are cancelled when the handler returns,
	// which is immediate after WebSocket upgrade. WebSocket goroutines need a
	// context tied to the gateway's lifecycle instead.
	g.setLifecycleCtx(ctx)
	g.ws.Start(ctx)

	// Build HTTP mux (diagnostics, WS, debug, channels, vector) and wrap it
	// with the request-ID middleware so auth/rate-limit logs can be correlated.
	server := g.buildHTTPServer()

	// conduit-31jg.27: bind synchronously so a port conflict fails startup
	// (non-zero exit, visible to systemd) instead of logging and running on
	// without HTTP/WS/health. Done before channels start so nothing needs
	// unwinding.
	listener, err := listenHTTP(server.Addr, httpBindRetryWindow)
	if err != nil {
		return fmt.Errorf("failed to bind HTTP listener on %s: %w", server.Addr, err)
	}

	// Start channel manager
	if err := g.startChannels(ctx); err != nil {
		_ = listener.Close()
		return fmt.Errorf("failed to start channels: %w", err)
	}

	// Start scheduler (loads jobs from cron_jobs.json)
	schedulerReady := false
	if g.scheduler != nil {
		if err := g.scheduler.Start(); err != nil {
			g.logger.Warn("failed to start scheduler", "error", err)
			g.logger.Warn("skipping heartbeat initialization to avoid wiping cron_jobs.json")
		} else {
			schedulerReady = true
		}
	}

	// Auto-create agent heartbeat job if enabled (MUST be after scheduler.Start() so
	// existing jobs are loaded from disk before we check for duplicates and potentially save)
	if schedulerReady {
		if err := g.initializeAgentHeartbeat(g.config); err != nil {
			g.logger.Warn("failed to initialize agent heartbeat", "error", err)
		}

		// Auto-create REM sleep cycle job if brain and REM are enabled
		if err := g.initializeREMCycle(g.config); err != nil {
			g.logger.Warn("failed to initialize REM sleep cycle", "error", err)
		}
	}

	// Start monitoring subsystem (heartbeat service + any future lifecycle).
	if err := g.monitoring.Start(ctx); err != nil {
		g.logger.Warn("failed to start monitoring service", "error", err)
	}

	// Start session state cleanup loop (prevents memory leak from abandoned sessions)
	stopCleanup := g.sessions.StartStateCleanup(30*time.Minute, 5*time.Minute)
	go func() {
		<-ctx.Done()
		stopCleanup()
	}()

	// Start cognition loops (conduit-18ub): the SPAR reflection idle-session
	// loop (Go-only metrics for substantive sessions that go idle; same
	// cadence as state cleanup) then the beads→Brain refresh loop.
	g.cognition.Start(ctx,
		func(ctx context.Context) { g.reflectOnIdleSessions(ctx, 30*time.Minute, 5*time.Minute) },
		func(ctx context.Context) { g.refreshBeadsPeriodic(ctx, 5*time.Minute) })

	// Start search subsystem: FTS file watcher and periodic safety-net
	// re-index loop (fsnotify handles real-time .md changes; the periodic
	// loop catches anything missed plus beads/brain/message re-indexing).
	if err := g.search.Start(ctx); err != nil {
		g.logger.Warn("failed to start search service", "error", err)
	}

	// Session wakeup listener: re-activates sessions when inter-session messages arrive.
	// Each wake signal triggers an AI processing loop on the target session in its own goroutine.
	// When we dequeue a session key we also clear its pendingWake slot so subsequent
	// wakes can enqueue again (coalescing only applies while a wake is still buffered).
	go func() {
		for {
			select {
			case sessionKey := <-g.sessionWake:
				g.clearPendingWake(sessionKey)
				go g.wakeSession(sessionKey)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start MQTT service if configured
	if g.mqttService != nil {
		if err := g.mqttService.Start(ctx); err != nil {
			g.logger.Warn("failed to start MQTT service", "error", err)
		} else {
			g.logger.Info("MQTT service started")
		}
	}

	// Start MCP server if configured (for claude-code provider)
	if g.mcpServer != nil {
		if err := g.mcpServer.Start(ctx); err != nil {
			g.logger.Error("failed to start MCP server", "error", err)
			// Non-fatal: gateway can still work without MCP
		} else {
			g.logger.Info("MCP server started")
		}

		// Write .mcp.json so Claude Code discovers the server
		if g.mcpConfigMgr != nil {
			if err := g.mcpConfigMgr.Setup(); err != nil {
				g.logger.Warn("failed to write .mcp.json", "error", err)
			}
		}
	}

	// Start SSH server if configured.
	g.startSSHServer(ctx)

	// Start message processing goroutine.
	go g.processMessages(ctx)

	// Serve on the pre-bound listener. A Serve failure after a successful
	// bind is fatal: shut the gateway down so the supervisor restarts it
	// rather than running headless (conduit-31jg.27).
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			g.logger.Error("HTTP server failed; shutting down gateway", "error", err)
			cancel()
		}
	}()

	g.logger.Info("gateway started", "port", g.config.Port, "addr", listener.Addr().String())

	g.processRestartBreadcrumb()

	// Wait for context cancellation, then drain all subsystems.
	<-ctx.Done()
	g.logger.Info("shutting down gateway")

	// Bounded so SIGTERM drain + stop stays under systemd's TimeoutStopSec
	// (see gatewayStopTimeout, conduit-31jg.27).
	shutdownCtx, stopCancel := context.WithTimeout(context.Background(), gatewayStopTimeout)
	defer stopCancel()
	g.stopAll(shutdownCtx, server)
	return nil
}
