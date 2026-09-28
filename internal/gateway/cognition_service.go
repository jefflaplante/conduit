package gateway

import (
	"context"
	"log/slog"
	"time"

	"conduit/internal/brain"
	"conduit/internal/brain/rem"
	"conduit/internal/config"
	"conduit/internal/reflection"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// CognitionService groups the agent's optional cognitive-memory and SPAR
// reflection subsystems (conduit-18ub), previously five loose fields on
// Gateway: the Brain (tiered memory), its REM sleep cycle, the reflection
// store, the farewell detector and the session reflector. All of them are
// brain-enabled-or-none: the REM cycle and the reflection trio are built on
// the Brain's database.
//
// Gateway holds it by value, so a zero CognitionService (brain disabled, or a
// test-built &Gateway{}) is valid and every field is simply nil. Fields are
// exported so sibling files in the gateway package (and tests) can reach the
// subsystems directly; cross-package consumers go through the
// types.ToolServices adapters built in buildToolServices.
type CognitionService struct {
	// Brain is the tiered cognitive memory (nil when brain is disabled or
	// failed to open).
	Brain *brain.Brain
	// REMCycle is the brain's REM sleep cycle (nil unless brain.rem_enabled).
	REMCycle *rem.REMCycle
	// ReflectionStore persists SPAR reflections in the brain DB (nil unless
	// reflection is enabled).
	ReflectionStore *reflection.ReflectionStore

	// SPAR reflection: session-end detection and metrics.
	FarewellDetector *reflection.FarewellDetector
	SessionReflector *reflection.SessionReflector
}

// BrainEnabled reports whether the Brain service is available.
func (c *CognitionService) BrainEnabled() bool { return c.Brain != nil }

// ReflectionEnabled reports whether SPAR session reflection is armed (a
// session reflector exists).
func (c *CognitionService) ReflectionEnabled() bool { return c.SessionReflector != nil }

// initBrain constructs the optional Brain cognitive architecture and
// its REM sleep cycle, wiring both onto the cognition service. A no-op when Brain is
// disabled; on construction failure the gateway logs a warning and continues
// without a brain service.
func (c *CognitionService) initBrain(cfg *config.Config, logger *slog.Logger) {
	if !cfg.Brain.Enabled {
		return
	}

	brainDBPath := cfg.Brain.Path
	if brainDBPath == "" {
		brainDBPath = config.DeriveBrainDBPath(cfg.Database.Path)
	}
	var brainOpts []brain.Option
	brainOpts = append(brainOpts, brain.WithRecallEventsPath(cfg.RecallEventsPath())) // conduit-31jg.40
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
	brainOpts = append(brainOpts, brainCapacityOptions(cfg.Brain)...) // conduit-31jg.53
	brainSvc, brainErr := brain.New(brainDBPath, brainOpts...)
	if brainErr != nil {
		logger.Warn("failed to initialize brain, continuing without", "error", brainErr)
		return
	}

	c.Brain = brainSvc
	logger.Info("brain cognitive architecture initialized", "path", brainDBPath)

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
	c.REMCycle = rem.NewREMCycle(c.Brain, c.Brain.DB(), remConfig)
	logger.Info("REM sleep cycle initialized",
		"schedule", cfg.Brain.REMSchedule,
		"prune_age_days", cfg.Brain.REMPruneAgeDays,
		"integration_day", cfg.Brain.REMIntegrationDay)
}

// initReflection constructs the optional SPAR reflection store,
// session reflector, and farewell detector, and wires per-tool reflection
// capture onto the execution engine. Requires a brain service for its
// underlying database; a no-op when brain or reflection is disabled.
func (c *CognitionService) initReflection(cfg *config.Config, executionEngine *tools.ExecutionEngine, logger *slog.Logger) {
	if c.Brain == nil {
		return
	}
	reflCfg := cfg.Reflection
	if reflCfg == nil {
		reflCfg = reflection.DefaultConfig()
	}
	if !reflCfg.Enabled {
		return
	}

	c.ReflectionStore = reflection.NewStore(c.Brain.DB())
	c.SessionReflector = reflection.NewSessionReflector(c.ReflectionStore)
	c.FarewellDetector = reflection.NewFarewellDetector()

	// Wire per-tool reflection capture: adapt ExecutionEngine's
	// AfterExecutionFunc to the reflection middleware's hook.
	reflMW := reflection.NewReflectionMiddleware(c.ReflectionStore, reflCfg)
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

	// conduit-31jg.13: failure/pattern trackers are per-turn now; their
	// threshold crossings are promoted to SPAR here (conduit-17wz /
	// conduit-2ngi) so cross-session learning flows through the store rather
	// than through prompt injection into other sessions.
	executionEngine.SetPivotHook(func(ctx context.Context, toolName string, failCount int, lastError string) {
		reflMW.RecordConsecutiveFailure(types.RequestSessionKey(ctx), toolName, failCount, lastError)
	})
	executionEngine.SetCircularHook(func(ctx context.Context, pattern, signatureHash string) {
		reflMW.RecordCircularPattern(types.RequestSessionKey(ctx), pattern, signatureHash)
	})

	logger.Info("reflection store initialized")
}

// Start launches the cognition background loops, in this order: the SPAR
// idle-session reflection loop (when reflection is enabled) and the
// beads→Brain refresh loop (when the brain is enabled). The loop bodies live
// on Gateway (they need the session store and logger) and are passed in; each
// runs in its own goroutine bound to ctx.
func (c *CognitionService) Start(ctx context.Context, reflectIdle, refreshBeads func(context.Context)) {
	// Start SPAR reflection idle-session loop (writes Go-only metrics for
	// substantive sessions that go idle).
	if c.ReflectionEnabled() {
		go reflectIdle(ctx)
	}

	// Start beads→Brain wiring: query active tasks from `br` CLI and write
	// summary to Brain's sense.tasks.active namespace for Situation Awareness.
	// Best-effort: if br is missing or slow, this is silently skipped.
	if c.BrainEnabled() {
		go refreshBeads(ctx)
	}
}

// Stop closes the Brain service. It is a no-op (nil) when the brain is
// disabled. The REM cycle and reflection subsystems hold no resources of
// their own beyond the brain DB.
func (c *CognitionService) Stop() error {
	if c.Brain != nil {
		return c.Brain.Close()
	}
	return nil
}
