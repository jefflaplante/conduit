package tools

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"conduit/internal/config"
	"conduit/internal/policy"
	"conduit/internal/sandbox"
	"conduit/internal/tools/communication"
	"conduit/internal/tools/core"
	"conduit/internal/tools/scheduling"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
	"conduit/internal/tools/vision"
	"conduit/internal/tools/web"
)

// globalRegistry holds a reference to the active registry for tools that need ToolExecutor.
// Set by SetServices() after the registry is fully initialized.
// conduit-31jg.19: atomic so a late SetServices can't race with readers.
var globalRegistry atomic.Pointer[Registry]

// GetRegistryAsExecutor returns the global registry as a ToolExecutor.
// Used by tools like SRE that need to orchestrate other tools.
// Returns a nil interface (not a typed nil) when no registry is set.
func GetRegistryAsExecutor() types.ToolExecutor {
	if r := globalRegistry.Load(); r != nil {
		return r
	}
	return nil
}

// Registry manages available tools and their execution
type Registry struct {
	// mu guards tools and enabledTools (conduit-31jg.19). RefreshSkillTools
	// mutates both at runtime (model-triggerable via Gateway reload) while
	// ExecuteTool/GetAvailableTools read them concurrently. Never hold mu
	// while a tool executes: look the tool up under RLock, release, then run
	// it (a running tool may itself trigger RefreshSkillTools). Methods
	// suffixed "Locked" assume the caller holds mu; all other methods acquire
	// it themselves and must not be called with mu held (RWMutex is not
	// re-entrant).
	mu           sync.RWMutex
	tools        map[string]types.Tool
	sandboxCfg   config.SandboxConfig
	enabledTools map[string]bool
	services     *types.ToolServices
	resultChars  int            // tools.max_tool_result_chars (conduit-31jg.39)
	policy       *policy.Engine // tool action policy, shadow mode (conduit-25lt.2); guarded by mu
}

// Type aliases for backward compatibility
type ChannelSender = types.ChannelSender
type ToolServices = types.ToolServices
type Tool = types.Tool
type ToolResult = types.ToolResult

// NewRegistry creates a new tools registry with service dependencies
func NewRegistry(cfg config.ToolsConfig) *Registry {
	registry := &Registry{
		tools:        make(map[string]types.Tool),
		sandboxCfg:   cfg.Sandbox,
		enabledTools: make(map[string]bool),
		services:     &types.ToolServices{}, // Initialize empty services
		resultChars:  cfg.MaxToolResultChars,
	}

	// Mark enabled tools (normalized for case/underscore-insensitive matching)
	for _, toolName := range cfg.EnabledTools {
		registry.enabledTools[normalizeToolName(toolName)] = true
	}
	// conduit-38cz: SessionsCancel comes with SessionsSpawn — a session
	// that can spawn sub-agents can always cancel its own.
	if registry.enabledTools[normalizeToolName("SessionsSpawn")] {
		registry.enabledTools[normalizeToolName("SessionsCancel")] = true
	}

	// conduit-31jg.6: symlinks are now resolved before the containment check.
	// Warn about top-level links in a sandbox root that point outside it —
	// file tools will refuse them until the target is added to allowed_paths.
	warnEscapingSymlinks(sandbox.FromConfig(cfg.Sandbox), sandbox.SymlinkScanOptions{}, log.Printf)

	// Don't register tools here - wait for services to be set

	return registry
}

// warnEscapingSymlinks logs each symlink under the sandbox roots that
// resolves outside them, and — conduit-31jg.73 — says so when the bounded
// deep scan stopped early, so a clean (or short) list is not mistaken for a
// complete one.
func warnEscapingSymlinks(sb *sandbox.Sandbox, opts sandbox.SymlinkScanOptions, logf func(format string, args ...any)) {
	found, truncated := sb.ScanEscapingSymlinks(opts)
	links := make([]string, 0, len(found))
	for link := range found {
		links = append(links, link)
	}
	sort.Strings(links)
	for _, link := range links {
		target := found[link]
		// conduit-31jg.87: a dangling link fails resolution too, but it
		// is not an escape and allowed_paths would not help — say so.
		if _, err := os.Stat(link); errors.Is(err, fs.ErrNotExist) {
			logf("[Sandbox] WARNING: %s -> %s is a dangling symlink (target missing); file tools cannot use it. Fix or remove the link.", link, target)
			continue
		}
		logf("[Sandbox] WARNING: %s -> %s resolves outside tools.sandbox roots; Read/Write/Edit/Glob will deny it. Add %q to tools.sandbox.allowed_paths to keep access.", link, target, target)
	}
	if truncated {
		logf("[Sandbox] WARNING: escaping-symlink scan of %v was truncated (entry/time budget hit); "+
			"the %d link(s) reported may be incomplete. Unreported escaping links are still denied at use.",
			sb.Roots(), len(found))
	}
}

// SetServices sets the service dependencies and registers tools
func (r *Registry) SetServices(services *types.ToolServices) {
	r.services = services

	// Set global registry reference for tools that need ToolExecutor
	globalRegistry.Store(r)

	// Initialize schema builder with discovery providers
	r.initializeSchemaBuilder()

	r.registerAllTools()
}

// initializeSchemaBuilder creates and configures the schema builder with discovery providers.
//
// conduit-5y17: this is the only place a discovery-backed SchemaBuilder is
// built (the gateway used to build a second one that SetServices then
// overwrote). It feeds GetToolSchemasWithContext (the `conduit tools` CLI)
// only; the model-facing tool definitions apply static SchemaHints without
// discovery so they stay prompt-cache stable.
func (r *Registry) initializeSchemaBuilder() {
	if r.services == nil {
		return
	}

	providers := make(map[string]schema.DiscoveryProvider)

	// Channel discovery provider - needs channel manager status
	if r.services.Gateway != nil {
		// Create adapter for gateway channel status
		statusAdapter := schema.NewStatusProviderAdapter(func() map[string]interface{} {
			if status, err := r.services.Gateway.GetChannelStatus(); err == nil && status != nil {
				return status
			}
			return make(map[string]interface{})
		})
		providers["channels"] = schema.NewChannelDiscoveryProvider(statusAdapter)
	}

	// Workspace discovery provider
	if r.services.ConfigMgr != nil {
		workspaceDir := r.sandboxCfg.WorkspaceDir
		if r.services.ConfigMgr.Workspace.ContextDir != "" {
			workspaceDir = r.services.ConfigMgr.Workspace.ContextDir
		}
		providers["workspace_paths"] = schema.NewWorkspaceDiscoveryProvider(
			workspaceDir,
			r.sandboxCfg.AllowedPaths,
		)
	}

	// Initialize schema builder with providers
	r.services.SchemaBuilder = schema.NewBuilder(providers)
}

// registerAllTools registers all available tools from all categories
func (r *Registry) registerAllTools() {
	var allTools []types.Tool

	// Core system tools (enhanced file operations + new memory/session tools)
	allTools = append(allTools, []types.Tool{
		&ReadFileTool{registry: r},
		&WriteFileTool{registry: r},
		core.NewEditTool(r.services),
		&ExecTool{registry: r},
		&ListFilesTool{registry: r},
		// Memory tools (MemorySearch only - use Read for direct file access)
		core.NewMemorySearchTool(r.services, r.sandboxCfg),
		// Session tools
		core.NewSessionsListTool(r.services),
		core.NewSessionsSendTool(r.services),
		core.NewSessionsSpawnTool(r.services),
		core.NewSessionsCancelTool(r.services), // conduit-38cz
		core.NewSessionStatusTool(r.services),
		// Gateway tool
		core.NewGatewayTool(r.services),
		// Context tool
		core.NewContextTool(r.services),
		// Find tool (universal search)
		core.NewFindTool(r.services),
		// Facts tool (structured fact extraction from memory)
		core.NewFactsTool(r.services, r.sandboxCfg),
		// Chain tool (multi-tool workflow execution)
		core.NewChainTool(r.services, r.sandboxCfg, r),
	}...)

	// Web integration tools
	allTools = append(allTools, []types.Tool{
		web.NewWebSearchTool(r.services),
		web.NewWebFetchTool(r.services),
	}...)

	// Communication tools
	allTools = append(allTools, []types.Tool{
		communication.NewMessageTool(r.services),
		communication.NewStatusUpdateTool(r.services),
		communication.NewTTSTool(r.services),
	}...)

	// Scheduling tools
	allTools = append(allTools, []types.Tool{
		scheduling.NewCronTool(r.services),
	}...)

	// Vision tools
	allTools = append(allTools, []types.Tool{
		vision.NewImageToolWithSandbox(r.services, r.sandboxCfg), // conduit-31jg.62
	}...)

	// NOTE: Optional tools (Datadog, K8s, PagerDuty, MQTT, SSH, UniFi, SRE)
	// are now registered via build tags and RegisterOptional().
	// See internal/tools/<tool>/register.go for each tool's registration.

	// Debug log tool (optional - requires ring buffer)
	if r.services.DebugLog != nil {
		allTools = append(allTools, core.NewDebugLogTool(r.services, r.services.DebugLog))
	}

	// Brain cognitive architecture tool (optional - requires brain service)
	if r.services.Brain != nil {
		allTools = append(allTools, core.NewBrainTool(r.services))
	}

	// Google Workspace tools (optional - requires gws CLI)
	allTools = append(allTools, []types.Tool{
		&GoogleWorkspaceTool{registry: r},
	}...)

	// Register all tools
	r.mu.Lock()
	for _, tool := range allTools {
		if tool != nil {
			r.tools[tool.Name()] = tool
			log.Printf("Registered tool: %s", tool.Name())
		}
	}
	r.mu.Unlock()

	// Instantiate optional tools from registered factories (build-tag gated)
	r.registerOptionalTools()

	// Register skill-based tools (written directly to r.tools/r.enabledTools)
	r.registerSkillTools()

	// Warn about config/build mismatches
	r.warnMismatchedOptionalTools()
}

// GetServices returns the service dependencies for tools that need them
func (r *Registry) GetServices() *types.ToolServices {
	return r.services
}
