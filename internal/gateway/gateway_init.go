package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/brain"
	"conduit/internal/brain/rem"
	"conduit/internal/config"
	"conduit/internal/logging"
	"conduit/internal/mcp"
	"conduit/internal/middleware"
	"conduit/internal/mqtt"
	"conduit/internal/reflection"
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

// initBrainSubsystem constructs the optional Brain cognitive architecture and
// its REM sleep cycle, wiring both onto the gateway. A no-op when Brain is
// disabled; on construction failure the gateway logs a warning and continues
// without a brain service.
func (g *Gateway) initBrainSubsystem(cfg *config.Config) {
	if !cfg.Brain.Enabled {
		return
	}

	brainDBPath := cfg.Brain.Path
	if brainDBPath == "" {
		brainDBPath = config.DeriveBrainDBPath(cfg.Database.Path)
	}
	var brainOpts []brain.Option
	if cfg.Brain.MaxLTMEntries > 0 {
		brainOpts = append(brainOpts, brain.WithMaxLTMEntries(cfg.Brain.MaxLTMEntries))
	}
	if cfg.Brain.AutoFlushSeconds > 0 {
		brainOpts = append(brainOpts, brain.WithAutoFlushInterval(time.Duration(cfg.Brain.AutoFlushSeconds)*time.Second))
	}
	if cfg.Brain.ConsolidateThreshold > 0 {
		brainOpts = append(brainOpts, brain.WithConsolidateThreshold(cfg.Brain.ConsolidateThreshold))
	}
	if cfg.Brain.EvictThreshold > 0 {
		brainOpts = append(brainOpts, brain.WithEvictThreshold(cfg.Brain.EvictThreshold))
	}
	brainOpts = append(brainOpts, brain.WithAutoPromote(cfg.Brain.AutoPromote))
	if cfg.Brain.WMGracePeriodSeconds > 0 {
		brainOpts = append(brainOpts, brain.WithWMGracePeriod(time.Duration(cfg.Brain.WMGracePeriodSeconds)*time.Second))
	}
	if cfg.Brain.AccessWeight > 0 {
		brainOpts = append(brainOpts, brain.WithAccessWeight(cfg.Brain.AccessWeight))
	}
	if cfg.Brain.RecencyWeight > 0 {
		brainOpts = append(brainOpts, brain.WithRecencyWeight(cfg.Brain.RecencyWeight))
	}
	if cfg.Brain.TierWeight > 0 {
		brainOpts = append(brainOpts, brain.WithTierWeight(cfg.Brain.TierWeight))
	}
	if cfg.Brain.RecencyDecayRate > 0 {
		brainOpts = append(brainOpts, brain.WithRecencyDecayRate(cfg.Brain.RecencyDecayRate))
	}
	if cfg.Brain.AccessCountCap > 0 {
		brainOpts = append(brainOpts, brain.WithAccessCountCap(cfg.Brain.AccessCountCap))
	}
	if cfg.Brain.WarmthInjectFloor > 0 {
		brainOpts = append(brainOpts, brain.WithWarmthInjectFloor(cfg.Brain.WarmthInjectFloor))
	}
	if cfg.Brain.WarmthInjectLimit != 0 {
		brainOpts = append(brainOpts, brain.WithWarmthInjectLimit(cfg.Brain.WarmthInjectLimit))
	}
	brainSvc, brainErr := brain.New(brainDBPath, brainOpts...)
	if brainErr != nil {
		g.logger.Warn("failed to initialize brain, continuing without", "error", brainErr)
		return
	}

	g.brainService = brainSvc
	g.logger.Info("brain cognitive architecture initialized", "path", brainDBPath)

	// Initialize REM cycle if enabled.
	if !cfg.Brain.REMEnabled {
		return
	}
	remConfig := rem.REMConfig{
		PruneAgeDays:      cfg.Brain.REMPruneAgeDays,
		SalienceDecayRate: cfg.Brain.REMSalienceDecayRate,
		IntegrationDay:    cfg.Brain.REMIntegrationDay,
		GroomWithLLM:      cfg.Brain.REMGroomWithLLM,
		LogPath:           cfg.Brain.REMLogPath,
		WorkspaceDir:      cfg.Workspace.ContextDir,
		MaxLTMEntries:     cfg.Brain.MaxLTMEntries,
	}
	g.remCycle = rem.NewREMCycle(g.brainService, g.brainService.DB(), remConfig)
	g.logger.Info("REM sleep cycle initialized",
		"schedule", cfg.Brain.REMSchedule,
		"prune_age_days", cfg.Brain.REMPruneAgeDays,
		"integration_day", cfg.Brain.REMIntegrationDay)
}

// initReflectionSubsystem constructs the optional SPAR reflection store,
// session reflector, and farewell detector, and wires per-tool reflection
// capture onto the execution engine. Requires a brain service for its
// underlying database; a no-op when brain or reflection is disabled.
func (g *Gateway) initReflectionSubsystem(cfg *config.Config, executionEngine *tools.ExecutionEngine) {
	if g.brainService == nil {
		return
	}
	reflCfg := cfg.Reflection
	if reflCfg == nil {
		reflCfg = reflection.DefaultConfig()
	}
	if !reflCfg.Enabled {
		return
	}

	g.reflectionStore = reflection.NewStore(g.brainService.DB())
	g.sessionReflector = reflection.NewSessionReflector(g.reflectionStore)
	g.farewellDetector = reflection.NewFarewellDetector()

	// Wire per-tool reflection capture: adapt ExecutionEngine's
	// AfterExecutionFunc to the reflection middleware's hook.
	reflMW := reflection.NewReflectionMiddleware(g.reflectionStore, reflCfg)
	hook := reflMW.Hook()
	executionEngine.SetAfterExecutionHook(func(ctx context.Context, toolName string, result *tools.ExecutionResult) {
		info := reflection.ToolOutcomeInfo{
			ToolName:   toolName,
			SessionKey: types.RequestSessionKey(ctx),
			Duration:   result.Duration,
		}
		if result.Error != nil {
			info.Error = result.Error.Error()
			info.IsTimeout = reflection.IsTimeoutError(info.Error)
		}
		if result.Result != nil {
			info.Success = result.Result.Success && result.Error == nil
			info.RetryCount = result.Result.Retries
		}
		hook(ctx, info)
	})

	g.logger.Info("reflection store initialized")
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
	if g.brainService != nil {
		brainSvcAdapter = newBrainAdapter(g.brainService)
	}

	// Build BrainFTSSearcher interface value (nil if brain or search DB
	// unavailable). Attaches the indexer to the search service in passing.
	var brainFTS types.BrainFTSSearcher
	if g.brainService != nil && g.search.SearchDB != nil {
		g.search.WireBrainIndexer(context.Background(), g.brainService.DB())
		brainFTS = g.search.BrainIndexer
	}

	// Build REMCycleRunner interface value (nil if REM cycle not initialized).
	var remCycleRunner types.REMCycleRunner
	if g.remCycle != nil {
		remCycleRunner = newREMCycleAdapter(g.remCycle)
	}

	// Build ReflectionService interface value (nil if reflection store not
	// initialized).
	var reflectionSvc types.ReflectionService
	if g.reflectionStore != nil {
		reflectionSvc = newReflectionAdapter(g.reflectionStore)
	}

	// Wire a vision analyzer backed by the AI router so the ImageTool can
	// perform real multimodal analysis via the configured provider (typically
	// Anthropic Claude vision). nil when no provider is configured.
	var visionAnalyzer types.VisionAnalyzer
	if aiRouter != nil && aiRouter.HasProviders() {
		visionAnalyzer = newVisionAdapter(aiRouter)
	}

	return &tools.ToolServices{
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

		// Create MCP server to expose Conduit tools to Claude Code.
		mcpServer := mcp.NewServer(toolsRegistry, ccCfg.MCPPort)

		// Create MCP config manager for .mcp.json lifecycle.
		var mcpConfigMgr *mcp.MCPConfigManager
		if ccCfg.WorkingDir != "" {
			mcpConfigMgr = mcp.NewMCPConfigManager(ccCfg.WorkingDir, ccCfg.MCPPort)
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
