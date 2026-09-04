package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/auth"
	"conduit/internal/brain"
	"conduit/internal/brain/rem"
	"conduit/internal/channels"
	"conduit/internal/channels/telegram"
	tuiAdapter "conduit/internal/channels/tui"
	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/logging"
	"conduit/internal/mcp"
	"conduit/internal/middleware"
	"conduit/internal/mqtt"
	"conduit/internal/protocol"
	"conduit/internal/reflection"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/stt"
	"conduit/internal/tools"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/types"
	"conduit/internal/version"
	"conduit/internal/workspace"

	charmssh "github.com/charmbracelet/ssh"
)

// HTTP server security limits
const (
	// MaxHeaderBytes limits HTTP request header size (1 MB).
	serverMaxHeaderBytes = 1 << 20 // 1 MB

	// ReadTimeout limits the time to read the entire request including body.
	serverReadTimeout = 30 * time.Second

	// WriteTimeout limits the time to write the response.
	serverWriteTimeout = 60 * time.Second

	// IdleTimeout limits the time an idle keep-alive connection stays open.
	serverIdleTimeout = 120 * time.Second

	// MaxRequestBodySize limits POST/PUT request body size (10 MB).
	MaxRequestBodySize int64 = 10 << 20 // 10 MB

	// MaxWebSocketConnections limits concurrent WebSocket connections.
	MaxWebSocketConnections int32 = 1000

	// MaxConcurrentRequests limits concurrent message-processing goroutines
	// to prevent unbounded goroutine growth under sustained load.
	MaxConcurrentRequests = 100
)

// Gateway represents the core Conduit gateway
type Gateway struct {
	config           *config.Config
	logger           *slog.Logger
	sessions         *sessions.Store
	ai               *ai.Router
	agentSystem      *agent.ConduitAgentWithIntegration
	tools            *tools.Registry
	workspaceContext *workspace.WorkspaceContext
	skillsManager    *skills.Manager
	channelManager   *channels.Manager
	scheduler        scheduler.SchedulerInterface
	compactionEngine *ai.CompactionEngine

	// Authentication (extracted into AuthService; see auth_service.go).
	auth *AuthService

	// Rate limiting
	rateLimitMiddleware *middleware.RateLimitMiddleware

	// Monitoring: metrics, heartbeat, event store, token-window fuel gauge,
	// and the alert delivery registry/audit trail. See MonitoringService
	// (monitoring_service.go) for the full surface.
	monitoring *MonitoringService

	// WebSocket subsystem (conduit-35t2): upgrader, client map, backpressure
	// semaphore, active-request cancel map, and the gateway-lifecycle context
	// used by per-connection goroutines. Extracted into WebSocketService to
	// break the Gateway god-object.
	ws *WebSocketService

	// ctx is the gateway-lifecycle context, bound by Start. It is shared with
	// WebSocketService but also used by sibling goroutines (subagents,
	// wakeSession, the SPAR reflection deferred-cleanup in handleClientRead)
	// so it stays on Gateway as the source of truth.
	ctx context.Context

	// Search: FTS5 indexer/searcher/watcher, dedicated search.db with
	// beads/brain/message indexers, and optional vector/semantic search
	// (extracted into SearchService; see search_service.go).
	search *SearchService

	// MQTT event ingest (optional)
	mqttService *mqtt.Service

	// Brain cognitive architecture (optional)
	brainService    *brain.Brain
	remCycle        *rem.REMCycle
	reflectionStore *reflection.ReflectionStore

	// SPAR reflection: session-end detection and metrics
	farewellDetector *reflection.FarewellDetector
	sessionReflector *reflection.SessionReflector

	// SSH server (optional)
	sshServer *charmssh.Server

	// MCP server for claude-code provider (optional)
	mcpServer    *mcp.Server
	mcpConfigMgr *mcp.MCPConfigManager

	// Debug ring buffer (for /ring command)
	ringBuffer *debuglog.RingBuffer

	// Graceful shutdown
	shutdownMgr *ShutdownManager

	// Session wakeup: session keys queued for immediate re-activation after
	// inter-session message delivery. pendingWake tracks which session keys
	// are already in the sessionWake buffer so repeated wakes for the same
	// session coalesce into one slot rather than filling the buffer.
	// See conduit-t38m.
	sessionWake     chan string
	pendingWakeMu   sync.Mutex
	pendingWakeKeys map[string]struct{}
}

// Client represents a WebSocket client connection
type Client struct {
	ID         string
	Role       string // "client" or "node"
	UserID     string // user identity for session scoping
	SessionKey string // active session key for this client
	TokenID    string // auth token ID used for this connection (for revocation)
	Conn       *websocket.Conn
	Send       chan []byte

	// CloseFrame carries an out-of-band signal from off-goroutine callers
	// (e.g. RevokeClientByToken running on the auth-revoke hook) asking the
	// send-pump to emit a WebSocket close frame with a specific payload
	// before exiting. The send-pump is the only goroutine that calls
	// Conn.Write*, so routing close frames through here guarantees
	// serialization and avoids the race where a revoker would call
	// WriteControl/Close concurrently with an in-flight WriteMessage on
	// the pump. Buffered size 1: duplicate revokes coalesce harmlessly
	// (first non-blocking send wins, the rest drop). nil on pre-existing
	// test fixtures is tolerated.
	CloseFrame chan []byte
}

// New creates a new Gateway instance
func New(cfg *config.Config) (*Gateway, error) {
	// Initialize structured logger
	logger := logging.New(cfg.Logging.GetLevel(), cfg.Logging.GetFormat())
	logging.SetDefault(logger)
	logger = logger.With("component", "gateway")

	// Initialize session store
	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	// Initialize workspace context if configured
	var workspaceContext *workspace.WorkspaceContext
	if cfg.Workspace.ContextDir != "" {
		logger.Info("initializing workspace context", "path", cfg.Workspace.ContextDir)
		workspaceContext = workspace.NewWorkspaceContext(cfg.Workspace.ContextDir)
	} else {
		logger.Warn("no workspace context directory configured")
	}

	// Initialize skills manager if configured
	var skillsManager *skills.Manager
	if cfg.Skills.Enabled {
		logger.Info("initializing skills manager")
		skillsManager = skills.NewManager(cfg.Skills)

		// Initialize skills manager
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := skillsManager.Initialize(ctx); err != nil {
			logger.Warn("failed to initialize skills manager", "error", err)
			// Continue without skills rather than failing completely
			skillsManager = nil
		} else {
			skillCount := 0
			if availableSkills, err := skillsManager.GetAvailableSkills(ctx); err == nil {
				skillCount = len(availableSkills)
			}
			logger.Info("skills manager initialized", "skill_count", skillCount)
		}
	} else {
		logger.Debug("skills system disabled in configuration")
	}

	// Initialize tools registry (tools will be registered after SetServices)
	toolsRegistry := tools.NewRegistry(cfg.Tools)

	// Initialize agent config (tools will be set after gateway is created)
	agentCfg := agent.AgentConfig{
		Name:        cfg.Agent.Name,
		Personality: cfg.Agent.Personality,
		Email:       cfg.Agent.Email,
		Identity: agent.IdentityConfig{
			OAuthIdentity:       cfg.Agent.Identity.OAuthIdentity,
			APIKeyIdentity:      cfg.Agent.Identity.APIKeyIdentity,
			OperatingPrinciples: cfg.Agent.Identity.OperatingPrinciples,
		},
		Capabilities: agent.AgentCapabilities{
			MemoryRecall:      cfg.Agent.Capabilities.MemoryRecall,
			ToolChaining:      cfg.Agent.Capabilities.ToolChaining,
			SkillsIntegration: cfg.Agent.Capabilities.SkillsIntegration,
			Heartbeats:        cfg.Agent.Capabilities.Heartbeats,
			SilentReplies:     cfg.Agent.Capabilities.SilentReplies,
		},
		PromptScaling:  cfg.Agent.PromptScaling,
		Timezone:       cfg.Timezone,
		RuntimeChannel: deriveRuntimeChannel(cfg.Channels),
	}

	// Use the integrated agent system (tools will be set after gateway is created)
	// SummaryManager is set later after AI router is available
	agentSystem := agent.NewConduitAgentWithIntegration(
		agentCfg,
		nil, // Tools set later after SetServices
		workspaceContext,
		nil, // SummaryManager set later after AI router is created
		skillsManager,
		cfg.AI.ModelAliases,
		nil, // BrainService set later via SetBrainService after brain is initialized
	)

	// Initialize the agent system
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agentSystem.Initialize(ctx); err != nil {
		logger.Warn("failed to initialize agent system", "error", err)
	} else {
		logger.Info("agent system initialized")
	}

	// Create tool execution engine with configurable chain limit
	maxToolChains := cfg.Tools.MaxToolChains
	if maxToolChains <= 0 {
		maxToolChains = 25 // Default fallback
	}
	executionEngine := tools.NewExecutionEngine(toolsRegistry, 4, 60*time.Second, maxToolChains)
	if cfg.Tools.MaxToolResultChars > 0 {
		executionEngine.SetMaxResultChars(cfg.Tools.MaxToolResultChars)
	}

	// Create debug ring buffer and wire verbose logging
	debugBuffer := debuglog.NewRingBuffer(debuglog.DefaultCapacity)
	executionEngine.SetDebugBuffer(debugBuffer)
	executionEngine.SetVerboseLogging(cfg.Debug.VerboseLogging)

	// Set package-level verbose logging for MQTT and AI
	mqtt.VerboseLogging = cfg.Debug.VerboseLogging
	ai.VerboseLogging = cfg.Debug.VerboseLogging

	executionAdapter := tools.NewExecutionEngineAdapter(executionEngine)

	// Initialize AI router with agent system AND execution engine
	aiRouter, err := ai.NewRouterWithExecution(cfg.AI, agentSystem, executionAdapter)
	if err != nil {
		return nil, fmt.Errorf("failed to create AI router: %w", err)
	}

	// Wire up session store for conversation history
	aiRouter.SetSessionStore(sessionStore)

	// Wire up token-aware history config
	aiRouter.SetHistoryConfig(&cfg.Agent.History)

	logger.Debug("tool execution engine wired up")

	// Initialize MCP server and session mapper if a claude-code provider is configured.
	mcpServer, mcpConfigMgr := setupMCPForClaudeCode(cfg, aiRouter, toolsRegistry, sessionStore, logger)

	// Initialize summary manager for AI-powered workspace summarization
	// (small-context models). Attaches to agentSystem when enabled.
	setupSummaryManager(cfg, logger, aiRouter, agentSystem, workspaceContext)

	// Initialize context compaction engine if enabled
	var compactionEngine *ai.CompactionEngine
	if cfg.AI.Compaction != nil && cfg.AI.Compaction.Enabled {
		compactionEngine = ai.NewCompactionEngine(aiRouter, sessionStore, *cfg.AI.Compaction)
		logger.Info("context compaction enabled",
			"threshold_percent", cfg.AI.Compaction.Threshold*100,
			"model", cfg.AI.Compaction.Model,
			"keep_messages", cfg.AI.Compaction.RecentMessagesToKeep)
	}

	// Initialize authentication subsystem (token storage, HTTP auth middleware,
	// WebSocket authenticator). See auth_service.go for details. This must be
	// constructed before rate-limit middleware and any handler that wraps with
	// auth middleware.
	authService, err := NewAuthService(cfg, logger, sessionStore.DB())
	if err != nil {
		return nil, fmt.Errorf("failed to create auth service: %w", err)
	}

	rateLimitMiddleware := buildRateLimitMiddleware(cfg, logger)

	// Initialize monitoring subsystem (metrics, heartbeat, event store,
	// token-window fuel gauge). Heartbeat integration and delivery registry
	// are wired in later once scheduler and channel sender exist.
	monitoringSvc, err := NewMonitoringService(cfg, logger, sessionStore, aiRouter)
	if err != nil {
		return nil, fmt.Errorf("failed to create monitoring service: %w", err)
	}

	wsService := NewWebSocketService(logger, websocket.Upgrader{
		CheckOrigin:  checkOrigin(cfg.AllowedOrigins),
		Subprotocols: []string{"conduit-auth"},
	}, MaxConcurrentRequests)

	gw := &Gateway{
		config:              cfg,
		logger:              logger,
		sessions:            sessionStore,
		ai:                  aiRouter,
		agentSystem:         agentSystem,
		tools:               toolsRegistry,
		workspaceContext:    workspaceContext,
		skillsManager:       skillsManager,
		channelManager:      nil, // Will be initialized below
		compactionEngine:    compactionEngine,
		auth:                authService,
		rateLimitMiddleware: rateLimitMiddleware,
		monitoring:          monitoringSvc,
		ws:                  wsService,
		sessionWake:         make(chan string, 64),
		pendingWakeKeys:     make(map[string]struct{}),
		mcpServer:           mcpServer,
		mcpConfigMgr:        mcpConfigMgr,
		ringBuffer:          debugBuffer,
	}

	gw.shutdownMgr = NewShutdownManager(logger, gw)

	// Register token revocation handler to close WebSocket connections
	// using a revoked token. This callback lives on *Gateway because it needs
	// access to the client map (which isn't owned by AuthService).
	gw.auth.AuthStorage.OnRevoke(gw.handleTokenRevocation)

	// Initialize channel manager and register factories
	gw.channelManager = channels.NewManager()
	var transcriber stt.Transcriber
	if gw.config.STT.Enabled && gw.config.STT.APIKey != "" {
		transcriber = stt.NewWhisperTranscriber(gw.config.STT.APIKey, gw.config.STT.Model)
	}
	gw.channelManager.RegisterFactory(telegram.NewFactoryWithDB(sessionStore.DB(), transcriber))
	gw.channelManager.RegisterFactory(tuiAdapter.NewFactory(nil)) // TUI factory for dynamic adapter creation

	// Now inject dependencies into tools registry to break the cycle
	// This triggers tool registration

	// Initialize search subsystem: FTS5 indices, dedicated search.db with
	// beads/message indexers, and optional vector/semantic search.
	// Brain indexer is attached later (after brain service is built) via
	// gw.search.WireBrainIndexer.
	searchSvc, err := NewSearchService(cfg, logger, sessionStore)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize search service: %w", err)
	}
	gw.search = searchSvc

	// Initialize optional MQTT event ingest service
	if cfg.MQTT.Enabled {
		gw.mqttService = mqtt.NewService(cfg.MQTT)
		logger.Info("MQTT service configured", "broker", cfg.MQTT.BrokerURL, "topic_count", len(cfg.MQTT.Topics))
	}

	// Initialize optional Brain cognitive architecture (+ REM cycle).
	gw.initBrainSubsystem(cfg)

	// Initialize optional SPAR reflection store (requires Brain for its database).
	gw.initReflectionSubsystem(cfg, executionEngine)

	toolsRegistry.SetServices(gw.buildToolServices(cfg, sessionStore, aiRouter, debugBuffer, skillsManager))

	// Register MCP tools now that the registry is fully populated.
	if mcpServer != nil {
		mcpServer.RegisterTools()
	}

	// NOW convert tools to AI format (after SetServices registered them)
	// Note: skill tools are already included via the registry (registered by registerSkillTools).
	// The agent's GetToolDefinitions() also adds skills dynamically for per-session filtering.
	aiTools := convertToolsToAIFormat(toolsRegistry)

	// Update agent with the now-registered tools
	agentSystem.SetTools(aiTools)

	// Wire brain service into agent for Situation Awareness prompt section
	if gw.brainService != nil {
		agentSystem.SetBrainService(gw.brainService)
	}

	// Initialize scheduler
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	gw.scheduler = scheduler.New(workspaceDir, gw.executeScheduledJob)

	// Initialize heartbeat integration
	hbIntegration := heartbeat.NewGatewayIntegration(workspaceDir, sessionStore, aiRouter, gw.scheduler, gw, gw.monitoring.MetricsCollector, cfg.AgentHeartbeat.Model, cfg.AgentHeartbeat.TimeoutSeconds)
	if gw.brainService != nil {
		hbIntegration.SetBrainWriter(newHeartbeatBrainWriter(gw.brainService))
		logger.Info("heartbeat Brain writer enabled for sense.alerts.* namespace")
	}
	gw.monitoring.WireHeartbeatIntegration(hbIntegration)

	// Wire alert auditor (conduit-1rp3): create a DeliveryRegistry and attach
	// an AlertAuditor backed by the same DB so every delivery attempt is
	// persisted to the alert_history table (migration #8).
	gw.monitoring.WireDeliveryRegistry(sessionStore.DB())
	logger.Info("alert auditor wired to delivery registry")

	// NOTE: initializeAgentHeartbeat is called AFTER scheduler.Start() in the Run() method
	// so that existing jobs are loaded from cron_jobs.json before the heartbeat job is added.

	logger.Info("gateway initialized",
		"agent_name", agentCfg.Name,
		"agent_personality", agentCfg.Personality,
		"workspace_enabled", workspaceContext != nil,
		"skills_enabled", skillsManager != nil && skillsManager.IsEnabled(),
		"tool_count", len(aiTools),
		"vector_search_enabled", gw.search.VectorService != nil,
		"mqtt_enabled", gw.mqttService != nil,
		"brain_enabled", gw.brainService != nil,
		"reflection_enabled", gw.reflectionStore != nil,
		"compaction_enabled", gw.compactionEngine != nil,
		"rate_limiting_enabled", cfg.RateLimiting.Enabled,
		"model_alias_count", len(cfg.AI.ModelAliases))

	if cfg.RateLimiting.Enabled {
		logger.Debug("rate limiting configuration",
			"anonymous_max_requests", cfg.RateLimiting.Anonymous.MaxRequests,
			"anonymous_window_seconds", cfg.RateLimiting.Anonymous.WindowSeconds,
			"authenticated_max_requests", cfg.RateLimiting.Authenticated.MaxRequests,
			"authenticated_window_seconds", cfg.RateLimiting.Authenticated.WindowSeconds)
	}

	return gw, nil
}

// createInternalToken generates an authentication token for internal services
// (e.g., the integrated SSH server) that connect back to the gateway via WebSocket.
func (g *Gateway) createInternalToken(clientName string) (string, error) {
	resp, err := g.auth.AuthStorage.CreateToken(auth.CreateTokenRequest{
		ClientName: clientName,
		Metadata: map[string]string{
			"type": "internal",
		},
	})
	if err != nil {
		return "", err
	}
	g.logger.Debug("created internal token", "client_name", clientName, "token_id", resp.TokenInfo.TokenID)
	return resp.Token, nil
}

// ShutdownManager returns the gateway's shutdown manager for external callers.
func (g *Gateway) ShutdownManager() *ShutdownManager {
	return g.shutdownMgr
}

// Start starts the gateway server
func (g *Gateway) Start(ctx context.Context) error {
	// Wrap the incoming context so ShutdownManager can cancel it independently
	// of signal-based cancellation from main.go.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g.shutdownMgr.SetCancel(cancel)

	// Store the gateway lifecycle context for WebSocket handlers.
	// HTTP request contexts (r.Context()) are cancelled when the handler returns,
	// which is immediate after WebSocket upgrade. WebSocket goroutines need a
	// context tied to the gateway's lifecycle instead.
	g.ctx = ctx
	g.ws.Start(ctx)

	// Build HTTP mux (diagnostics, WS, debug, channels, vector) and wrap it
	// with the request-ID middleware so auth/rate-limit logs can be correlated.
	server := g.buildHTTPServer()

	// Start channel manager
	if err := g.startChannels(ctx); err != nil {
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

	// Start SPAR reflection idle-session loop (writes Go-only metrics for
	// substantive sessions that go idle). Runs on the same cadence as state cleanup.
	if g.sessionReflector != nil {
		go g.reflectOnIdleSessions(ctx, 30*time.Minute, 5*time.Minute)
	}

	// Start beads→Brain wiring: query active tasks from `br` CLI and write
	// summary to Brain's sense.tasks.active namespace for Situation Awareness.
	// Best-effort: if br is missing or slow, this is silently skipped.
	if g.brainService != nil {
		go g.refreshBeadsPeriodic(ctx, 5*time.Minute)
	}

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

	// Start HTTP server in goroutine.
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			g.logger.Error("HTTP server error", "error", err)
		}
	}()

	g.logger.Info("gateway started", "port", g.config.Port)

	g.processRestartBreadcrumb()

	// Wait for context cancellation, then drain all subsystems.
	<-ctx.Done()
	g.logger.Info("shutting down gateway")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g.stopAll(shutdownCtx, server)
	return nil
}

// handleWebSocket handles WebSocket connections with authentication.
// This stays on *Gateway because it is the HTTP handler entry point and
// needs orchestrator-level access to auth (wsAuthenticator), metrics, tools,
// skills, and the channel manager for the initial GatewayInfo frame. It
// hands off to WebSocketService for connection-state bookkeeping.
func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check WebSocket connection limit before doing any work
	if g.ws.WSConnCount.Load() >= MaxWebSocketConnections {
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected: limit reached",
			"current", g.ws.WSConnCount.Load(),
			"max", MaxWebSocketConnections)
		return
	}

	// Authenticate the WebSocket upgrade request
	authResult := g.auth.WSAuthenticator.Authenticate(r)
	if !authResult.Authenticated {
		g.auth.WSAuthenticator.RejectUpgrade(w, authResult.Error)
		return
	}

	// Build response header for protocol negotiation
	var responseHeader http.Header
	if authResult.ResponseProtocol != "" {
		responseHeader = http.Header{
			"Sec-WebSocket-Protocol": []string{authResult.ResponseProtocol},
		}
	}

	// Atomically increment and check the connection count.
	// Re-check after increment to handle races between the Load() above and now.
	if count := g.ws.WSConnCount.Add(1); count > MaxWebSocketConnections {
		g.ws.WSConnCount.Add(-1)
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected (race): limit reached",
			"current", count-1,
			"max", MaxWebSocketConnections)
		return
	}

	conn, err := g.ws.Upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		g.ws.WSConnCount.Add(-1) // Decrement on upgrade failure
		g.logger.Error("WebSocket upgrade error", "error", err)
		return
	}

	client := &Client{
		ID:         fmt.Sprintf("client_%d", time.Now().UnixNano()),
		Role:       authResult.AuthInfo.ClientName, // Store authenticated client name
		UserID:     authResult.AuthInfo.ClientName, // Default user identity from auth
		TokenID:    authResult.AuthInfo.TokenID,    // Track token for revocation
		Conn:       conn,
		Send:       make(chan []byte, 256),
		CloseFrame: make(chan []byte, 1),
	}

	g.ws.ClientMu.Lock()
	g.ws.Clients[client.ID] = client
	clientCount := len(g.ws.Clients)
	g.ws.ClientMu.Unlock()

	// Update metrics
	if g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.UpdateWebSocketConnections(clientCount)
	}

	g.logger.Info("client connected", "client_id", client.ID, "auth", authResult.AuthInfo.ClientName)

	// Send enriched gateway info to client
	toolCount := len(g.tools.GetAvailableTools())
	var skillCount int
	if g.skillsManager != nil {
		if skills, err := g.skillsManager.GetAvailableSkills(context.Background()); err == nil {
			skillCount = len(skills)
		}
	}
	g.sendToClient(client, &protocol.GatewayInfo{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeGatewayInfo,
			ID:        fmt.Sprintf("gi_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		AssistantName: g.config.Agent.Name,
		Version:       version.Info(),
		GitCommit:     version.GitCommit,
		UptimeSeconds: int64(g.monitoring.GatewayMetrics.GetUptime().Seconds()),
		ModelAliases:  g.getModelAliases(),
		ToolCount:     toolCount,
		SkillCount:    skillCount,
	})

	// Handle client in separate goroutines.
	// Use g.ctx (gateway lifecycle) instead of r.Context() because the HTTP request
	// context is cancelled when this handler returns, which happens immediately
	// after spawning these goroutines.
	go g.handleClientWrite(client)
	go g.handleClientRead(g.ctx, client)
}

// handleTokenRevocation closes all WebSocket connections authenticated with the
// given token. This is called by TokenStorage.OnRevoke as a best-effort
// operation -- errors from already-closing connections are silently ignored.
//
// The name stays on *Gateway (per conduit-23rz) to keep the symbol stable for
// existing callers and tests. It delegates to WebSocketService.
func (g *Gateway) handleTokenRevocation(tokenID string) {
	if g.ws == nil {
		return
	}
	n := g.ws.RevokeClientByToken(tokenID)
	if n > 0 {
		g.logger.Info("closed connections for revoked token",
			"connection_count", n,
			"token_id", tokenID)
	}
}

// handleClientRead handles incoming messages from a WebSocket client
func (g *Gateway) handleClientRead(ctx context.Context, client *Client) {
	defer func() {
		// SPAR reflection: fire low-confidence (Go-only) reflection on WS disconnect
		// for substantive sessions. This runs before cleanup so the session data is
		// still available.
		if client.SessionKey != "" {
			reflCtx, reflCancel := context.WithTimeout(g.ctx, 5*time.Second)
			g.reflectOnSessionEnd(reflCtx, client.SessionKey)
			reflCancel()
		}

		g.ws.ClientMu.Lock()
		delete(g.ws.Clients, client.ID)
		clientCount := len(g.ws.Clients)
		g.ws.ClientMu.Unlock()

		// Decrement active WebSocket connection count
		g.ws.WSConnCount.Add(-1)

		// Update metrics
		if g.monitoring.MetricsCollector != nil {
			g.monitoring.MetricsCollector.UpdateWebSocketConnections(clientCount)
		}

		client.Conn.Close()
		g.logger.Debug("client disconnected", "client_id", client.ID)
	}()

	// Set message size limit to prevent DoS via large messages
	maxMessageSize := g.config.WebSocket.GetMaxMessageSize()
	client.Conn.SetReadLimit(maxMessageSize)

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				g.logger.Debug("client closed connection normally", "client_id", client.ID)
			} else {
				g.logger.Warn("WebSocket read error", "client_id", client.ID, "error", err)
			}
			break
		}

		parsed, err := protocol.ParseMessage(message)
		if err != nil {
			g.logger.Warn("failed to parse message", "client_id", client.ID, "error", err)
			continue
		}

		switch msg := parsed.(type) {
		case *protocol.ChatMessage:
			if g.shutdownMgr != nil && g.shutdownMgr.IsDraining() {
				g.sendToClient(client, map[string]string{
					"type":    "system",
					"content": "Gateway is restarting — not accepting new requests.",
				})
				continue
			}
			select {
			case g.ws.MsgSemaphore <- struct{}{}:
				go func() {
					defer func() { <-g.ws.MsgSemaphore }()
					g.handleWebSocketChat(ctx, client, msg)
				}()
			default:
				g.recordWebSocketDrop(client, msg, "msg_semaphore_full")
			}
		case *protocol.CommandMessage:
			go g.handleWebSocketCommand(ctx, client, msg)
		case *protocol.SessionSwitch:
			go g.handleWebSocketSessionSwitch(client, msg)
		case *protocol.HealthCheck:
			g.sendToClient(client, &protocol.HealthCheck{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeHealthCheck,
					ID:        fmt.Sprintf("health_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				Status: "ok",
			})
		default:
			g.logger.Debug("unhandled message type", "client_id", client.ID, "type", fmt.Sprintf("%T", msg))
		}
	}
}

// handleClientWrite is a thin wrapper that delegates to WebSocketService.
// Kept on *Gateway only so the goroutine-spawn call site in handleWebSocket
// reads naturally; the actual loop lives on WebSocketService.
func (g *Gateway) handleClientWrite(client *Client) {
	g.ws.HandleClientWrite(client)
}

// startChannels initializes and starts the channel manager
func (g *Gateway) startChannels(ctx context.Context) error {
	// Convert config channels to channel configs
	var channelConfigs []channels.ChannelConfig

	for _, chConfig := range g.config.Channels {
		channelConfig := channels.ChannelConfig{
			ID:      chConfig.Name,
			Type:    chConfig.Type,
			Name:    chConfig.Name,
			Enabled: chConfig.Enabled,
			Config:  chConfig.Config,
		}
		channelConfigs = append(channelConfigs, channelConfig)
	}

	// Start channel manager
	if err := g.channelManager.Start(ctx, channelConfigs); err != nil {
		return fmt.Errorf("failed to start channel manager: %w", err)
	}

	g.logger.Info("channel manager started")
	return nil
}

// stopChannels stops the channel manager
func (g *Gateway) stopChannels() {
	if err := g.channelManager.Stop(); err != nil {
		g.logger.Error("error stopping channel manager", "error", err)
	}
}

// processMessages handles the main message processing loop
func (g *Gateway) processMessages(ctx context.Context) {
	g.logger.Debug("starting message processor")

	for {
		select {
		case msg := <-g.channelManager.ReceiveMessages():
			if g.shutdownMgr != nil && g.shutdownMgr.IsDraining() {
				g.logger.Warn("rejecting channel message during shutdown drain",
					"channel_id", msg.ChannelID)
				continue
			}
			select {
			case g.ws.MsgSemaphore <- struct{}{}:
				go func() {
					defer func() { <-g.ws.MsgSemaphore }()
					g.handleIncomingMessage(ctx, msg)
				}()
			default:
				g.recordIngestDrop(msg, "msg_semaphore_full")
			}

		case <-ctx.Done():
			return
		}
	}
}

// recordIngestDrop emits the gateway.ingest.drops{channel} metric and writes a
// DLQ row for an ingress message that could not be handed off because
// msgSemaphore was full. Silent drops were the bug in conduit-101n.
func (g *Gateway) recordIngestDrop(msg *protocol.IncomingMessage, reason string) {
	g.logger.Warn("request backpressure: dropping channel message",
		"channel_id", msg.ChannelID, "reason", reason)

	if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
		g.monitoring.GatewayMetrics.IncrementIngestDrop(msg.ChannelID)
	}
	if g.sessions != nil {
		if err := writeIngestDLQ(g.sessions.DB(), msg, reason); err != nil {
			g.logger.Error("failed to write ingest DLQ row",
				"channel_id", msg.ChannelID, "error", err)
		}
	}
}

// recordWebSocketDrop is the WS-chat analogue of recordIngestDrop. WS clients
// don't have a channel adapter, so the drop is counted under the synthetic
// "websocket" channel label and the DLQ row records the originating client.
func (g *Gateway) recordWebSocketDrop(client *Client, msg *protocol.ChatMessage, reason string) {
	g.logger.Warn("request backpressure: dropping chat message",
		"client_id", client.ID, "reason", reason)

	if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
		g.monitoring.GatewayMetrics.IncrementIngestDrop("websocket")
	}
	if g.sessions != nil {
		if err := writeClientChatDLQ(g.sessions.DB(), client.ID, client.UserID, msg.SessionKey, msg.Text, reason); err != nil {
			g.logger.Error("failed to write ingest DLQ row",
				"client_id", client.ID, "error", err)
		}
	}
}

// handleIncomingMessage processes a single incoming message
func (g *Gateway) handleIncomingMessage(ctx context.Context, msg *protocol.IncomingMessage) {
	// Add request ID to context for correlation
	ctx = logging.WithRequestID(ctx, "")
	reqID := logging.RequestIDFromContext(ctx)

	g.logger.Debug("processing message",
		"channel_id", msg.ChannelID,
		"text_length", len(msg.Text),
		"request_id", reqID)

	// Track activity in metrics collector
	if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.MarkActivity()
	}

	// Get or create session
	session, err := g.sessions.GetOrCreateSession(msg.UserID, msg.ChannelID)
	if err != nil {
		logging.Error(ctx, "error getting session", "error", err)
		return
	}

	// Handle commands before AI processing
	if handled := g.handleCommand(ctx, msg, session); handled {
		return
	}

	// conduit-1mnp: busy-ack. If a turn is already in flight for this session,
	// the per-session turn lock will queue this message behind it — tell the
	// user immediately instead of leaving them in silence. 30s cooldown per
	// session so rapid nudges don't spam acks.
	g.ws.ActiveRequestsMu.Lock()
	_, turnInFlight := g.ws.ActiveRequests[session.Key]
	g.ws.ActiveRequestsMu.Unlock()
	if turnInFlight {
		now := time.Now().Unix()
		lastAck, _ := strconv.ParseInt(session.Context["last_busy_ack"], 10, 64)
		if now-lastAck > 30 {
			ack := &protocol.OutgoingMessage{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeOutgoingMessage,
					ID:        fmt.Sprintf("busyack_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				ChannelID:  msg.ChannelID,
				SessionKey: msg.SessionKey,
				UserID:     msg.UserID,
				Text:       "Still working on your previous request — this message is queued and I'll handle it right after.",
			}
			g.channelManager.SendMessage(ack)
			_ = g.sessions.SetSessionContext(session.Key, "last_busy_ack", strconv.FormatInt(now, 10))
			logging.Info(ctx, "busy-ack sent, message queued behind in-flight turn",
				"session_key", session.Key)
		}
	}

	// Reset wake_depth on normal user messages so the recursion guard resets
	// after a human sends a message to the session.
	if session.Context["wake_depth"] != "" && session.Context["wake_depth"] != "0" {
		_ = g.sessions.SetSessionContext(session.Key, "wake_depth", "0")
	}

	// Add user message to session (store text marker for photos, not binary data)
	textToStore := msg.Text
	if len(msg.Attachments) > 0 {
		if textToStore == "" {
			textToStore = "[Sent a photo]"
		} else {
			textToStore = "[Photo] " + textToStore
		}
	}
	_, err = g.sessions.AddMessage(session.Key, "user", textToStore, msg.Metadata)
	if err != nil {
		logging.Error(ctx, "error saving user message", "error", err)
		return
	}

	// SPAR reflection: check for farewell or context budget trigger before sending to AI.
	isFarewell, _ := g.shouldTriggerReflection(msg.Text)
	isContextBudgetReflect := false
	messageForAI := msg.Text
	if isFarewell {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			messageForAI = msg.Text + "\n\n[System: " + reflPrompt + "]"
			g.logger.Info("SPAR reflection: farewell detected, injecting reflection prompt",
				"session_key", session.Key, "channel_id", msg.ChannelID)
		}
	} else if session.Context["reflection_context_budget_triggered"] == "true" {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			messageForAI = msg.Text + "\n\n[System: " + reflPrompt + "]"
			isContextBudgetReflect = true
			_ = g.sessions.SetSessionContextBatch(session.Key, map[string]string{
				"reflection_context_budget_triggered": "",
			})
			g.logger.Info("SPAR reflection: context budget triggered, injecting reflection prompt",
				"session_key", session.Key)
		}
	}

	// Start typing indicator loop (refreshes every 4 seconds until done).
	// Buffered to 1 to prevent goroutine leak if close() races with send.
	typingDone := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		// Send immediately
		g.channelManager.SendTypingIndicator(msg.ChannelID, msg.UserID)

		for {
			select {
			case <-typingDone:
				return
			case <-ticker.C:
				g.channelManager.SendTypingIndicator(msg.ChannelID, msg.UserID)
			}
		}
	}()

	// Generate AI response with tool execution support
	if g.ai != nil {
		// Create cancellable context for this request
		reqCtx, cancel := context.WithCancel(ctx)
		reqCtx = types.WithRequestContext(reqCtx, msg.ChannelID, msg.UserID, session.Key)

		// Thread image attachments to the AI layer for vision analysis
		if len(msg.Attachments) > 0 {
			aiAttachments := make([]ai.Attachment, len(msg.Attachments))
			for i, att := range msg.Attachments {
				aiAttachments[i] = ai.Attachment{
					Type:      att.Type,
					MediaType: att.MediaType,
					Data:      att.Data,
				}
			}
			reqCtx = ai.WithAttachments(reqCtx, aiAttachments)
		}

		// Track this request so /stop can cancel it
		g.ws.ActiveRequestsMu.Lock()
		g.ws.ActiveRequests[session.Key] = cancel
		requestCount := len(g.ws.ActiveRequests)
		g.ws.ActiveRequestsMu.Unlock()

		// Update metrics
		if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
			g.monitoring.MetricsCollector.UpdateActiveRequests(requestCount)
		}

		// Ensure we clean up when done
		defer func() {
			g.ws.ActiveRequestsMu.Lock()
			delete(g.ws.ActiveRequests, session.Key)
			finalRequestCount := len(g.ws.ActiveRequests)
			g.ws.ActiveRequestsMu.Unlock()

			// Update metrics on cleanup
			if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
				g.monitoring.MetricsCollector.UpdateActiveRequests(finalRequestCount)
			}
		}()

		// Attach tool event callback for thinking status and tool logging
		reqCtx = tools.WithToolEventCallback(reqCtx, func(event tools.ToolEventInfo) {
			if event.EventType == "thinking" {
				g.channelManager.SendTypingIndicator(msg.ChannelID, msg.UserID)
			}
			logging.Debug(reqCtx, "tool event",
				"channel", msg.ChannelID,
				"event", event.EventType,
				"tool", event.ToolName)
		})

		// Get model and provider overrides from session context
		modelOverride := session.Context["model"]
		providerOverride := session.Context["provider"]

		// Check if adapter supports streaming
		adapter, _ := g.channelManager.GetAdapter(msg.ChannelID)
		streamingAdapter, supportsStreaming := adapter.(channels.StreamingAdapter)

		var convResponse ai.ConversationResponse
		var err error
		typingClosed := false
		streamingUsed := false

		if supportsStreaming {
			// Use streaming mode
			chatID, _ := strconv.ParseInt(msg.UserID, 10, 64)

			// Send placeholder message
			placeholderMsgID, sendErr := streamingAdapter.SendMessageWithID(chatID, "...")
			if sendErr != nil {
				logging.Warn(reqCtx, "streaming: failed to send placeholder", "error", sendErr)
				supportsStreaming = false // Fall back to non-streaming
			} else {
				close(typingDone) // Stop typing indicator since we have a message now
				typingClosed = true

				// Set up streaming state
				var textBuilder strings.Builder
				var lastEditTime time.Time
				lastEditLen := 0 // track text length at last edit for delta detection
				editInterval := 500 * time.Millisecond
				minCharsForEdit := 50

				onDelta := func(delta string, done bool) {
					textBuilder.WriteString(delta)

					currentText := textBuilder.String()
					timeSinceEdit := time.Since(lastEditTime)

					// Edit message if enough time passed or enough chars accumulated or done
					shouldEdit := done ||
						(timeSinceEdit >= editInterval && len(currentText) > minCharsForEdit) ||
						(len(currentText)-lastEditLen > 100) // Every 100 new chars since last edit

					if shouldEdit && len(currentText) > 0 {
						// Strip trailing silent tokens before showing to user
						displayText := channels.StripTrailingSilentTokens(currentText)
						if displayText != "" {
							if editErr := streamingAdapter.EditMessageText(chatID, placeholderMsgID, displayText); editErr != nil {
								logging.Warn(reqCtx, "streaming: edit failed", "error", editErr)
							}
						}
						lastEditTime = time.Now()
						lastEditLen = len(currentText)
					}
				}

				convResponse, err = g.ai.GenerateResponseStreaming(reqCtx, session, messageForAI, providerOverride, modelOverride, onDelta)

				// Final edit with complete text
				if err == nil && convResponse != nil {
					finalContent := convResponse.GetContent()
					streamedLength := textBuilder.Len()

					// Check for silent response patterns in final content
					if channels.IsSilentResponse(finalContent) {
						logging.Debug(reqCtx, "streaming: silent response pattern detected, deleting placeholder")
						// Delete the placeholder message since we don't want to show this
						if deleteErr := streamingAdapter.DeleteMessage(chatID, placeholderMsgID); deleteErr != nil {
							logging.Warn(reqCtx, "streaming: failed to delete placeholder", "error", deleteErr)
						}
						streamingUsed = true
					} else if finalContent != "" {
						// Sanitize internal markers before editing the placeholder
						finalContent = channels.SanitizeOutgoingText(finalContent)
						// If tool execution happened, the final content might be different from streamed text
						if streamedLength > 0 && finalContent != textBuilder.String() {
							logging.Debug(reqCtx, "streaming: tool execution detected",
								"streamed_chars", streamedLength,
								"final_chars", len(finalContent))
						}
						if finalContent != "" {
							if editErr := streamingAdapter.EditMessageText(chatID, placeholderMsgID, finalContent); editErr != nil {
								logging.Warn(reqCtx, "streaming: final edit failed, falling back to SendMessage", "error", editErr)
								_ = streamingAdapter.DeleteMessage(chatID, placeholderMsgID)
								// streamingUsed stays false → non-streaming SendMessage path will run
							} else {
								streamingUsed = true
							}
						} else {
							logging.Warn(reqCtx, "streaming: finalContent empty after sanitization, deleting placeholder")
							_ = streamingAdapter.DeleteMessage(chatID, placeholderMsgID)
						}
					} else if streamedLength > 0 {
						// Fallback to streamed text if no final content
						streamedText := textBuilder.String()
						// Also check streamed text for silent patterns
						if channels.IsSilentResponse(streamedText) {
							logging.Debug(reqCtx, "streaming: silent response in streamed text, deleting placeholder")
							if deleteErr := streamingAdapter.DeleteMessage(chatID, placeholderMsgID); deleteErr != nil {
								logging.Warn(reqCtx, "streaming: failed to delete placeholder", "error", deleteErr)
							}
							streamingUsed = true
						} else {
							streamedText = channels.SanitizeOutgoingText(streamedText)
							logging.Debug(reqCtx, "streaming: using streamed text only", "chars", streamedLength)
							if streamedText != "" {
								if editErr := streamingAdapter.EditMessageText(chatID, placeholderMsgID, streamedText); editErr != nil {
									logging.Warn(reqCtx, "streaming: streamed text edit failed, falling back", "error", editErr)
									_ = streamingAdapter.DeleteMessage(chatID, placeholderMsgID)
								} else {
									streamingUsed = true
								}
							} else {
								logging.Warn(reqCtx, "streaming: streamedText empty after sanitization, deleting placeholder")
								_ = streamingAdapter.DeleteMessage(chatID, placeholderMsgID)
							}
						}
					}
				}
			}
		}

		// Fall back to non-streaming if streaming not used or failed
		if !supportsStreaming || (convResponse == nil && err == nil) {
			// Progress callback for status updates during long operations
			onProgress := func(status string) {
				progressMsg := &protocol.OutgoingMessage{
					BaseMessage: protocol.BaseMessage{
						Type:      protocol.TypeOutgoingMessage,
						ID:        fmt.Sprintf("progress_%d", time.Now().UnixNano()),
						Timestamp: time.Now(),
					},
					ChannelID:  msg.ChannelID,
					SessionKey: msg.SessionKey,
					UserID:     msg.UserID,
					Text:       status,
				}
				g.channelManager.SendMessage(progressMsg)
			}

			convResponse, err = g.ai.GenerateResponseWithToolsAndProgress(reqCtx, session, messageForAI, providerOverride, modelOverride, onProgress)
		}
		if err != nil {
			if !typingClosed {
				close(typingDone) // Stop typing indicator
			}

			// Check if this was a cancellation (from /stop)
			if reqCtx.Err() == context.Canceled {
				logging.Debug(reqCtx, "request cancelled for session", "session_key", session.Key)
				return // Silent return, /stop already sent a message
			}

			logging.Error(reqCtx, "error generating AI response", "error", err)

			// Send error message back to user
			errorMsg := &protocol.OutgoingMessage{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeOutgoingMessage,
					ID:        fmt.Sprintf("error_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				ChannelID:  msg.ChannelID,
				SessionKey: msg.SessionKey,
				UserID:     msg.UserID,
				Text:       ai.GetUserMessage(err),
			}

			g.channelManager.SendMessage(errorMsg)
			return
		}

		if !typingClosed {
			close(typingDone) // Stop typing indicator
		}

		responseContent := convResponse.GetContent()

		// Persist usage to session context for /context command and context-budget gauge (conduit-2v0t)
		if usage := convResponse.GetUsage(); usage != nil {
			batch := recordTokenUsage(session, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)

			// Proactive context window warning
			if warning := contextWarningIfNeeded(session, usage.PromptTokens, modelOverride); warning.Text != "" {
				responseContent += warning.Text
				batch[warning.Key] = "true"
				// SPAR: trigger reflection on next message when context budget >= 80%
				if warning.Key == "context_warned_80" && g.sessionReflector != nil {
					batch["reflection_context_budget_triggered"] = "true"
				}
			}

			_ = g.sessions.SetSessionContextBatch(session.Key, batch)
		}

		// Check for silent response tokens (NO_REPLY, HEARTBEAT_OK)
		if responseContent == "" || channels.IsSilentResponse(responseContent) {
			if responseContent == "" {
				logging.Warn(ctx, "empty response content, not sending to channel")
			} else {
				logging.Debug(ctx, "silent response detected in channel message, suppressing",
					"response_chars", len(responseContent))
			}
			return
		}

		// Add AI response to session
		_, err = g.sessions.AddMessage(session.Key, "assistant", responseContent, nil)
		if err != nil {
			logging.Error(ctx, "error saving AI message", "error", err)
		}

		// SPAR reflection: after model responds to farewell or context budget trigger, compute session metrics
		if isFarewell || isContextBudgetReflect {
			if updatedSession, sErr := g.sessions.GetSession(session.Key); sErr == nil {
				g.reflectHighConfidencePost(ctx, updatedSession)
			}
		}

		// Skip sending if streaming already edited the message
		if streamingUsed {
			logging.Debug(ctx, "streaming: response delivered via message editing",
				"response_chars", len(responseContent))
			return
		}

		// Send response back through channel
		outgoingMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("response_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID:  msg.ChannelID,
			SessionKey: msg.SessionKey,
			UserID:     msg.UserID,
			Text:       responseContent,
		}

		// Forward source message ID so reply tags can resolve [[reply_to_current]]
		if srcID, ok := msg.Metadata["message_id"]; ok && srcID != "" {
			outgoingMsg.Metadata = map[string]string{
				"source_message_id": srcID,
			}
		}

		if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
			logging.Error(ctx, "error sending response", "error", err)
		}
	} else {
		// Echo back if no AI available (for testing)
		echoMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("echo_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID:  msg.ChannelID,
			SessionKey: msg.SessionKey,
			UserID:     msg.UserID,
			Text:       fmt.Sprintf("Echo: %s", msg.Text),
		}

		g.channelManager.SendMessage(echoMsg)
	}
}
