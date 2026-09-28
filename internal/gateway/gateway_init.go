package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/logging"
	"conduit/internal/mcp"
	"conduit/internal/middleware"
	"conduit/internal/mqtt"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/tools"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/types"
	"conduit/internal/workspace"
)

// buildRateLimitMiddleware constructs the HTTP rate-limit middleware from the
// gateway's configured anonymous / authenticated tiers. Extracted from New so
// the constructor reads as a flat service-wiring sequence.
func buildRateLimitMiddleware(cfg *config.Config, logger *slog.Logger) *middleware.RateLimitMiddleware {
	return middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Logger: logger,
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
}

// buildToolServices assembles the ToolServices struct consumed by the tool
// registry after all underlying subsystems (search, brain, reflection, mqtt,
// vision) have been constructed. Extracted from New so the constructor body
// reads as a flat wiring sequence.
func (g *Gateway) buildToolServices(
	cfg *config.Config,
	sessionStore *sessions.Store,
	aiRouter *ai.Router,
	debugBuffer *debuglog.RingBuffer,
	skillsManager *skills.Manager,
) *tools.ToolServices {
	// Build VectorService interface value (nil if disabled).
	var vectorSearch types.VectorService
	if g.search.VectorService != nil {
		vectorSearch = g.search.VectorService
	}

	// Build MQTTService interface value (nil if disabled).
	var mqttSvc types.MQTTService
	if g.mqttService != nil {
		mqttSvc = mqtt.NewServiceAdapter(g.mqttService)
	}

	// Build BrainService interface value (nil if disabled).
	var brainSvcAdapter types.BrainService
	if g.cognition.BrainEnabled() {
		brainSvcAdapter = newBrainAdapter(g.cognition.Brain)
	}

	// Build BrainFTSSearcher interface value (nil if brain or search DB
	// unavailable). Attaches the indexer to the search service in passing.
	var brainFTS types.BrainFTSSearcher
	if g.cognition.BrainEnabled() && g.search.SearchDB != nil {
		g.search.WireBrainIndexer(context.Background(), g.cognition.Brain.DB())
		brainFTS = g.search.BrainIndexer
	}

	// Build REMCycleRunner interface value (nil if REM cycle not initialized).
	var remCycleRunner types.REMCycleRunner
	if g.cognition.REMCycle != nil {
		remCycleRunner = newREMCycleAdapter(g.cognition.REMCycle)
	}

	// Build ReflectionService interface value (nil if reflection store not
	// initialized).
	var reflectionSvc types.ReflectionService
	if g.cognition.ReflectionStore != nil {
		reflectionSvc = newReflectionAdapter(g.cognition.ReflectionStore)
	}

	// Wire a vision analyzer backed by the AI router so the ImageTool can
	// perform real multimodal analysis via the configured provider (typically
	// Anthropic Claude vision). nil when no provider is configured.
	var visionAnalyzer types.VisionAnalyzer
	if aiRouter != nil && aiRouter.HasProviders() {
		visionAnalyzer = newVisionAdapter(aiRouter)
	}

	// conduit-c8ct/w3l7: approval gate for K8s/SSH. Assigned only when set
	// so a nil *Manager never becomes a non-nil interface.
	var approvals approval.Requester
	if g.approvals != nil {
		approvals = g.approvals
	}

	return &tools.ToolServices{
		Approvals:     approvals,
		SessionStore:  sessionStore,
		ConfigMgr:     cfg,
		WebClient:     &http.Client{Timeout: 30 * time.Second},
		ChannelSender: g, // Gateway implements ChannelSender interface
		Gateway:       g, // Gateway implements GatewayService interface
		Searcher:      g.search.FTSSearcher,
		VectorSearch:  vectorSearch,
		VectorIndexer: g.search.VectorIndexer,
		MQTTService:   mqttSvc,
		Brain:         brainSvcAdapter,
		BrainFTS:      brainFTS,
		REMCycle:      remCycleRunner,
		Reflection:    reflectionSvc,
		Vision:        visionAnalyzer,
		SchemaBuilder: createSchemaBuilder(g, cfg),
		DebugLog:      debugBuffer,
		SkillsManager: skillsManager,
	}
}

// setupMCPForClaudeCode initializes the MCP server and .mcp.json config
// manager when a claude-code provider is configured. It also wires a session
// mapper into the provider for conversation continuity. Returns nil values
// when no claude-code provider is configured.
func setupMCPForClaudeCode(
	cfg *config.Config,
	aiRouter *ai.Router,
	toolsRegistry *tools.Registry,
	executionEngine *tools.ExecutionEngine,
	sessionStore *sessions.Store,
	logger *slog.Logger,
) (*mcp.Server, *mcp.MCPConfigManager) {
	for _, provCfg := range cfg.AI.Providers {
		if provCfg.Type != "claude-code" {
			continue
		}
		ccCfg := provCfg.ClaudeCodeOrDefault()

		// Create session mapper for conversation continuity.
		ccSessionMapper := sessions.NewClaudeCodeSessionMapper(sessionStore.DB())
		if err := ccSessionMapper.EnsureTable(); err != nil {
			logger.Warn("failed to create claude code session table", "error", err)
		}

		// Wire session mapper into the provider.
		if provider, ok := aiRouter.GetProvider(provCfg.Name); ok {
			if ccProvider, ok := provider.(*ai.ClaudeCodeProvider); ok {
				ccProvider.SetSessionMapper(ccSessionMapper)
			}
		}

		// conduit-31jg.8: bearer auth, and tool calls go through the
		// execution engine (timeout, truncation, reflection, panic recovery).
		authMode, authToken, ok := resolveMCPAuth(cfg, logger)
		if !ok {
			return nil, nil
		}

		// Create MCP server to expose Conduit tools to Claude Code.
		mcpServer := mcp.NewServer(toolsRegistry, ccCfg.MCPPort,
			mcp.WithExecutor(executionEngine),
			mcp.WithAuth(authMode, authToken))

		// Create MCP config manager for .mcp.json lifecycle.
		var mcpConfigMgr *mcp.MCPConfigManager
		if ccCfg.WorkingDir != "" {
			mcpConfigMgr = mcp.NewMCPConfigManager(ccCfg.WorkingDir, ccCfg.MCPPort)
			mcpConfigMgr.SetAuthHeader(authMode != mcp.AuthDisabled)
		}

		logger.Info("claude-code provider configured",
			"mcp_port", ccCfg.MCPPort,
			"working_dir", ccCfg.WorkingDir)
		return mcpServer, mcpConfigMgr // Only one claude-code provider supported.
	}
	return nil, nil
}

// setupSummaryManager constructs the workspace summary manager (for small-
// context models) and attaches it to the agent system. A no-op when summary
// is disabled or no workspace context is configured.
func setupSummaryManager(
	cfg *config.Config,
	logger *slog.Logger,
	aiRouter *ai.Router,
	agentSystem *agent.ConduitAgentWithIntegration,
	workspaceContext *workspace.WorkspaceContext,
) {
	if !cfg.Workspace.Summary.Enabled || workspaceContext == nil {
		return
	}

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
