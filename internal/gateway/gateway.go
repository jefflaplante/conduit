package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	"conduit/internal/middleware"
	"conduit/internal/monitoring"
	"conduit/internal/scheduler"
	"conduit/internal/searchdb"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	internalssh "conduit/internal/ssh"
	"conduit/internal/tools"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
	"conduit/internal/tui"
	vecgoservice "conduit/internal/vecgo"
	"conduit/internal/vecgo/embedding"
	"conduit/internal/version"
	"conduit/internal/workspace"
	"conduit/pkg/protocol"

	charmssh "github.com/charmbracelet/ssh"
)

// Gateway represents the core Conduit gateway
type Gateway struct {
	config           *config.Config
	sessions         *sessions.Store
	ai               *ai.Router
	agentSystem      *agent.ConduitAgentWithIntegration
	tools            *tools.Registry
	workspaceContext *workspace.WorkspaceContext
	skillsManager    *skills.Manager
	channelManager   *channels.Manager
	scheduler        scheduler.SchedulerInterface

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
	upgrader websocket.Upgrader
	clients  map[string]*Client
	clientMu sync.RWMutex
	ctx      context.Context // gateway lifecycle context (for WebSocket handlers)

	// Active request tracking for /stop
	activeRequests   map[string]context.CancelFunc // sessionKey -> cancel function
	activeRequestsMu sync.RWMutex

	// FTS5 full-text search
	ftsIndexer  *fts.Indexer
	ftsSearcher *fts.Searcher

	// Search database (separate from gateway.db)
	searchDB      *searchdb.SearchDB
	beadsIndexer  *searchdb.BeadsIndexer
	messageSyncer *searchdb.MessageSyncer

	// Vector/semantic search (optional)
	vectorService *vecgoservice.Service

	// SSH server (optional)
	sshServer *charmssh.Server
}

// Client represents a WebSocket client connection
type Client struct {
	ID         string
	Role       string // "client" or "node"
	UserID     string // user identity for session scoping
	SessionKey string // active session key for this client
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
	// Initialize session store
	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to create session store: %w", err)
	}

	// Initialize workspace context if configured
	var workspaceContext *workspace.WorkspaceContext
	if cfg.Workspace.ContextDir != "" {
		log.Printf("Initializing workspace context from: %s", cfg.Workspace.ContextDir)
		workspaceContext = workspace.NewWorkspaceContext(cfg.Workspace.ContextDir)
	} else {
		log.Println("WARNING: No workspace context directory configured")
	}

	// Initialize skills manager if configured
	var skillsManager *skills.Manager
	if cfg.Skills.Enabled {
		log.Println("Initializing skills manager...")
		skillsManager = skills.NewManager(cfg.Skills)

		// Initialize skills manager
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := skillsManager.Initialize(ctx); err != nil {
			log.Printf("WARNING: Failed to initialize skills manager: %v", err)
			// Continue without skills rather than failing completely
			skillsManager = nil
		} else {
			skillCount := 0
			if availableSkills, err := skillsManager.GetAvailableSkills(ctx); err == nil {
				skillCount = len(availableSkills)
			}
			log.Printf("Skills manager initialized with %d skills", skillCount)
		}
	} else {
		log.Println("Skills system disabled in configuration")
	}

	// Initialize tools registry (tools will be registered after SetServices)
	toolsRegistry := tools.NewRegistry(cfg.Tools)

	// Initialize agent config (tools will be set after gateway is created)
	agentCfg := agent.AgentConfig{
		Name:        cfg.Agent.Name,
		Personality: cfg.Agent.Personality,
		Identity: agent.IdentityConfig{
			OAuthIdentity:  cfg.Agent.Identity.OAuthIdentity,
			APIKeyIdentity: cfg.Agent.Identity.APIKeyIdentity,
		},
		Capabilities: agent.AgentCapabilities{
			MemoryRecall:      cfg.Agent.Capabilities.MemoryRecall,
			ToolChaining:      cfg.Agent.Capabilities.ToolChaining,
			SkillsIntegration: cfg.Agent.Capabilities.SkillsIntegration,
			Heartbeats:        cfg.Agent.Capabilities.Heartbeats,
			SilentReplies:     cfg.Agent.Capabilities.SilentReplies,
		},
	}

	// Use the integrated agent system (tools will be set after gateway is created)
	agentSystem := agent.NewConduitAgentWithIntegration(
		agentCfg,
		nil, // Tools set later after SetServices
		workspaceContext,
		skillsManager,
		cfg.AI.ModelAliases,
	)

	// Initialize the agent system
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := agentSystem.Initialize(ctx); err != nil {
		log.Printf("WARNING: Failed to initialize agent system: %v", err)
	} else {
		log.Println("Agent system initialized successfully")
	}

	// Create tool execution engine with configurable chain limit
	maxToolChains := cfg.Tools.MaxToolChains
	if maxToolChains <= 0 {
		maxToolChains = 25 // Default fallback
	}
	executionEngine := tools.NewExecutionEngine(toolsRegistry, 4, 60*time.Second, maxToolChains)
	executionAdapter := tools.NewExecutionEngineAdapter(executionEngine)

	// Initialize AI router with agent system AND execution engine
	aiRouter, err := ai.NewRouterWithExecution(cfg.AI, agentSystem, executionAdapter)
	if err != nil {
		return nil, fmt.Errorf("failed to create AI router: %w", err)
	}

	// Wire up session store for conversation history
	aiRouter.SetSessionStore(sessionStore)

	log.Println("Tool execution engine wired up")

	// Initialize authentication system using the same database
	authStorage := auth.NewTokenStorage(sessionStore.DB())

	// Create auth middleware (skip health monitoring endpoints)
	authMiddleware := middleware.NewAuthMiddleware(authStorage, middleware.AuthMiddlewareConfig{
		SkipPaths: []string{"/health", "/metrics", "/diagnostics", "/prometheus"},
		OnAuthError: func(r *http.Request, err middleware.AuthError) {
			log.Printf("[Auth] Authentication failed: %s %s (code: %d)",
				r.Method, r.URL.Path, err.Code)
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
			log.Printf("[Gateway] Rate limit exceeded: %s %s (identifier: %s, type: %s)",
				r.Method, r.URL.Path, identifier, map[bool]string{true: "anonymous_ip", false: "authenticated_client"}[isAnonymous])
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
		log.Printf("Heartbeat service configured: %d second interval", cfg.Heartbeat.IntervalSeconds)
	} else {
		log.Println("Heartbeat service disabled in configuration")
	}

	gw := &Gateway{
		config:              cfg,
		sessions:            sessionStore,
		ai:                  aiRouter,
		agentSystem:         agentSystem,
		tools:               toolsRegistry,
		workspaceContext:    workspaceContext,
		skillsManager:       skillsManager,
		channelManager:      nil, // Will be initialized below
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
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// TODO: Implement proper origin checking
				return true
			},
			Subprotocols: []string{"conduit-auth"},
		},
	}

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
			log.Printf("WARNING: Failed to initialize search database: %v (falling back to gateway.db)", err)
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
			sessionStore.SetMessageCallbacks(
				gw.messageSyncer.MessageAddedCallback(),
				gw.messageSyncer.SessionClearedCallback(),
			)

			// Run initial sync operations
			indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)

			// Sync messages from gateway.db to search.db
			if err := gw.messageSyncer.FullSync(indexCtx); err != nil {
				log.Printf("WARNING: Initial message sync failed: %v", err)
			}

			// Index beads
			if err := gw.beadsIndexer.IndexBeads(indexCtx); err != nil {
				log.Printf("WARNING: Initial beads indexing failed: %v", err)
			}

			indexCancel()
			log.Printf("Search database initialized at %s", sdb.Path())
		}
	} else {
		// Search disabled - use gateway.db (backward compatibility)
		ftsIndexer = fts.NewIndexer(sessionStore.DB(), ftsWorkspaceDir)
		ftsSearcher = fts.NewSearcher(sessionStore.DB())
		log.Println("Search database disabled, using gateway.db for FTS")
	}

	gw.ftsIndexer = ftsIndexer
	gw.ftsSearcher = ftsSearcher

	// Run initial workspace indexing
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := ftsIndexer.IndexWorkspace(indexCtx); err != nil {
		log.Printf("WARNING: Initial FTS5 workspace indexing failed: %v", err)
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
				log.Printf("Using OpenAI embeddings (model: %s)", vecCfg.Embedder.Name())
			} else {
				log.Printf("WARNING: OpenAI embeddings configured but no API key; falling back to TF-IDF")
			}
		default:
			// TF-IDF is the default, no action needed (vecgo service handles it)
		}

		vectorSvc, vecErr := vecgoservice.NewService(vecCfg)
		if vecErr != nil {
			log.Printf("WARNING: Failed to initialize vector search: %v (continuing without)", vecErr)
		} else {
			gw.vectorService = vectorSvc
			indexWorkspaceForVector(ftsWorkspaceDir, vectorSvc)
			log.Printf("Vector search initialized at %s", vectorDBPath)
		}
	}

	// Create schema builder with discovery providers for enhanced tool schemas
	schemaBuilder := createSchemaBuilder(gw, cfg)

	// Build VectorService interface value (nil if disabled)
	var vectorSearch types.VectorService
	if gw.vectorService != nil {
		vectorSearch = gw.vectorService
	}

	toolServices := &tools.ToolServices{
		SessionStore:  sessionStore,
		ConfigMgr:     cfg,
		WebClient:     &http.Client{Timeout: 30 * time.Second},
		ChannelSender: gw, // Gateway implements ChannelSender interface
		Gateway:       gw, // Gateway implements GatewayService interface
		Searcher:      ftsSearcher,
		VectorSearch:  vectorSearch,
		SchemaBuilder: schemaBuilder,
	}
	toolsRegistry.SetServices(toolServices)

	// NOW convert tools to AI format (after SetServices registered them)
	aiTools := convertToolsToAIFormat(toolsRegistry)

	// Add skills-generated tools if available
	if skillsManager != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if skillTools, err := skillsManager.GenerateTools(ctx); err == nil {
			log.Printf("Adding %d skills-generated tools", len(skillTools))
			for _, skillTool := range skillTools {
				aiTool := ai.Tool{
					Name:        skillTool.Name(),
					Description: skillTool.Description(),
					Parameters:  skillTool.Parameters(),
				}
				aiTools = append(aiTools, aiTool)
			}
		} else {
			log.Printf("WARNING: Failed to generate skills tools: %v", err)
		}
	}

	// Update agent with the now-registered tools
	agentSystem.SetTools(aiTools)

	// Initialize scheduler
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}
	gw.scheduler = scheduler.New(workspaceDir, gw.executeScheduledJob)

	// Initialize heartbeat integration
	gw.heartbeatIntegration = heartbeat.NewGatewayIntegration(workspaceDir, sessionStore, aiRouter, gw.scheduler, gw, metricsCollector)

	// Auto-create agent heartbeat job if enabled
	if err := gw.initializeAgentHeartbeat(cfg); err != nil {
		log.Printf("WARNING: Failed to initialize agent heartbeat: %v", err)
	}

	log.Printf("Gateway initialized with:")
	log.Printf("  - Agent: %s (%s personality)", agentCfg.Name, agentCfg.Personality)
	log.Printf("  - Workspace: %v", workspaceContext != nil)
	log.Printf("  - Skills: %v", skillsManager != nil && skillsManager.IsEnabled())
	log.Printf("  - Tools: %d available", len(aiTools))
	log.Printf("  - Scheduler: enabled")
	log.Printf("  - Auth: enabled (middleware + WebSocket authenticator)")
	log.Printf("  - Vector Search: %v", gw.vectorService != nil)
	if cfg.RateLimiting.Enabled {
		log.Printf("  - Rate Limiting: enabled (anonymous: %d req/%ds, authenticated: %d req/%ds)",
			cfg.RateLimiting.Anonymous.MaxRequests, cfg.RateLimiting.Anonymous.WindowSeconds,
			cfg.RateLimiting.Authenticated.MaxRequests, cfg.RateLimiting.Authenticated.WindowSeconds)
	} else {
		log.Printf("  - Rate Limiting: disabled")
	}
	if len(cfg.AI.ModelAliases) > 0 {
		log.Printf("  - Model Aliases: %d configured", len(cfg.AI.ModelAliases))
	} else {
		log.Println("  - Model Aliases: using defaults (not configured)")
		log.Println("    To customize, add \"model_aliases\" to the \"ai\" section of your config.json:")
		log.Println("    \"model_aliases\": { \"haiku\": \"claude-haiku-4-5-20251001\", \"sonnet\": \"claude-sonnet-4-20250514\", \"opus\": \"claude-opus-4-6\", \"default\": \"\" }")
	}

	return gw, nil
}

// executeScheduledJob is called when a Go cron job fires
func (g *Gateway) executeScheduledJob(ctx context.Context, job *scheduler.Job) error {
	log.Printf("[Scheduler] Executing job: %s - %s", job.ID, job.Command)

	// Check if this is a heartbeat job
	if heartbeat.IsHeartbeatJob(job) {
		log.Printf("[Scheduler] Routing to heartbeat execution framework")
		return g.heartbeatIntegration.ExecuteHeartbeat(ctx, job)
	}

	// Handle regular cron jobs (existing logic)
	// Create a session for this job
	sessionKey := fmt.Sprintf("cron_%s_%d", job.ID, time.Now().UnixNano())
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

	// Execute the job command as an AI prompt
	response, err := g.ai.GenerateResponseWithTools(ctx, session, job.Command, "", model)
	if err != nil {
		return fmt.Errorf("AI execution failed: %w", err)
	}

	// If there's a target, send the result there
	if job.Target != "" {
		responseContent := response.GetContent()

		// Check for silent response patterns - don't send these to the target
		if responseContent == "" || isSilentResponse(responseContent) {
			log.Printf("[Scheduler] Job %s completed with silent response, not sending to target", job.ID)
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
				ID:        fmt.Sprintf("cron_%s_%d", job.ID, time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID: channelID,
			UserID:    userID,
			Text:      responseContent,
		}

		if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
			log.Printf("[Scheduler] Failed to send job output to %s: %v", job.Target, err)
		}
	}

	log.Printf("[Scheduler] Job %s completed, response: %d chars", job.ID, len(response.GetContent()))
	return nil
}

// convertToolsToAIFormat converts tools registry tools to AI format
func convertToolsToAIFormat(registry *tools.Registry) []ai.Tool {
	var aiTools []ai.Tool

	availableTools := registry.GetAvailableTools()
	for _, tool := range availableTools {
		aiTool := ai.Tool{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
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
	log.Printf("Created internal token for %s (id: %s)", clientName, resp.TokenInfo.TokenID)
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

	// Public health endpoints (no auth required, but rate limited)
	mux.Handle("/health", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleHealthEnhanced)))
	mux.Handle("/metrics", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleMetrics)))
	mux.Handle("/diagnostics", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleDiagnostics)))
	mux.Handle("/prometheus", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handlePrometheusMetrics)))

	// WebSocket endpoint with custom authentication and rate limiting
	mux.Handle("/ws", g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleWebSocket)))

	// Protected API endpoints - wrapped with auth middleware and rate limiting
	// Order: auth middleware first (sets context), then rate limiting (uses context), then handler
	mux.Handle("/api/channels/status", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleChannelStatus))))
	mux.Handle("/api/test/message", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(g.handleTestMessage))))

	// Vector API endpoints (registered unconditionally; handlers return 503 when disabled)
	vectorAPI := &VectorAPI{vectorService: g.vectorService}
	mux.Handle("/api/vector/search", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleSearch))))
	mux.Handle("/api/vector/index", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleIndex))))
	mux.Handle("/api/vector/delete", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleDelete))))
	mux.Handle("/api/vector/status", g.authMiddleware.Wrap(g.rateLimitMiddleware.Wrap(http.HandlerFunc(vectorAPI.handleStatus))))

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", g.config.Port),
		Handler: mux,
	}

	// Start channel manager
	if err := g.startChannels(ctx); err != nil {
		return fmt.Errorf("failed to start channels: %w", err)
	}

	// Start scheduler
	if g.scheduler != nil {
		if err := g.scheduler.Start(); err != nil {
			log.Printf("WARNING: Failed to start scheduler: %v", err)
		}
	}

	// Start heartbeat service
	if g.heartbeatService != nil {
		if err := g.heartbeatService.Start(ctx); err != nil {
			log.Printf("WARNING: Failed to start heartbeat service: %v", err)
		}
	}

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
						log.Printf("FTS5 periodic re-index failed: %v", err)
					}

					// Re-index beads if available
					if g.beadsIndexer != nil {
						if err := g.beadsIndexer.IndexBeads(ctx); err != nil {
							log.Printf("Beads periodic re-index failed: %v", err)
						}
					}

					// Run incremental message sync as safety net
					if g.messageSyncer != nil {
						if err := g.messageSyncer.IncrementalSync(ctx); err != nil {
							log.Printf("Message incremental sync failed: %v", err)
						}
					}
				}
			}
		}()
	}

	// Start SSH server if configured
	if g.config.SSH.Enabled {
		sshConfig := internalssh.SSHConfig{
			ListenAddr:         g.config.SSH.ListenAddr,
			HostKeyPath:        g.config.SSH.HostKeyPath,
			AuthorizedKeysPath: g.config.SSH.AuthorizedKeysPath,
			GatewayURL:         fmt.Sprintf("ws://localhost:%d/ws", g.config.Port),
			AssistantName:      g.config.Agent.Name,
			Location:           g.config.GetLocation(),
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
			log.Printf("WARNING: Failed to create SSH server: %v", err)
		} else {
			g.sshServer = sshServer
			go func() {
				log.Printf("SSH server listening on %s (direct mode)", sshConfig.ListenAddr)
				if err := sshServer.ListenAndServe(); err != nil {
					select {
					case <-ctx.Done():
					default:
						log.Printf("SSH server error: %v", err)
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
			log.Printf("HTTP server error: %v", err)
		}
	}()

	log.Printf("Gateway started on port %d", g.config.Port)

	// Wait for context cancellation
	<-ctx.Done()

	// Graceful shutdown
	log.Println("Shutting down gateway...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	g.stopChannels()

	// Stop SSH server
	if g.sshServer != nil {
		log.Println("Stopping SSH server...")
		g.sshServer.Close()
	}

	// Stop heartbeat service
	if g.heartbeatService != nil {
		if err := g.heartbeatService.Stop(); err != nil {
			log.Printf("Error stopping heartbeat service: %v", err)
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

	// Close vector search service
	if g.vectorService != nil {
		if err := g.vectorService.Close(); err != nil {
			log.Printf("Error closing vector service: %v", err)
		}
	}

	return nil
}

// handleWebSocket handles WebSocket connections with authentication
func (g *Gateway) handleWebSocket(w http.ResponseWriter, r *http.Request) {
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

	conn, err := g.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		ID:     fmt.Sprintf("client_%d", time.Now().UnixNano()),
		Role:   authResult.AuthInfo.ClientName, // Store authenticated client name
		UserID: authResult.AuthInfo.ClientName, // Default user identity from auth
		Conn:   conn,
		Send:   make(chan []byte, 256),
	}

	g.clientMu.Lock()
	g.clients[client.ID] = client
	clientCount := len(g.clients)
	g.clientMu.Unlock()

	// Update metrics
	if g.metricsCollector != nil {
		g.metricsCollector.UpdateWebSocketConnections(clientCount)
	}

	log.Printf("Client connected: %s (auth: %s)", client.ID, authResult.AuthInfo.ClientName)

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

		// Update metrics
		if g.metricsCollector != nil {
			g.metricsCollector.UpdateWebSocketConnections(clientCount)
		}

		client.Conn.Close()
		log.Printf("Client disconnected: %s", client.ID)
	}()

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("Client %s closed connection normally", client.ID)
			} else {
				log.Printf("WebSocket read error from %s: %v", client.ID, err)
			}
			break
		}

		parsed, err := protocol.ParseMessage(message)
		if err != nil {
			log.Printf("Failed to parse message from %s: %v", client.ID, err)
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
			log.Printf("Unhandled message type from %s: %T", client.ID, msg)
		}
	}
}

// handleClientWrite handles outgoing messages to a WebSocket client
func (g *Gateway) handleClientWrite(client *Client) {
	defer client.Conn.Close()

	for {
		select {
		case message, ok := <-client.Send:
			if !ok {
				client.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
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

	log.Println("Channel manager started")
	return nil
}

// stopChannels stops the channel manager
func (g *Gateway) stopChannels() {
	if err := g.channelManager.Stop(); err != nil {
		log.Printf("Error stopping channel manager: %v", err)
	}
}

// processMessages handles the main message processing loop
func (g *Gateway) processMessages(ctx context.Context) {
	log.Println("Starting message processor...")

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
	log.Printf("Processing message from %s (%d chars)", msg.ChannelID, len(msg.Text))

	// Track activity in metrics collector
	if g.metricsCollector != nil {
		g.metricsCollector.MarkActivity()
	}

	// Get or create session
	session, err := g.sessions.GetOrCreateSession(msg.UserID, msg.ChannelID)
	if err != nil {
		log.Printf("Error getting session: %v", err)
		return
	}

	// Handle commands before AI processing
	if handled := g.handleCommand(ctx, msg, session); handled {
		return
	}

	// Add user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", msg.Text, msg.Metadata)
	if err != nil {
		log.Printf("Error saving user message: %v", err)
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

		// Get model override from session context (if set via /model)
		modelOverride := session.Context["model"]

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
				log.Printf("[Streaming] Failed to send placeholder: %v", sendErr)
				supportsStreaming = false // Fall back to non-streaming
			} else {
				close(typingDone) // Stop typing indicator since we have a message now
				typingClosed = true

				// Set up streaming state
				var textBuilder strings.Builder
				var lastEditTime time.Time
				editInterval := 500 * time.Millisecond
				minCharsForEdit := 50

				onDelta := func(delta string, done bool) {
					textBuilder.WriteString(delta)

					currentText := textBuilder.String()
					timeSinceEdit := time.Since(lastEditTime)

					// Edit message if enough time passed or enough chars accumulated or done
					shouldEdit := done ||
						(timeSinceEdit >= editInterval && len(currentText) > minCharsForEdit) ||
						(len(currentText)-len(currentText) > 100) // Every 100 chars

					if shouldEdit && len(currentText) > 0 {
						if editErr := streamingAdapter.EditMessageText(chatID, placeholderMsgID, currentText); editErr != nil {
							log.Printf("[Streaming] Edit failed: %v", editErr)
						}
						lastEditTime = time.Now()
					}
				}

				convResponse, err = g.ai.GenerateResponseStreaming(reqCtx, session, msg.Text, modelOverride, onDelta)

				// Final edit with complete text
				if err == nil && convResponse != nil {
					finalContent := convResponse.GetContent()
					streamedLength := textBuilder.Len()

					// Check for silent response patterns in final content
					if isSilentResponse(finalContent) {
						log.Printf("[Streaming] Silent response pattern detected, deleting placeholder")
						// Delete the placeholder message since we don't want to show this
						if deleteErr := streamingAdapter.DeleteMessage(chatID, placeholderMsgID); deleteErr != nil {
							log.Printf("[Streaming] Failed to delete placeholder: %v", deleteErr)
						}
						streamingUsed = true
					} else if finalContent != "" {
						// If tool execution happened, the final content might be different from streamed text
						if streamedLength > 0 && finalContent != textBuilder.String() {
							log.Printf("[Streaming] Tool execution detected: streamed=%d chars, final=%d chars",
								streamedLength, len(finalContent))
						}
						streamingAdapter.EditMessageText(chatID, placeholderMsgID, finalContent)
						streamingUsed = true
					} else if streamedLength > 0 {
						// Fallback to streamed text if no final content
						streamedText := textBuilder.String()
						// Also check streamed text for silent patterns
						if isSilentResponse(streamedText) {
							log.Printf("[Streaming] Silent response in streamed text, deleting placeholder")
							if deleteErr := streamingAdapter.DeleteMessage(chatID, placeholderMsgID); deleteErr != nil {
								log.Printf("[Streaming] Failed to delete placeholder: %v", deleteErr)
							}
						} else {
							log.Printf("[Streaming] Using streamed text only: %d chars", streamedLength)
							streamingAdapter.EditMessageText(chatID, placeholderMsgID, streamedText)
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

			convResponse, err = g.ai.GenerateResponseWithToolsAndProgress(reqCtx, session, msg.Text, "", modelOverride, onProgress)
		}
		if err != nil {
			if !typingClosed {
				close(typingDone) // Stop typing indicator
			}

			// Check if this was a cancellation (from /stop)
			if reqCtx.Err() == context.Canceled {
				log.Printf("Request cancelled for session: %s", session.Key)
				return // Silent return, /stop already sent a message
			}

			log.Printf("Error generating AI response: %v", err)

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
			_ = g.sessions.SetSessionContext(session.Key, "last_prompt_tokens", strconv.Itoa(usage.PromptTokens))
			_ = g.sessions.SetSessionContext(session.Key, "last_completion_tokens", strconv.Itoa(usage.CompletionTokens))
			_ = g.sessions.SetSessionContext(session.Key, "last_total_tokens", strconv.Itoa(usage.TotalTokens))
		}

		// Check for silent response tokens (NO_REPLY, HEARTBEAT_OK)
		if responseContent == "" || isSilentResponse(responseContent) {
			if responseContent == "" {
				log.Printf("Warning: Empty response content, not sending to channel")
			} else {
				log.Printf("Silent response detected in channel message (%d chars), suppressing", len(responseContent))
			}
			return
		}

		// Add AI response to session
		_, err = g.sessions.AddMessage(session.Key, "assistant", responseContent, nil)
		if err != nil {
			log.Printf("Error saving AI message: %v", err)
		}

		// Skip sending if streaming already edited the message
		if streamingUsed {
			log.Printf("[Streaming] Response delivered via message editing (%d chars)", len(responseContent))
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
			log.Printf("Error sending response: %v", err)
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
		http.Error(w, fmt.Sprintf("Error creating session: %v", err), http.StatusInternalServerError)
		return
	}

	// Add user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", req.Message, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error saving user message: %v", err), http.StatusInternalServerError)
		return
	}

	// Generate AI response
	if g.ai == nil {
		http.Error(w, "AI router not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	// Use GenerateResponseWithTools to enable tool execution
	modelOverride := session.Context["model"]
	convResponse, err := g.ai.GenerateResponseWithTools(ctx, session, req.Message, "", modelOverride)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error generating AI response: %v", err), http.StatusInternalServerError)
		return
	}

	// Add AI response to session
	_, err = g.sessions.AddMessage(session.Key, "assistant", convResponse.GetContent(), nil)
	if err != nil {
		log.Printf("Error saving AI message: %v", err)
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
			log.Printf("vecgo: skip %s: %v", path, readErr)
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
			log.Printf("vecgo: index %s: %v", relPath, indexErr)
			return nil
		}
		indexed++
		return nil
	})
	if err != nil {
		log.Printf("WARNING: Vector workspace indexing walk error: %v", err)
	}
	if indexed > 0 {
		if saveErr := svc.Save(ctx); saveErr != nil {
			log.Printf("WARNING: Vector index save failed: %v", saveErr)
		}
		log.Printf("Vector search: indexed %d workspace files", indexed)
	}
}
