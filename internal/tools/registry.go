package tools

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/config"
	"conduit/internal/sandbox"
	"conduit/internal/skills"
	"conduit/internal/tools/communication"
	"conduit/internal/tools/core"
	"conduit/internal/tools/scheduling"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
	"conduit/internal/tools/vision"
	"conduit/internal/tools/web"
)

// optionalFactories holds factories for optional tools registered via init().
// Tools register here using build tags; the registry instantiates them at startup.
var optionalFactories = make(map[string]types.OptionalToolFactory)

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

// RegisterOptional registers an optional tool factory.
// Called from init() functions in optional tool packages with build tags.
// The factory will be invoked during SetServices() to instantiate the tool.
func RegisterOptional(name string, factory types.OptionalToolFactory) {
	optionalFactories[name] = factory
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
	resultChars  int // tools.max_tool_result_chars (conduit-31jg.39)
}

// Type aliases for backward compatibility
type ChannelSender = types.ChannelSender
type ToolServices = types.ToolServices
type Tool = types.Tool
type ToolResult = types.ToolResult

// skillToolBridge adapts *skills.SkillToolAdapter to satisfy types.Tool.
// SkillToolAdapter.Execute returns *skills.RegistryToolResult (to avoid circular imports),
// so this bridge converts it to *types.ToolResult.
type skillToolBridge struct {
	adapter  *skills.SkillToolAdapter
	services *types.ToolServices
}

func (b *skillToolBridge) Name() string                       { return b.adapter.Name() }
func (b *skillToolBridge) Description() string                { return b.adapter.Description() }
func (b *skillToolBridge) Parameters() map[string]interface{} { return b.adapter.Parameters() }
func (b *skillToolBridge) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	result, err := b.adapter.Execute(ctx, args)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return types.NewErrorResult("skill_error", result.Error), nil
	}

	toolResult := &types.ToolResult{Success: true, Content: result.Content, Data: result.Data}

	// Auto-cache declared brain keys from skill output
	b.autoCacheBrainKeys(ctx, result)

	return toolResult, nil
}

// autoCacheBrainKeys stores declared Produces keys from skill output into Brain working memory.
func (b *skillToolBridge) autoCacheBrainKeys(ctx context.Context, result *skills.RegistryToolResult) {
	if b.services == nil || b.services.Brain == nil {
		return
	}
	produces := b.adapter.BrainProduces()
	if len(produces) == 0 || result.Data == nil {
		return
	}
	source := "skill:" + b.adapter.Name()
	for _, key := range produces {
		if val, ok := result.Data[key]; ok {
			valStr := fmt.Sprintf("%v", val)
			if err := b.services.Brain.Store(ctx, key, valStr, types.BrainTierWorking, source); err != nil {
				log.Printf("Brain auto-cache failed for %s: %v", key, err)
			}
		}
	}
}

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
	for link, target := range found {
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

// initializeSchemaBuilder creates and configures the schema builder with discovery providers
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

// HasTool returns true if a tool is registered (compiled in and instantiated).
// Used by optional tools to check for dependencies (e.g., SRE checks for Datadog).
func (r *Registry) HasTool(name string) bool {
	_, exists := r.getTool(name)
	return exists
}

// getTool looks up a registered tool by exact name under the read lock.
func (r *Registry) getTool(name string) (types.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, exists := r.tools[name]
	return tool, exists
}

// ListAvailableOptionalTools returns names of optional tools that were compiled in.
// This reflects what factories are registered, not what tools are enabled.
func ListAvailableOptionalTools() []string {
	names := make([]string, 0, len(optionalFactories))
	for name := range optionalFactories {
		names = append(names, name)
	}
	return names
}

// normalizeToolName converts a tool name to a canonical form for case-insensitive matching.
// Handles both PascalCase (SessionsSpawn) and snake_case (sessions_spawn) inputs.
func normalizeToolName(name string) string {
	// Remove underscores and convert to lowercase
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

// isToolEnabled checks if a tool is enabled (case-insensitive, underscore-insensitive).
func (r *Registry) isToolEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isToolEnabledLocked(name)
}

// isToolEnabledLocked is isToolEnabled for callers already holding r.mu.
func (r *Registry) isToolEnabledLocked(name string) bool {
	return r.enabledTools[normalizeToolName(name)]
}

// buildSkillBridges discovers skill adapters. It touches no registry maps
// and runs without r.mu so skill discovery I/O never blocks tool lookups.
func (r *Registry) buildSkillBridges() []*skillToolBridge {
	if r.services.SkillsManager == nil || !r.services.SkillsManager.IsEnabled() {
		return nil
	}
	skillAdapters, err := skills.GenerateToolAdapters(context.Background(), r.services.SkillsManager)
	if err != nil {
		log.Printf("Failed to register skill tools: %v", err)
		return nil
	}
	bridges := make([]*skillToolBridge, 0, len(skillAdapters))
	for _, adapter := range skillAdapters {
		bridges = append(bridges, &skillToolBridge{adapter: adapter, services: r.services})
	}
	return bridges
}

// addSkillBridgesLocked registers bridges as enabled tools. Caller holds r.mu (write).
func (r *Registry) addSkillBridgesLocked(bridges []*skillToolBridge) {
	for _, bridge := range bridges {
		r.tools[bridge.Name()] = bridge
		r.enabledTools[normalizeToolName(bridge.Name())] = true
	}
	if len(bridges) > 0 {
		log.Printf("Registered and enabled %d skill-based tools", len(bridges))
	}
}

// registerSkillTools discovers skill adapters and registers them as enabled tools.
func (r *Registry) registerSkillTools() {
	bridges := r.buildSkillBridges()
	r.mu.Lock()
	r.addSkillBridgesLocked(bridges)
	r.mu.Unlock()
}

// registerOptionalTools instantiates optional tools from registered factories.
// Factories are registered via RegisterOptional() from init() functions with build tags.
func (r *Registry) registerOptionalTools() {
	for name, factory := range optionalFactories {
		// Factories run without r.mu held: they may call back into the
		// registry (e.g. HasTool / GetRegistryAsExecutor).
		tool, err := factory(r.services, r.services.ConfigMgr)
		if err != nil {
			log.Printf("Failed to create optional tool %s: %v", name, err)
			continue
		}
		if tool == nil {
			// Tool is compiled in but disabled via config - this is expected
			log.Printf("Optional tool %s: compiled but disabled via config", name)
			continue
		}
		r.mu.Lock()
		r.tools[tool.Name()] = tool
		r.mu.Unlock()
		log.Printf("Registered optional tool: %s", tool.Name())
	}
}

// warnMismatchedOptionalTools logs warnings for tools enabled in config but not compiled.
func (r *Registry) warnMismatchedOptionalTools() {
	if r.services.ConfigMgr == nil {
		return
	}
	cfg := r.services.ConfigMgr

	// Check each optional tool config against registered tools
	checks := []struct {
		name    string
		enabled bool
	}{
		{"Datadog", cfg.Datadog.Enabled},
		{"DatadogMonitor", cfg.Datadog.Enabled},
		{"Kubernetes", cfg.Kubernetes.Enabled},
		{"PagerDuty", cfg.PagerDuty.Enabled},
		{"SSH", cfg.RemoteSSH.Enabled},
		// MQTT is checked via service, not config
		// UniFi has no config enable flag
	}

	for _, check := range checks {
		if check.enabled && !r.HasTool(check.name) {
			log.Printf("Warning: %s is enabled in config but not compiled (missing build tag)", check.name)
		}
	}
}

// RefreshSkillTools removes old skill tools and re-registers from fresh state.
// Returns the number of skill tools now registered.
func (r *Registry) RefreshSkillTools() int {
	// conduit-31jg.19: discover outside the lock, then swap atomically under
	// the write lock so readers never observe a half-refreshed registry.
	bridges := r.buildSkillBridges()

	r.mu.Lock()
	defer r.mu.Unlock()
	// Remove old skill tools (identified by bridge type)
	for name, tool := range r.tools {
		if _, ok := tool.(*skillToolBridge); ok {
			delete(r.tools, name)
			// enabledTools is keyed by normalized name (see addSkillBridgesLocked).
			delete(r.enabledTools, normalizeToolName(name))
		}
	}
	// Re-register from fresh skills manager state
	r.addSkillBridgesLocked(bridges)
	// Count registered skill tools
	count := 0
	for _, tool := range r.tools {
		if _, ok := tool.(*skillToolBridge); ok {
			count++
		}
	}
	return count
}

// ExecuteTool executes a tool by name with the given arguments, including validation
//
// conduit-31jg.19: this is the single choke point every tool invocation passes
// through (ExecutionEngine.executeSingle/executeParallel, the MCP handler,
// Chain, planning, SRE). A panicking tool is recovered here and converted into
// a failed result + error naming the tool, so it cannot take down the gateway.
func (r *Registry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (result *types.ToolResult, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[Registry] PANIC in tool %q: %v\n%s", name, rec, debug.Stack())
			err = fmt.Errorf("tool %q panicked: %v", name, rec)
			result = types.NewErrorResult("tool_panic", err.Error()).
				WithSuggestions([]string{"This is a bug in the tool; try a different approach or tool"})
		}
	}()

	// Look up enablement and tool under the read lock, then release it
	// before executing (the tool may itself call RefreshSkillTools).
	r.mu.RLock()
	enabled := r.isToolEnabledLocked(name)
	tool, exists := r.tools[name]
	r.mu.RUnlock()

	// Check if tool is enabled
	if !enabled {
		result := types.NewErrorResult("tool_disabled", fmt.Sprintf("tool '%s' is not enabled", name)).
			WithSuggestions([]string{"Check the enabled_tools list in your config.json to enable this tool"})
		// Suggest enabled tools of the same category
		if similar := r.findSimilarEnabledTools(name); len(similar) > 0 {
			result.WithAvailableValues(similar)
		}
		return result, nil
	}

	if !exists {
		result := types.NewErrorResult("tool_not_found", fmt.Sprintf("tool '%s' not found", name))
		available := r.getEnabledToolNames()
		if len(available) > 0 {
			result.WithAvailableValues(available)
		}
		if closest := r.findClosestToolName(name); closest != "" {
			result.WithSuggestions([]string{fmt.Sprintf("Did you mean '%s'?", closest)})
		}
		return result, nil
	}

	// Validate parameters if tool supports validation
	if validator, ok := tool.(types.ParameterValidator); ok {
		validationResult := validator.ValidateParameters(ctx, args)
		if !validationResult.Valid {
			return r.createValidationErrorResult(name, validationResult), nil
		}
	}

	// Execute tool
	toolResult, execErr := tool.Execute(ctx, args)
	if execErr != nil {
		errResult := &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool execution error: %v", execErr),
		}
		// conduit-31jg.47: keep the tool's output when it returned both a
		// result and an error (was discarded, hiding e.g. partial stderr).
		if toolResult != nil {
			errResult.Content = toolResult.Content
			errResult.Data = toolResult.Data
			errResult.ErrorDetails = toolResult.ErrorDetails
		}
		return errResult, execErr
	}

	return toolResult, nil
}

// callTimeoutProvider is implemented by tools that accept a per-call timeout
// parameter (Bash). conduit-31jg.39.
type callTimeoutProvider interface {
	CallTimeout(args map[string]interface{}) (time.Duration, bool)
}

// CallTimeout reports the per-call timeout a tool call requests, if the
// named tool supports one. The execution engine uses it to size that
// call's deadline (conduit-31jg.39).
func (r *Registry) CallTimeout(name string, args map[string]interface{}) (time.Duration, bool) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return 0, false
	}
	if p, ok := tool.(callTimeoutProvider); ok {
		return p.CallTimeout(args)
	}
	return 0, false
}

// modelDataProvider is implemented (by duck typing, so optional-tool
// subpackages need not import this package) by tools whose useful payload
// lives in ToolResult.Data rather than Content. Only for these does the
// execution engine append "Structured data: {json}" to the model-facing
// text (conduit-31jg.39).
type modelDataProvider interface {
	IncludeDataInModelOutput() bool
}

// IncludeDataInModelOutput reports whether the named tool opted into having
// its result Data rendered for the model (conduit-31jg.39).
func (r *Registry) IncludeDataInModelOutput(name string) bool {
	r.mu.RLock()
	tool, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	p, ok := tool.(modelDataProvider)
	return ok && p.IncludeDataInModelOutput()
}

// maxResultChars is the model-facing result budget (config
// tools.max_tool_result_chars, default DefaultMaxToolResultChars). Read uses
// it to page output itself instead of being middle-truncated.
func (r *Registry) maxResultChars() int {
	if r.resultChars > 0 {
		return r.resultChars
	}
	return DefaultMaxToolResultChars
}

// createValidationErrorResult creates a rich error result from validation failures
func (r *Registry) createValidationErrorResult(toolName string, validation *types.ValidationResult) *types.ToolResult {
	if len(validation.Errors) == 0 {
		return types.NewErrorResult("validation_failed", "Parameter validation failed")
	}

	// Use the first error as the primary error message
	primaryError := validation.Errors[0]
	message := fmt.Sprintf("Parameter '%s': %s", primaryError.Parameter, primaryError.Message)

	result := types.NewErrorResult("invalid_parameter", message).
		WithParameter(primaryError.Parameter, primaryError.ProvidedValue)

	if len(primaryError.AvailableValues) > 0 {
		result.WithAvailableValues(primaryError.AvailableValues)
	}

	if len(primaryError.Examples) > 0 {
		var examples []string
		for _, example := range primaryError.Examples {
			examples = append(examples, fmt.Sprintf("%v", example))
		}
		result.WithExamples(examples)
	}

	// Add suggestions from validation result
	if len(validation.Suggestions) > 0 {
		result.WithSuggestions(validation.Suggestions)
	}

	// Add discovery hint if available
	if primaryError.DiscoveryHint != "" {
		result.WithSuggestions(append(result.ErrorDetails.Suggestions, primaryError.DiscoveryHint))
	}

	// Add context about all validation errors
	context := map[string]interface{}{
		"tool":              toolName,
		"validation_errors": len(validation.Errors),
		"total_parameters":  len(validation.Errors),
	}

	if len(validation.Errors) > 1 {
		var allErrors []string
		for _, err := range validation.Errors {
			allErrors = append(allErrors, fmt.Sprintf("%s: %s", err.Parameter, err.Message))
		}
		context["all_errors"] = allErrors
	}

	return result.WithContext(context)
}

// GetAvailableTools returns a list of available tools
func (r *Registry) GetAvailableTools() map[string]types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	available := make(map[string]types.Tool)
	for name, tool := range r.tools {
		if r.isToolEnabledLocked(name) {
			available[name] = tool
		}
	}
	return available
}

// getEnabledToolNames returns the names of all enabled tools.
func (r *Registry) getEnabledToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.tools {
		if r.isToolEnabledLocked(name) {
			names = append(names, name)
		}
	}
	return names
}

// findSimilarEnabledTools returns enabled tool names that share a prefix or substring with the given name.
func (r *Registry) findSimilarEnabledTools(name string) []string {
	lower := strings.ToLower(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var similar []string
	for toolName := range r.tools {
		if r.isToolEnabledLocked(toolName) && strings.Contains(strings.ToLower(toolName), lower[:min(len(lower), 3)]) {
			similar = append(similar, toolName)
		}
	}
	return similar
}

// findClosestToolName returns the enabled tool name most similar to the given name, or "".
func (r *Registry) findClosestToolName(name string) string {
	lower := strings.ToLower(name)
	best := ""
	bestScore := 0
	r.mu.RLock()
	defer r.mu.RUnlock()
	for toolName := range r.tools {
		if r.isToolEnabledLocked(toolName) {
			score := commonPrefixLen(lower, strings.ToLower(toolName))
			if score > bestScore {
				bestScore = score
				best = toolName
			}
		}
	}
	if bestScore >= 2 {
		return best
	}
	return ""
}

// commonPrefixLen returns the length of the common prefix between two strings.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// GetToolSchemas returns JSON schemas for all available tools
func (r *Registry) GetToolSchemas() []map[string]interface{} {
	return r.GetToolSchemasWithContext(context.Background())
}

// GetToolSchemasWithContext returns JSON schemas for all available tools, enhanced with discovery data
func (r *Registry) GetToolSchemasWithContext(ctx context.Context) []map[string]interface{} {
	var schemas []map[string]interface{}

	for _, tool := range r.GetAvailableTools() {
		params := tool.Parameters()

		// Check if tool provides schema hints and we have a schema builder
		if r.services != nil && r.services.SchemaBuilder != nil {
			if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
				hints := enhancedTool.GetSchemaHints()
				if hints != nil && len(hints) > 0 {
					params = r.services.SchemaBuilder.EnhanceSchema(ctx, params, hints)
				}
			}
		}

		toolSchema := map[string]interface{}{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  params,
		}
		schemas = append(schemas, toolSchema)
	}

	return schemas
}

// GetToolHelp returns comprehensive help information for a specific tool including examples
func (r *Registry) GetToolHelp(toolName string) map[string]interface{} {
	r.mu.RLock()
	tool, exists := r.tools[toolName]
	enabled := r.isToolEnabledLocked(toolName)
	r.mu.RUnlock()
	if !exists || !enabled {
		return map[string]interface{}{
			"error": fmt.Sprintf("Tool '%s' not found or not enabled", toolName),
		}
	}

	help := map[string]interface{}{
		"name":        tool.Name(),
		"description": tool.Description(),
		"parameters":  tool.Parameters(),
		"enabled":     enabled,
	}

	// Add schema hints if available
	if r.services != nil && r.services.SchemaBuilder != nil {
		if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
			hints := enhancedTool.GetSchemaHints()
			if hints != nil && len(hints) > 0 {
				help["schema_hints"] = hints
			}
		}
	}

	// Add usage examples if available
	if exampleProvider, ok := tool.(types.UsageExampleProvider); ok {
		examples := exampleProvider.GetUsageExamples()
		if len(examples) > 0 {
			help["examples"] = examples
		}
	}

	// Add validation capabilities info
	if _, ok := tool.(types.ParameterValidator); ok {
		help["supports_validation"] = true
	}

	if _, ok := tool.(types.ParameterDiscoverer); ok {
		help["supports_discovery"] = true
	}

	if _, ok := tool.(types.SelfTester); ok {
		help["supports_selftest"] = true
	}

	return help
}

// GetAllToolsHelp returns help information for all available tools
func (r *Registry) GetAllToolsHelp() map[string]interface{} {
	toolsHelp := make(map[string]interface{})

	for name := range r.GetAvailableTools() {
		toolsHelp[name] = r.GetToolHelp(name)
	}

	return map[string]interface{}{
		"tools": toolsHelp,
		"count": len(toolsHelp),
		"categories": map[string][]string{
			"file_operations": {"Read", "Write", "Edit", "Glob"},
			"system":          {"Bash"},
			"memory":          {"MemorySearch"},
			"communication":   {"Message", "Tts"},
			"web":             {"WebSearch", "WebFetch"},
			"sessions":        {"SessionsList", "SessionsSend", "SessionsSpawn", "SessionStatus"},
			"scheduling":      {"Cron"},
			"gateway":         {"Gateway"},
			"vision":          {"Image"},
		},
	}
}

// isPathAllowed checks if a file path is allowed within the sandbox.
// conduit-31jg.6: delegates to the shared symlink-aware sandbox resolver.
func (r *Registry) isPathAllowed(path string) bool {
	_, ok := r.sandboxResolve(path)
	return ok
}

// sandboxResolve returns the canonical (symlink-resolved) path when path is
// inside the sandbox. Callers should do their I/O on the returned path so the
// checked path and the opened path are the same.
// conduit-31jg.6
func (r *Registry) sandboxResolve(path string) (string, bool) {
	real, err := sandbox.FromConfig(r.sandboxCfg).Resolve(path)
	if err != nil {
		return "", false
	}
	return real, true
}

// RegistrySelfTestResult aggregates self-test results for all tools.
type RegistrySelfTestResult struct {
	Results       map[string]*types.SelfTestResult `json:"results"`
	TotalTools    int                              `json:"total_tools"`
	TestedTools   int                              `json:"tested_tools"`
	HealthyTools  int                              `json:"healthy_tools"`
	DegradedTools int                              `json:"degraded_tools"`
	FailedTools   int                              `json:"failed_tools"`
	TestedAt      time.Time                        `json:"tested_at"`
	TestDuration  time.Duration                    `json:"test_duration"`
}

// Summary returns a human-readable summary.
func (r *RegistrySelfTestResult) Summary() string {
	return fmt.Sprintf("%d tools tested: %d healthy, %d degraded, %d failed (%d did not implement self-test)",
		r.TestedTools, r.HealthyTools, r.DegradedTools, r.FailedTools, r.TotalTools-r.TestedTools)
}

// IsHealthy returns true if all tested tools are OK.
func (r *RegistrySelfTestResult) IsHealthy() bool {
	return r.FailedTools == 0 && r.DegradedTools == 0
}

// SelfTestTool runs a self-test on a specific tool by name.
// Returns nil if the tool doesn't exist or isn't enabled.
func (r *Registry) SelfTestTool(ctx context.Context, name string, opts *types.SelfTestOptions) *types.SelfTestResult {
	r.mu.RLock()
	tool, exists := r.tools[name]
	enabled := r.isToolEnabledLocked(name)
	r.mu.RUnlock()
	if !exists || !enabled {
		return nil
	}

	tester, ok := tool.(types.SelfTester)
	if !ok {
		return &types.SelfTestResult{
			Status:   types.SelfTestStatusOK,
			Message:  fmt.Sprintf("Tool '%s' does not implement self-test (assumed functional)", name),
			TestedAt: time.Now(),
		}
	}

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return tester.SelfTest(toolCtx, opts)
}

// SelfTestAll runs self-tests on all enabled tools that implement SelfTester.
func (r *Registry) SelfTestAll(ctx context.Context, opts *types.SelfTestOptions) *RegistrySelfTestResult {
	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &RegistrySelfTestResult{
		Results:  make(map[string]*types.SelfTestResult),
		TestedAt: time.Now(),
	}

	availableTools := r.GetAvailableTools()
	result.TotalTools = len(availableTools)

	for name, tool := range availableTools {
		if tester, ok := tool.(types.SelfTester); ok {
			toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			testResult := tester.SelfTest(toolCtx, opts)
			cancel()

			result.Results[name] = testResult
			result.TestedTools++

			switch testResult.Status {
			case types.SelfTestStatusOK:
				result.HealthyTools++
			case types.SelfTestStatusDegraded:
				result.DegradedTools++
			case types.SelfTestStatusFailed:
				result.FailedTools++
			}
		}
	}

	result.TestDuration = time.Since(result.TestedAt)
	return result
}
