package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/auth"
	"conduit/internal/channels"
	"conduit/internal/channels/telegram"
	tuiAdapter "conduit/internal/channels/tui"
	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/logging"
	"conduit/internal/mcp"
	"conduit/internal/middleware"
	"conduit/internal/mqtt"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/stt"
	"conduit/internal/tools"
	"conduit/internal/tools/debuglog"
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
	// so it stays on Gateway as the source of truth. Written by Start under
	// ctxMu; read it through lifecycleCtx() (conduit-31jg.73).
	ctx   context.Context
	ctxMu sync.RWMutex

	// Search: FTS5 indexer/searcher/watcher, dedicated search.db with
	// beads/brain/message indexers, and optional vector/semantic search
	// (extracted into SearchService; see search_service.go).
	search *SearchService

	// MQTT event ingest (optional)
	mqttService *mqtt.Service

	// Cognition (optional): Brain cognitive architecture, REM cycle and SPAR
	// reflection (session-end detection and metrics). Held by value so a
	// zero Gateway has a valid, disabled cognition service. See
	// CognitionService (cognition_service.go; conduit-18ub).
	cognition CognitionService

	// SSH server (optional)
	sshServer *charmssh.Server

	// MCP server for claude-code provider (optional)
	mcpServer    *mcp.Server
	mcpConfigMgr *mcp.MCPConfigManager

	// Debug ring buffer (for /ring command)
	ringBuffer *debuglog.RingBuffer

	// Human-in-the-loop approvals for risky tool actions (conduit-31jg.43).
	approvals *approval.Manager

	// Live config reload state for update_config (conduit-rmho); see
	// config_reload.go. Read the effective config via currentConfig().
	reload configReloader

	// Shared turn pipeline (conduit-31jg.35); built lazily by turns().
	turnRunnerOnce sync.Once
	turnRunner     *TurnRunner

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

// New creates a new Gateway instance
func New(cfg *config.Config) (*Gateway, error) {
	// Initialize structured logger
	logger := logging.New(cfg.Logging.GetLevel(), cfg.Logging.GetFormat())
	logging.SetDefault(logger)
	logger = logger.With("component", "gateway")

	// Initialize session store
	// conduit-31jg.3: same DB path resolver as the `conduit token` CLI.
	sessionStore, err := sessions.NewStore(auth.ResolveDatabasePath(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	// Initialize workspace context if configured
	var workspaceContext *workspace.WorkspaceContext
	if cfg.Workspace.ContextDir != "" {
		logger.Info("initializing workspace context", "path", cfg.Workspace.ContextDir)
		workspaceContext = workspace.NewWorkspaceContextWithLookback(cfg.Workspace.ContextDir, cfg.Workspace.Files.Memory.DailyLookbackDays)
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
		QuietHours:     promptQuietHours(cfg), // conduit-31jg.60
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

	// conduit-1z0g: wire the AI router as the empty guard's cross-model
	// failover source. z.ai returns HTTP 200 with an EMPTY payload under load
	// (prompt_tokens=0); same-model retries die identically, so the guard's
	// final attempt runs the provider's fallback_model on its OWN provider.
	ai.SetEmptyFailoverRouter(aiRouter)

	// conduit-31jg.57: the router built ONE pricing resolver from cfg.AI
	// (ai.pricing_overrides + deprecated smart_routing alias + built-ins);
	// make it the package default so resolver-less paths agree.
	ai.SetDefaultPricingResolver(aiRouter.PricingResolver())
	if n := len(aiRouter.PricingResolver().OverrideModels()); n > 0 {
		logger.Info("pricing overrides loaded", "models", n)
	}

	// Wire up session store for conversation history
	aiRouter.SetSessionStore(sessionStore)

	// Wire up token-aware history config
	aiRouter.SetHistoryConfig(&cfg.Agent.History)

	// conduit-2lzv: persistent metadata-only LLM call log (JSONL, rotated).
	if callLog, err := ai.OpenCallLog(cfg.AI.CallLog, cfg.DataDir); err != nil {
		logger.Warn("LLM call log disabled", "error", err)
	} else if callLog != nil {
		aiRouter.SetCallLog(callLog)
		logger.Info("LLM call log enabled", "path", callLog.Path())
	}

	logger.Debug("tool execution engine wired up")

	// Initialize MCP server and session mapper if a claude-code provider is configured.
	mcpServer, mcpConfigMgr := setupMCPForClaudeCode(cfg, aiRouter, toolsRegistry, executionEngine, sessionStore, logger)

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
	gw.initApprovals() // conduit-31jg.43

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
	gw.cognition.initBrain(cfg, logger)

	// Initialize optional SPAR reflection store (requires Brain for its database).
	gw.cognition.initReflection(cfg, executionEngine, logger)

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
	if gw.cognition.BrainEnabled() {
		agentSystem.SetBrainService(gw.cognition.Brain)
	}

	// Initialize scheduler
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	// conduit-31jg.34: cron stays in time.Local on purpose. Existing
	// cron_jobs.json expressions were written for the server zone (UTC);
	// scheduler.WithLocation(cfg.GetLocation()) would shift them. Per-job
	// "CRON_TZ=<zone> " prefixes are supported for opt-in migration.
	// conduit-31jg.60: `conduit cron migrate-tz` rewrites jobs to carry
	// CRON_TZ=<configured zone>; migrated jobs ignore this default.
	gw.scheduler = scheduler.New(workspaceDir, gw.executeScheduledJob)

	// Initialize heartbeat integration
	hbIntegration := heartbeat.NewGatewayIntegration(workspaceDir, sessionStore, aiRouter, gw.scheduler, gw, gw.monitoring.MetricsCollector, cfg.AgentHeartbeat.Model, cfg.AgentHeartbeat.TimeoutSeconds)
	hbIntegration.SetAgentHeartbeatConfig(cfg.AgentHeartbeat) // conduit-31jg.33: configured TZ + quiet window
	if gw.cognition.BrainEnabled() {
		hbIntegration.SetBrainWriter(newHeartbeatBrainWriter(gw.cognition.Brain))
		logger.Info("heartbeat Brain writer enabled for sense.alerts.* namespace")
	}
	// conduit-31jg.66: heartbeat turns run on the shared TurnRunner.
	hbIntegration.SetAIExecutor(newTurnAIExecutor(gw))
	gw.monitoring.WireHeartbeatIntegration(hbIntegration)

	// Wire alert auditor (conduit-1rp3): create a DeliveryRegistry and attach
	// an AlertAuditor backed by the same DB so every delivery attempt is
	// persisted to the alert_history table (migration #8).
	gw.monitoring.WireDeliveryRegistry(sessionStore.DB())
	// conduit-31jg.59: route heartbeat delivery through that registry
	// (ChannelSenderDeliverer wrapping gw) for breaker + audit + retries.
	hbIntegration.SetDeliveryRegistry(gw.monitoring.DeliveryRegistry)
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
		"brain_enabled", gw.cognition.BrainEnabled(),
		"reflection_enabled", gw.cognition.ReflectionStore != nil,
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

// ShutdownManager returns the gateway's shutdown manager for external callers.
func (g *Gateway) ShutdownManager() *ShutdownManager {
	return g.shutdownMgr
}

// promptQuietHours returns the agent_heartbeat quiet window for the prompt's
// Time Context hint, inheriting the top-level timezone when the heartbeat
// block has none. conduit-31jg.60
func promptQuietHours(cfg *config.Config) *config.AgentHeartbeatConfig {
	hb := cfg.AgentHeartbeat
	if hb.Timezone == "" {
		hb.Timezone = cfg.Timezone
	}
	return &hb
}
