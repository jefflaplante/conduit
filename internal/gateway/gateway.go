package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/auth"
	"conduit/internal/channels"
	"conduit/internal/channels/telegram"
	tuiAdapter "conduit/internal/channels/tui"
	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/heartbeat"
	"conduit/internal/logging"
	"conduit/internal/middleware"
	"conduit/internal/monitoring"
	"conduit/internal/mqtt"
	"conduit/internal/scheduler"
	"conduit/internal/searchdb"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	internalssh "conduit/internal/ssh"
	"conduit/internal/tools"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
	"conduit/internal/tui"
	vecgoservice "conduit/internal/vecgo"
	"conduit/internal/vecgo/embedding"
	"conduit/internal/version"
	"conduit/internal/workspace"
	"conduit/internal/protocol"

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

	// Authentication
	authStorage     *auth.TokenStorage
	authMiddleware  *middleware.AuthMiddleware
	wsAuthenticator *middleware.WebSocketAuthenticator

	// Rate limiting
	rateLimitMiddleware *middleware.RateLimitMiddleware

	// Monitoring and heartbeat
	gatewayMetrics       *monitoring.GatewayMetrics
	metricsCollector     monitoring.MetricsCollectorInterface
	heartbeatService     *monitoring.HeartbeatService
	heartbeatIntegration heartbeat.HeartbeatIntegrationInterface
	eventStore           monitoring.EventStore

	// WebSocket handling
	upgrader    websocket.Upgrader
	clients     map[string]*Client
	clientMu    sync.RWMutex
	wsConnCount atomic.Int32    // active WebSocket connection count
	ctx         context.Context // gateway lifecycle context (for WebSocket handlers)

	// Active request tracking for /stop
	activeRequests   map[string]context.CancelFunc // sessionKey -> cancel function
	activeRequestsMu sync.RWMutex

	// FTS5 full-text search
	ftsIndexer  *fts.Indexer
	ftsSearcher *fts.Searcher

	// Search database (separate from gateway.db)
	searchDB       *searchdb.SearchDB
	beadsIndexer   *searchdb.BeadsIndexer
	messageSyncer  *searchdb.MessageSyncer
	asyncMsgSyncer *searchdb.AsyncMessageSyncer

	// Vector/semantic search (optional)
	vectorService *vecgoservice.Service

	// MQTT event ingest (optional)
	mqttService *mqtt.Service

	// SSH server (optional)
	sshServer *charmssh.Server

	// Debug ring buffer (for /ring command)
	ringBuffer *debuglog.RingBuffer
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
}

// channelStatusAdapter wraps the channel manager to implement schema.ChannelStatusGetter
type channelStatusAdapter struct {
	manager *channels.Manager
}

// GetStatus implements schema.ChannelStatusGetter
func (a *channelStatusAdapter) GetStatus() map[string]interface{} {
	result := make(map[string]interface{})
	for id, status := range a.manager.GetStatus() {
		result[id] = map[string]interface{}{
			"status":  string(status.Status),
			"message": status.Message,
			"name":    id, // Use ID as name for now
		}
	}
	return result
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

	// Initialize summary manager for AI-powered workspace summarization (small-context models)
	if cfg.Workspace.Summary.Enabled && workspaceContext != nil {
		logger.Info("initializing workspace summary manager")
		summaryExecutor := workspace.NewSummaryExecutor(
			newSummaryAIRouterAdapter(aiRouter),
			cfg.Workspace.Summary.Model,
		)
		fallbackToTruncate := true
		if cfg.Workspace.Summary.FallbackToTruncate != nil {
			fallbackToTruncate = *cfg.Workspace.Summary.FallbackToTruncate
		}
		summaryConfig := workspace.SummaryConfig{
			Enabled:            cfg.Workspace.Summary.Enabled,
			Model:              cfg.Workspace.Summary.Model,
			TargetRatio:        cfg.Workspace.Summary.TargetRatio,
			CacheDir:           cfg.Workspace.Summary.CacheDir,
			CacheTTLHours:      cfg.Workspace.Summary.CacheTTLHours,
			FallbackToTruncate: fallbackToTruncate,
			FileConfigs:        convertSummaryFileConfigs(cfg.Workspace.Summary.FileConfigs),
		}
		if summaryConfig.Model == "" {
			summaryConfig.Model = "claude-haiku-4-5-20251001"
		}
		if summaryConfig.TargetRatio == 0 {
			summaryConfig.TargetRatio = 0.25
		}
		if summaryConfig.CacheDir == "" {
			summaryConfig.CacheDir = ".summaries"
		}
		if summaryConfig.CacheTTLHours == 0 {
			summaryConfig.CacheTTLHours = 168
		}
		summaryManager := workspace.NewSummaryManager(
			cfg.Workspace.ContextDir,
			summaryExecutor,
			summaryConfig,
		)
		agentSystem.SetSummaryManager(summaryManager)
		logger.Info("workspace summary manager initialized",
			"model", summaryConfig.Model,
			"target_ratio_percent", summaryConfig.TargetRatio*100)
	}

	// Initialize context compaction engine if enabled
	var compactionEngine *ai.CompactionEngine
	if cfg.AI.Compaction != nil && cfg.AI.Compaction.Enabled {
		compactionEngine = ai.NewCompactionEngine(aiRouter, sessionStore, *cfg.AI.Compaction)
		logger.Info("context compaction enabled",
			"threshold_percent", cfg.AI.Compaction.Threshold*100,
			"model", cfg.AI.Compaction.Model,
			"keep_messages", cfg.AI.Compaction.RecentMessagesToKeep)
	}

	// Initialize authentication system using the same database
	authStorage := auth.NewTokenStorage(sessionStore.DB(), cfg.Auth.TokenSecret)

	// Build auth skip paths based on diagnostics config
	// By default, require auth for /metrics, /diagnostics, /prometheus
	// /health is configurable for load balancer compatibility
	var authSkipPaths []string
	if cfg.Diagnostics.IsHealthPublic() {
		authSkipPaths = append(authSkipPaths, "/health")
	}
	if !cfg.Diagnostics.RequireAuth {
		// Legacy behavior: all diagnostic endpoints are public
		authSkipPaths = append(authSkipPaths, "/metrics", "/diagnostics", "/prometheus")
	}

	// Create auth middleware
	authMiddleware := middleware.NewAuthMiddleware(authStorage, middleware.AuthMiddlewareConfig{
		SkipPaths: authSkipPaths,
		OnAuthError: func(r *http.Request, err middleware.AuthError) {
			logging.Warn(r.Context(), "authentication failed",
				"method", r.Method,
				"path", r.URL.Path,
				"code", err.Code)
		},
	})

	// Create WebSocket authenticator
	wsAuthenticator := middleware.NewWebSocketAuthenticator(authStorage)

	// Create rate limiting middleware
	rateLimitMiddleware := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Config: middleware.RateLimitConfig{
			Enabled: cfg.RateLimiting.Enabled,
			Anonymous: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: cfg.RateLimiting.Anonymous.WindowSeconds,
				MaxRequests:   cfg.RateLimiting.Anonymous.MaxRequests,
			},
			Authenticated: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: cfg.RateLimiting.Authenticated.WindowSeconds,
				MaxRequests:   cfg.RateLimiting.Authenticated.MaxRequests,
			},
			CleanupIntervalSeconds: cfg.RateLimiting.CleanupIntervalSeconds,
		},
		OnRateLimitExceeded: func(r *http.Request, identifier string, isAnonymous bool) {
			clientType := "authenticated_client"
			if isAnonymous {
				clientType = "anonymous_ip"
			}
			logging.Warn(r.Context(), "rate limit exceeded",
				"method", r.Method,
				"path", r.URL.Path,
				"identifier", identifier,
				"client_type", clientType)
		},
	})

	// Initialize monitoring system
	gatewayMetrics := monitoring.NewGatewayMetrics()
	gatewayMetrics.SetVersion(version.Info())

	// Create event store for heartbeat events
	eventStore := monitoring.NewMemoryEventStore(1000)

	// Create metrics collector
	metricsCollector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   sessionStore,
		GatewayMetrics: gatewayMetrics,
	})

	// Create heartbeat service
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
		authStorage:         authStorage,
		authMiddleware:      authMiddleware,
		wsAuthenticator:     wsAuthenticator,
		rateLimitMiddleware: rateLimitMiddleware,
		gatewayMetrics:      gatewayMetrics,
		metricsCollector:    metricsCollector,
		heartbeatService:    heartbeatService,
		eventStore:          eventStore,
		clients:             make(map[string]*Client),
		activeRequests:      make(map[string]context.CancelFunc),
		ringBuffer:          debugBuffer,
		upgrader: websocket.Upgrader{
			CheckOrigin:  checkOrigin(cfg.AllowedOrigins),
			Subprotocols: []string{"conduit-auth"},
		},
	}

	// Register token revocation handler to close WebSocket connections
	// using a revoked token.
	authStorage.OnRevoke(gw.handleTokenRevocation)

	// Initialize channel manager and register factories
	gw.channelManager = channels.NewManager()
	gw.channelManager.RegisterFactory(telegram.NewFactoryWithDB(sessionStore.DB()))
	gw.channelManager.RegisterFactory(tuiAdapter.NewFactory(nil)) // TUI factory for dynamic adapter creation

	// Now inject dependencies into tools registry to break the cycle
	// This triggers tool registration

	// Initialize search database (separate from gateway.db)
	// This consolidates FTS5 indices into search.db for better separation of concerns
	ftsWorkspaceDir := cfg.Workspace.ContextDir
	if ftsWorkspaceDir == "" {
		ftsWorkspaceDir = "./workspace"
	}

	var ftsIndexer *fts.Indexer
	var ftsSearcher *fts.Searcher

	if cfg.Search.IsEnabled() {
		searchDBPath := cfg.Search.Path // Empty means derive from gateway.db path
		sdb, err := searchdb.NewSearchDB(searchDBPath, cfg.Database.Path, sessionStore.DB())
		if err != nil {
			logger.Warn("failed to initialize search database, falling back to gateway.db", "error", err)
			// Fall back to using gateway.db for FTS (backward compatibility)
			ftsIndexer = fts.NewIndexer(sessionStore.DB(), ftsWorkspaceDir)
			ftsSearcher = fts.NewSearcher(sessionStore.DB())
		} else {
			gw.searchDB = sdb

			// Use search.db for FTS operations
			ftsIndexer = fts.NewIndexer(sdb.DB(), ftsWorkspaceDir)
			ftsSearcher = fts.NewSearcher(sdb.DB())

			// Initialize beads indexer
			beadsDir := cfg.Search.BeadsDir
			if beadsDir == "" {
				beadsDir = ".beads"
			}
			gw.beadsIndexer = searchdb.NewBeadsIndexer(sdb.DB(), beadsDir)

			// Initialize message syncer and wire callbacks
			gw.messageSyncer = searchdb.NewMessageSyncer(sdb.DB(), sessionStore.DB())
			gw.asyncMsgSyncer = searchdb.NewAsyncMessageSyncer(gw.messageSyncer, 256)
			sessionStore.SetMessageCallbacks(
				gw.asyncMsgSyncer.MessageAddedCallback(),  // non-blocking
				gw.messageSyncer.SessionClearedCallback(), // session clear stays synchronous (rare)
			)

			// Run initial sync operations
			indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)

			// Sync messages from gateway.db to search.db
			if err := gw.messageSyncer.FullSync(indexCtx); err != nil {
				logger.Warn("initial message sync failed", "error", err)
			}

			// Index beads
			if err := gw.beadsIndexer.IndexBeads(indexCtx); err != nil {
				logger.Warn("initial beads indexing failed", "error", err)
			}

			indexCancel()
			logger.Info("search database initialized", "path", sdb.Path())
		}
	} else {
		// Search disabled - use gateway.db (backward compatibility)
		ftsIndexer = fts.NewIndexer(sessionStore.DB(), ftsWorkspaceDir)
		ftsSearcher = fts.NewSearcher(sessionStore.DB())
		logger.Debug("search database disabled, using gateway.db for FTS")
	}

	gw.ftsIndexer = ftsIndexer
	gw.ftsSearcher = ftsSearcher

	// Run initial workspace indexing
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := ftsIndexer.IndexWorkspace(indexCtx); err != nil {
		logger.Warn("initial FTS5 workspace indexing failed", "error", err)
	}
	indexCancel()

	// Initialize optional vector/semantic search service
	if cfg.Vector.Enabled {
		vectorDBPath := cfg.Vector.Path
		if vectorDBPath == "" {
			vectorDBPath = config.DeriveVectorDBPath(cfg.Database.Path)
		}
		vecCfg := vecgoservice.Config{
			DBPath:    vectorDBPath,
			ChunkSize: cfg.Vector.ChunkSize,
			EmbedDims: cfg.Vector.EmbedDims,
		}

		// Select embedding provider
		switch cfg.Vector.EmbedProvider {
		case "openai":
			if cfg.Vector.OpenAI != nil && cfg.Vector.OpenAI.APIKey != "" {
				vecCfg.Embedder = embedding.NewOpenAIEmbedder(
					cfg.Vector.OpenAI.APIKey,
					cfg.Vector.OpenAI.Model,
					cfg.Vector.EmbedDims,
				)
				logger.Info("using OpenAI embeddings", "model", vecCfg.Embedder.Name())
			} else {
				logger.Warn("OpenAI embeddings configured but no API key, falling back to TF-IDF")
			}
		default:
			// TF-IDF is the default, no action needed (vecgo service handles it)
		}

		vectorSvc, vecErr := vecgoservice.NewService(vecCfg)
		if vecErr != nil {
			logger.Warn("failed to initialize vector search, continuing without", "error", vecErr)
		} else {
			gw.vectorService = vectorSvc
			indexWorkspaceForVector(ftsWorkspaceDir, vectorSvc)
			logger.Info("vector search initialized", "path", vectorDBPath)
		}
	}

	// Initialize optional MQTT event ingest service
	if cfg.MQTT.Enabled {
		gw.mqttService = mqtt.NewService(cfg.MQTT)
		logger.Info("MQTT service configured", "broker", cfg.MQTT.BrokerURL, "topic_count", len(cfg.MQTT.Topics))
	}

	// Create schema builder with discovery providers for enhanced tool schemas
	schemaBuilder := createSchemaBuilder(gw, cfg)

	// Build VectorService interface value (nil if disabled)
	var vectorSearch types.VectorService
	if gw.vectorService != nil {
		vectorSearch = gw.vectorService
	}

	// Build MQTTService interface value (nil if disabled)
	var mqttSvc types.MQTTService
	if gw.mqttService != nil {
		mqttSvc = mqtt.NewServiceAdapter(gw.mqttService)
	}

	toolServices := &tools.ToolServices{
		SessionStore:  sessionStore,
		ConfigMgr:     cfg,
		WebClient:     &http.Client{Timeout: 30 * time.Second},
		ChannelSender: gw, // Gateway implements ChannelSender interface
		Gateway:       gw, // Gateway implements GatewayService interface
		Searcher:      ftsSearcher,
		VectorSearch:  vectorSearch,
		MQTTService:   mqttSvc,
		SchemaBuilder: schemaBuilder,
		DebugLog:      debugBuffer,
	}
	toolsRegistry.SetServices(toolServices)

	// NOW convert tools to AI format (after SetServices registered them)
	// Note: skill tools are already included via the registry (registered by registerSkillTools).
	// The agent's GetToolDefinitions() also adds skills dynamically for per-session filtering.
	aiTools := convertToolsToAIFormat(toolsRegistry)

	// Update agent with the now-registered tools
	agentSystem.SetTools(aiTools)

	// Initialize scheduler
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	gw.scheduler = scheduler.New(workspaceDir, gw.executeScheduledJob)

	// Initialize heartbeat integration
	gw.heartbeatIntegration = heartbeat.NewGatewayIntegration(workspaceDir, sessionStore, aiRouter, gw.scheduler, gw, metricsCollector, cfg.AgentHeartbeat.Model, cfg.AgentHeartbeat.TimeoutSeconds)

	// NOTE: initializeAgentHeartbeat is called AFTER scheduler.Start() in the Run() method
	// so that existing jobs are loaded from cron_jobs.json before the heartbeat job is added.

	logger.Info("gateway initialized",
		"agent_name", agentCfg.Name,
		"agent_personality", agentCfg.Personality,
		"workspace_enabled", workspaceContext != nil,
		"skills_enabled", skillsManager != nil && skillsManager.IsEnabled(),
		"tool_count", len(aiTools),
		"vector_search_enabled", gw.vectorService != nil,
		"mqtt_enabled", gw.mqttService != nil,
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

// executeScheduledJob is called when a Go cron job fires
func (g *Gateway) executeScheduledJob(ctx context.Context, job *scheduler.Job) error {
	g.logger.Info("executing scheduled job", "job_id", job.ID, "command", job.Command)

	// Check if this is a heartbeat job
	if heartbeat.IsHeartbeatJob(job) {
		g.logger.Debug("routing to heartbeat execution framework", "job_id", job.ID)
		return g.heartbeatIntegration.ExecuteHeartbeat(ctx, job)
	}

	// Handle regular cron jobs (existing logic)
	// Create a session for this job
	sessionKey := fmt.Sprintf(agent.CronSessionKeyPrefix+"%s_%d", job.ID, time.Now().UnixNano())
	session, err := g.sessions.GetOrCreateSession("cron", sessionKey)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	// Resolve model alias
	model := job.Model
	if model == "" {
		model = g.getDefaultModel()
	} else if fullModel, exists := g.getModelAliases()[strings.ToLower(model)]; exists && fullModel != "" {
		model = fullModel
	}

	// Store model and skill filter in session context for prompt/tool filtering
	if session.Context == nil {
		session.Context = make(map[string]string)
	}
	session.Context["model"] = model
	if len(job.Skills) > 0 {
		session.Context["skill_filter"] = strings.Join(job.Skills, ",")
	}

	// Execute the job command as an AI prompt
	response, err := g.ai.GenerateResponseWithTools(ctx, session, job.Command, "", model)
	if err != nil {
		return fmt.Errorf("AI execution failed: %w", err)
	}

	// If there's a target, send the result there
	if job.Target != "" {
		responseContent := response.GetContent()

		// Check for silent response patterns - don't send these to the target
		if responseContent == "" || channels.IsSilentResponse(responseContent) {
			g.logger.Debug("job completed with silent response, not sending to target", "job_id", job.ID)
			return nil
		}

		// Target format: "telegram:chatid" or just "chatid"
		parts := strings.SplitN(job.Target, ":", 2)
		var channelID, userID string
		if len(parts) == 2 {
			channelID = parts[0]
			userID = parts[1]
		} else {
			channelID = "telegram"
			userID = job.Target
		}

		outgoingMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf(agent.CronSessionKeyPrefix+"%s_%d", job.ID, time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID: channelID,
			UserID:    userID,
			Text:      responseContent,
		}

		if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
			g.logger.Error("failed to send job output", "job_id", job.ID, "target", job.Target, "error", err)
		}
	}

	g.logger.Info("job completed", "job_id", job.ID, "response_chars", len(response.GetContent()))
	return nil
}

// convertToolsToAIFormat converts tools registry tools to AI format
// deriveRuntimeChannel returns the first enabled channel name, or "websocket" as fallback.
func deriveRuntimeChannel(channels []config.ChannelConfig) string {
	for _, ch := range channels {
		if ch.Enabled {
			return ch.Type
		}
	}
	return "websocket"
}

func convertToolsToAIFormat(registry *tools.Registry) []ai.Tool {
	var aiTools []ai.Tool

	availableTools := registry.GetAvailableTools()
	for _, tool := range availableTools {
		description := tool.Description()
		params := tool.Parameters()

		// Apply schema hints from EnhancedSchemaProvider
		if esp, ok := tool.(types.EnhancedSchemaProvider); ok {
			hints := esp.GetSchemaHints()
			if len(hints) > 0 {
				builder := schema.NewBuilder(nil)
				params = builder.EnhanceSchema(context.Background(), params, hints)
			}
		}

		// Append usage examples to description
		if uep, ok := tool.(types.UsageExampleProvider); ok {
			examples := uep.GetUsageExamples()
			if len(examples) > 0 {
				description += "\n\nUsage examples:"
				for _, ex := range examples {
					description += fmt.Sprintf("\n- %s: %s", ex.Name, ex.Description)
				}
			}
		}

		// Append per-action documentation
		if adp, ok := tool.(types.ActionDocProvider); ok {
			docs := adp.GetActionDocs()
			if len(docs) > 0 {
				description += "\n\nAction details:"
				for action, doc := range docs {
					description += fmt.Sprintf("\n[%s] %s", action, doc.Description)
					if len(doc.RequiredParams) > 0 {
						description += fmt.Sprintf(" Required: %s.", strings.Join(doc.RequiredParams, ", "))
					}
					if len(doc.OptionalParams) > 0 {
						description += fmt.Sprintf(" Optional: %s.", strings.Join(doc.OptionalParams, ", "))
					}
					if doc.Returns != "" {
						description += fmt.Sprintf(" Returns: %s.", doc.Returns)
					}
				}
			}
		}

		aiTool := ai.Tool{
			Name:        tool.Name(),
			Description: description,
			Parameters:  params,
		}
		aiTools = append(aiTools, aiTool)
	}

	return aiTools
}

// createInternalToken generates an authentication token for internal services
// (e.g., the integrated SSH server) that connect back to the gateway via WebSocket.
func (g *Gateway) createInternalToken(clientName string) (string, error) {
	resp, err := g.authStorage.CreateToken(auth.CreateTokenRequest{
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

// Start starts the gateway server
func (g *Gateway) Start(ctx context.Context) error {
	// Store the gateway lifecycle context for WebSocket handlers.
	// HTTP request contexts (r.Context()) are cancelled when the handler returns,
	// which is immediate after WebSocket upgrade. WebSocket goroutines need a
	// context tied to the gateway's lifecycle instead.
	g.ctx = ctx

	// Start HTTP server for WebSocket connections
	mux := http.NewServeMux()

	// Diagnostic endpoints - auth requirement controlled by diagnostics config
	// Auth middleware skip paths are configured at gateway initialization based on config.
	// Default: /health is public (for load balancers), others require auth.
	mux.Handle("/health", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleHealthEnhanced))))
	mux.Handle("/metrics", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleMetrics))))
	mux.Handle("/diagnostics", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleDiagnostics))))
	mux.Handle("/prometheus", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handlePrometheusMetrics))))

	// WebSocket endpoint with custom authentication and rate limiting
	mux.Handle("/ws", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleWebSocket)))

	// Protected API endpoints - wrapped with auth middleware and rate limiting
	// Order: auth middleware first (sets context), then rate limiting (uses context), then handler
	// POST endpoints also get request body size limiting to prevent OOM attacks.
	mux.Handle("/debug/prompt", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleDebugPrompt))))
	mux.Handle("/api/channels/status", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleChannelStatus))))
	mux.Handle("/api/test/message", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(g.handleTestMessage), MaxRequestBodySize))))

	// Vector API endpoints (registered unconditionally; handlers return 503 when disabled)
	vectorAPI := &VectorAPI{vectorService: g.vectorService}
	mux.Handle("/api/vector/search", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleSearch), MaxRequestBodySize))))
	mux.Handle("/api/vector/index", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleIndex), MaxRequestBodySize))))
	mux.Handle("/api/vector/delete", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(
		limitRequestBody(http.HandlerFunc(vectorAPI.handleDelete), MaxRequestBodySize))))
	mux.Handle("/api/vector/status", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleStatus))))

	server := &http.Server{
		Addr:           fmt.Sprintf(":%d", g.config.Port),
		Handler:        mux,
		MaxHeaderBytes: serverMaxHeaderBytes,
		ReadTimeout:    serverReadTimeout,
		WriteTimeout:   serverWriteTimeout,
		IdleTimeout:    serverIdleTimeout,
	}

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
	}

	// Start heartbeat service
	if g.heartbeatService != nil {
		if err := g.heartbeatService.Start(ctx); err != nil {
			g.logger.Warn("failed to start heartbeat service", "error", err)
		}
	}

	// Start session state cleanup loop (prevents memory leak from abandoned sessions)
	stopCleanup := g.sessions.StartStateCleanup(30*time.Minute, 5*time.Minute)
	go func() {
		<-ctx.Done()
		stopCleanup()
	}()

	// Start periodic FTS5 workspace re-indexing (every 5 minutes)
	if g.ftsIndexer != nil {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Re-index workspace documents
					if err := g.ftsIndexer.IndexWorkspace(ctx); err != nil {
						g.logger.Warn("FTS5 periodic re-index failed", "error", err)
					}

					// Re-index beads if available
					if g.beadsIndexer != nil {
						if err := g.beadsIndexer.IndexBeads(ctx); err != nil {
							g.logger.Warn("beads periodic re-index failed", "error", err)
						}
					}

					// Run incremental message sync as safety net
					if g.messageSyncer != nil {
						if err := g.messageSyncer.IncrementalSync(ctx); err != nil {
							g.logger.Warn("message incremental sync failed", "error", err)
						}
					}
				}
			}
		}()
	}

	// Start MQTT service if configured
	if g.mqttService != nil {
		if err := g.mqttService.Start(ctx); err != nil {
			g.logger.Warn("failed to start MQTT service", "error", err)
		} else {
			g.logger.Info("MQTT service started")
		}
	}

	// Start SSH server if configured
	if g.config.SSH.Enabled {
		// Build shell security config from gateway config (SSH mode)
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
					Metrics:      g.metricsCollector,
					ModelAliases: g.getModelAliases(),
					AgentName:    g.config.Agent.Name,
					Version:      version.Info(),
					GitCommit:    version.GitCommit,
					UptimeFunc:   func() int64 { return int64(g.gatewayMetrics.GetUptime().Seconds()) },
					ToolCount:    toolCount,
					SkillCount:   skillCount,
				})
			},
		}
		sshServer, err := internalssh.NewServer(sshConfig)
		if err != nil {
			g.logger.Warn("failed to create SSH server", "error", err)
		} else {
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
	}

	// Start message processing goroutine
	go g.processMessages(ctx)

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			g.logger.Error("HTTP server error", "error", err)
		}
	}()

	g.logger.Info("gateway started", "port", g.config.Port)

	// Wait for context cancellation
	<-ctx.Done()

	// Graceful shutdown
	g.logger.Info("shutting down gateway")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		g.logger.Error("server shutdown error", "error", err)
	}

	g.stopChannels()

	// Stop SSH server
	if g.sshServer != nil {
		g.logger.Debug("stopping SSH server")
		g.sshServer.Close()
	}

	// Stop heartbeat service
	if g.heartbeatService != nil {
		if err := g.heartbeatService.Stop(); err != nil {
			g.logger.Error("error stopping heartbeat service", "error", err)
		}
	}

	// Stop scheduler
	if g.scheduler != nil {
		g.scheduler.Stop()
	}

	// Stop rate limiting middleware
	if g.rateLimitMiddleware != nil {
		g.rateLimitMiddleware.Stop()
	}

	// Drain async message syncer before closing search DB
	if g.asyncMsgSyncer != nil {
		g.asyncMsgSyncer.Close()
	}

	// Stop MQTT service
	if g.mqttService != nil {
		g.mqttService.Stop()
	}

	// Close vector search service
	if g.vectorService != nil {
		if err := g.vectorService.Close(); err != nil {
			g.logger.Error("error closing vector service", "error", err)
		}
	}

	return nil
}

// checkOrigin returns a function that validates WebSocket Origin headers.
// If allowedOrigins is non-empty, only those origins (case-insensitive) are accepted.
// If allowedOrigins is empty, requests with no Origin header or localhost origins are accepted.
func checkOrigin(allowedOrigins []string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		// No Origin header means same-origin (non-browser or same-origin browser request)
		if origin == "" {
			return true
		}

		originLower := strings.ToLower(origin)

		// If explicit allowlist is configured, check against it
		if len(allowedOrigins) > 0 {
			for _, allowed := range allowedOrigins {
				if strings.EqualFold(origin, allowed) {
					return true
				}
			}
			logging.Warn(r.Context(), "WebSocket origin rejected",
				"origin", origin,
				"reason", "not in allowed origins")
			return false
		}

		// Default policy: allow localhost origins only
		for _, prefix := range []string{
			"http://localhost",
			"https://localhost",
			"http://127.0.0.1",
			"https://127.0.0.1",
			"http://[::1]",
			"https://[::1]",
		} {
			if originLower == prefix || strings.HasPrefix(originLower, prefix+":") {
				return true
			}
		}

		logging.Warn(r.Context(), "WebSocket origin rejected",
			"origin", origin,
			"reason", "only localhost permitted")
		return false
	}
}

// handleWebSocket handles WebSocket connections with authentication
func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check WebSocket connection limit before doing any work
	if g.wsConnCount.Load() >= MaxWebSocketConnections {
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected: limit reached",
			"current", g.wsConnCount.Load(),
			"max", MaxWebSocketConnections)
		return
	}

	// Authenticate the WebSocket upgrade request
	authResult := g.wsAuthenticator.Authenticate(r)
	if !authResult.Authenticated {
		g.wsAuthenticator.RejectUpgrade(w, authResult.Error)
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
	if count := g.wsConnCount.Add(1); count > MaxWebSocketConnections {
		g.wsConnCount.Add(-1)
		http.Error(w, "Too many WebSocket connections", http.StatusServiceUnavailable)
		g.logger.Warn("WebSocket connection rejected (race): limit reached",
			"current", count-1,
			"max", MaxWebSocketConnections)
		return
	}

	conn, err := g.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		g.wsConnCount.Add(-1) // Decrement on upgrade failure
		g.logger.Error("WebSocket upgrade error", "error", err)
		return
	}

	client := &Client{
		ID:      fmt.Sprintf("client_%d", time.Now().UnixNano()),
		Role:    authResult.AuthInfo.ClientName, // Store authenticated client name
		UserID:  authResult.AuthInfo.ClientName, // Default user identity from auth
		TokenID: authResult.AuthInfo.TokenID,    // Track token for revocation
		Conn:    conn,
		Send:    make(chan []byte, 256),
	}

	g.clientMu.Lock()
	g.clients[client.ID] = client
	clientCount := len(g.clients)
	g.clientMu.Unlock()

	// Update metrics
	if g.metricsCollector != nil {
		g.metricsCollector.UpdateWebSocketConnections(clientCount)
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
		UptimeSeconds: int64(g.gatewayMetrics.GetUptime().Seconds()),
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
func (g *Gateway) handleTokenRevocation(tokenID string) {
	g.clientMu.RLock()
	var targets []*Client
	for _, c := range g.clients {
		if c.TokenID == tokenID {
			targets = append(targets, c)
		}
	}
	g.clientMu.RUnlock()

	for _, c := range targets {
		g.logger.Debug("closing connection for revoked token",
			"client_id", c.ID,
			"token_id", tokenID)
		closeMsg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "token revoked")
		_ = c.Conn.WriteControl(websocket.CloseMessage, closeMsg, time.Now().Add(time.Second))
		_ = c.Conn.Close()
	}

	if len(targets) > 0 {
		g.logger.Info("closed connections for revoked token",
			"connection_count", len(targets),
			"token_id", tokenID)
	}
}

// handleChannelStatus provides channel status information
func (g *Gateway) handleChannelStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := g.channelManager.GetStatus()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Simple JSON encoding (in production, use json.Marshal)
	response := "{\n"
	first := true
	for id, channelStatus := range status {
		if !first {
			response += ",\n"
		}
		response += fmt.Sprintf(`  "%s": {
    "status": "%s",
    "message": "%s",
    "timestamp": "%s"
  }`, id, channelStatus.Status, channelStatus.Message, channelStatus.Timestamp.Format(time.RFC3339))
		first = false
	}
	response += "\n}"

	w.Write([]byte(response))
}

// handleClientRead handles incoming messages from a WebSocket client
func (g *Gateway) handleClientRead(ctx context.Context, client *Client) {
	defer func() {
		g.clientMu.Lock()
		delete(g.clients, client.ID)
		clientCount := len(g.clients)
		g.clientMu.Unlock()

		// Decrement active WebSocket connection count
		g.wsConnCount.Add(-1)

		// Update metrics
		if g.metricsCollector != nil {
			g.metricsCollector.UpdateWebSocketConnections(clientCount)
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
			go g.handleWebSocketChat(ctx, client, msg)
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

// handleClientWrite handles outgoing messages to a WebSocket client.
// It monitors both the client's Send channel and the gateway's lifecycle context (g.ctx).
// When the gateway shuts down, it sends a WebSocket close message and exits,
// preventing goroutine leaks from long-lived connections.
func (g *Gateway) handleClientWrite(client *Client) {
	defer client.Conn.Close()

	for {
		select {
		case message, ok := <-client.Send:
			if !ok {
				// Send channel closed; send WebSocket close frame and exit.
				client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				g.logger.Warn("WebSocket write error", "error", err)
				return
			}
		case <-g.ctx.Done():
			// Gateway is shutting down; send close frame and exit.
			client.Conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"))
			return
		}
	}
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
			go g.handleIncomingMessage(ctx, msg)

		case <-ctx.Done():
			return
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
	if g.metricsCollector != nil {
		g.metricsCollector.MarkActivity()
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

	// Add user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", msg.Text, msg.Metadata)
	if err != nil {
		logging.Error(ctx, "error saving user message", "error", err)
		return
	}

	// Start typing indicator loop (refreshes every 4 seconds until done)
	typingDone := make(chan struct{})
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

		// Track this request so /stop can cancel it
		g.activeRequestsMu.Lock()
		g.activeRequests[session.Key] = cancel
		requestCount := len(g.activeRequests)
		g.activeRequestsMu.Unlock()

		// Update metrics
		if g.metricsCollector != nil {
			g.metricsCollector.UpdateActiveRequests(requestCount)
		}

		// Ensure we clean up when done
		defer func() {
			g.activeRequestsMu.Lock()
			delete(g.activeRequests, session.Key)
			finalRequestCount := len(g.activeRequests)
			g.activeRequestsMu.Unlock()

			// Update metrics on cleanup
			if g.metricsCollector != nil {
				g.metricsCollector.UpdateActiveRequests(finalRequestCount)
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

				convResponse, err = g.ai.GenerateResponseStreaming(reqCtx, session, msg.Text, providerOverride, modelOverride, onDelta)

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
							streamingAdapter.EditMessageText(chatID, placeholderMsgID, finalContent)
						}
						streamingUsed = true
					} else if streamedLength > 0 {
						// Fallback to streamed text if no final content
						streamedText := textBuilder.String()
						// Also check streamed text for silent patterns
						if channels.IsSilentResponse(streamedText) {
							logging.Debug(reqCtx, "streaming: silent response in streamed text, deleting placeholder")
							if deleteErr := streamingAdapter.DeleteMessage(chatID, placeholderMsgID); deleteErr != nil {
								logging.Warn(reqCtx, "streaming: failed to delete placeholder", "error", deleteErr)
							}
						} else {
							streamedText = channels.SanitizeOutgoingText(streamedText)
							logging.Debug(reqCtx, "streaming: using streamed text only", "chars", streamedLength)
							if streamedText != "" {
								streamingAdapter.EditMessageText(chatID, placeholderMsgID, streamedText)
							}
						}
						streamingUsed = true
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

			convResponse, err = g.ai.GenerateResponseWithToolsAndProgress(reqCtx, session, msg.Text, providerOverride, modelOverride, onProgress)
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
				Text:       "Sorry, I encountered an error processing your message.",
			}

			g.channelManager.SendMessage(errorMsg)
			return
		}

		if !typingClosed {
			close(typingDone) // Stop typing indicator
		}

		responseContent := convResponse.GetContent()

		// Persist usage to session context for /context command
		if usage := convResponse.GetUsage(); usage != nil {
			batch := map[string]string{
				"last_prompt_tokens":     strconv.Itoa(usage.PromptTokens),
				"last_completion_tokens": strconv.Itoa(usage.CompletionTokens),
				"last_total_tokens":      strconv.Itoa(usage.TotalTokens),
			}

			// Proactive context window warning
			if warning := contextWarningIfNeeded(session, usage.PromptTokens, modelOverride); warning.Text != "" {
				responseContent += warning.Text
				batch[warning.Key] = "true"
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

// limitRequestBody wraps a handler to enforce a maximum request body size.
// Requests that exceed the limit will receive a 413 Payload Too Large error.
func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// handleTestMessage provides a test endpoint for sending messages without Telegram
func (g *Gateway) handleTestMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse request body
	var req struct {
		Message string `json:"message"`
		UserID  string `json:"user_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	if req.UserID == "" {
		req.UserID = "test_user"
	}

	// Get or create session
	session, err := g.sessions.GetOrCreateSession(req.UserID, "test")
	if err != nil {
		g.logger.Error("test message: error creating session", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", req.Message, nil)
	if err != nil {
		g.logger.Error("test message: error saving user message", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Generate AI response
	if g.ai == nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	// Use GenerateResponseWithTools to enable tool execution
	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]
	convResponse, err := g.ai.GenerateResponseWithTools(ctx, session, req.Message, providerOverride, modelOverride)
	if err != nil {
		g.logger.Error("test message: error generating AI response", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Add AI response to session
	_, err = g.sessions.AddMessage(session.Key, "assistant", convResponse.GetContent(), nil)
	if err != nil {
		g.logger.Error("error saving AI message", "error", err)
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"response": convResponse.GetContent(),
		"usage":    convResponse.GetUsage(),
		"steps":    convResponse.GetSteps(),
	})
}

// createSchemaBuilder creates a schema builder with discovery providers
func createSchemaBuilder(gw *Gateway, cfg *config.Config) *schema.Builder {
	providers := make(map[string]schema.DiscoveryProvider)

	// Add channel discovery provider
	if gw != nil && gw.channelManager != nil {
		channelProvider := schema.NewChannelDiscoveryProvider(&channelStatusAdapter{manager: gw.channelManager})
		providers["channels"] = channelProvider
	}

	// Add workspace discovery provider
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	allowedPaths := cfg.Tools.Sandbox.AllowedPaths
	workspaceProvider := schema.NewWorkspaceDiscoveryProvider(workspaceDir, allowedPaths)
	providers["workspace_paths"] = workspaceProvider

	return schema.NewBuilder(providers)
}

// indexWorkspaceForVector walks workspace .md files and indexes them into the vector service.
func indexWorkspaceForVector(workspaceDir string, svc *vecgoservice.Service) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := logging.Default()
	var indexed int
	err := filepath.Walk(workspaceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.Debug("vecgo: skip file", "path", path, "error", readErr)
			return nil
		}
		relPath, _ := filepath.Rel(workspaceDir, path)
		if relPath == "" {
			relPath = path
		}
		meta := map[string]string{
			"source": "workspace",
			"path":   relPath,
			"title":  strings.TrimSuffix(info.Name(), ".md"),
		}
		if indexErr := svc.Index(ctx, relPath, string(data), meta); indexErr != nil {
			logger.Warn("vecgo: index failed", "path", relPath, "error", indexErr)
			return nil
		}
		indexed++
		return nil
	})
	if err != nil {
		logger.Warn("vector workspace indexing walk error", "error", err)
	}
	if indexed > 0 {
		if saveErr := svc.Save(ctx); saveErr != nil {
			logger.Warn("vector index save failed", "error", saveErr)
		}
		logger.Info("vector search indexed workspace files", "file_count", indexed)
	}
}

// summaryAIRouterAdapter adapts ai.Router to workspace.SummaryAIRouter
type summaryAIRouterAdapter struct {
	router *ai.Router
}

// newSummaryAIRouterAdapter creates a new adapter
func newSummaryAIRouterAdapter(router *ai.Router) *summaryAIRouterAdapter {
	return &summaryAIRouterAdapter{router: router}
}

// GenerateSimpleResponse generates a simple AI response without tools
func (a *summaryAIRouterAdapter) GenerateSimpleResponse(ctx context.Context, prompt, model string) (workspace.SummaryAIResponse, error) {
	// Create a minimal session for the summarization request
	tempSession := &sessions.Session{
		Key:     "summary_temp",
		Context: map[string]string{"model": model},
	}

	// Use GenerateResponse without tools for simple summarization
	// The 4th param is provider name (empty = default), model is set via session context
	response, err := a.router.GenerateResponse(ctx, tempSession, prompt, "")
	if err != nil {
		return nil, err
	}

	return &summaryAIResponseAdapter{content: response.Content}, nil
}

// summaryAIResponseAdapter adapts ai.GenerateResponse to workspace.SummaryAIResponse
type summaryAIResponseAdapter struct {
	content string
}

// GetContent returns the response content
func (a *summaryAIResponseAdapter) GetContent() string {
	return a.content
}

// convertSummaryFileConfigs converts config types to workspace types
func convertSummaryFileConfigs(cfgConfigs map[string]config.SummaryFileConfig) map[string]workspace.SummaryFileConfig {
	if len(cfgConfigs) == 0 {
		return nil
	}
	result := make(map[string]workspace.SummaryFileConfig, len(cfgConfigs))
	for filename, cfg := range cfgConfigs {
		result[filename] = workspace.SummaryFileConfig{
			Ratio:        cfg.Ratio,
			PreserveKeys: cfg.PreserveKeys,
		}
	}
	return result
}
