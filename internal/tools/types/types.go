package types

import (
	"context"
	"net/http"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/schema"
)

// ToolExecutor provides a way to execute tools by name.
// This is the canonical interface for tool execution, used by SRE, planning, and chain tools.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error)
}

// ToolRegistry extends ToolExecutor with tool discovery.
// Used by tools that need to enumerate available tools (e.g., chain tool).
type ToolRegistry interface {
	ToolExecutor
	GetAvailableTools() map[string]Tool
}

// ToolServices provides access to services for tools (no direct gateway dependency)
type ToolServices struct {
	SessionStore  *sessions.Store
	ConfigMgr     *config.Config
	WebClient     *http.Client
	SkillsManager *skills.Manager
	ChannelSender ChannelSender     // Interface for channel operations
	Gateway       GatewayService    // Interface for gateway operations
	Searcher      SearchService     // FTS5 full-text search
	VectorSearch  VectorService     // Optional vector/semantic search
	VectorIndexer VectorIndexer     // Optional on-demand vector indexer
	MQTTService   MQTTService       // Optional MQTT event ingest
	Brain         BrainService      // Optional tiered memory (LTM + working + scratchpad)
	BrainFTS      BrainFTSSearcher  // Optional FTS5 search over brain LTM
	REMCycle      REMCycleRunner    // Optional REM sleep cycle runner
	Reflection    ReflectionService // Optional SPAR reflection store
	Vision        VisionAnalyzer    // Optional multimodal image analysis (Anthropic vision, etc.)

	// Approvals gates risky tool operations behind a human "YES <code>"
	// reply on the originating channel (conduit-c8ct, conduit-w3l7). Nil
	// means no approval channel: gated operations fail closed.
	Approvals approval.Requester

	// Schema enhancement
	SchemaBuilder *schema.Builder // For enhancing tool schemas with discovery data

	// Debug
	DebugLog *debuglog.RingBuffer // In-memory ring buffer for debug log entries (nil-safe)
}

// Tool defines the interface for executable tools
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error)
}

// OptionalToolFactory creates an optional tool from services and config.
// Returns (nil, nil) if the tool is compiled in but disabled via config.
// Returns (nil, error) if the tool cannot be initialized due to missing dependencies.
// This signature supports both internal build-tagged tools and future external modules.
type OptionalToolFactory func(services *ToolServices, cfg *config.Config) (Tool, error)

// EnhancedSchemaProvider is an optional interface for tools that provide
// schema enhancement hints (examples, validation constraints, discovery).
// Tools that don't implement this interface use their static Parameters() as-is.
type EnhancedSchemaProvider interface {
	// GetSchemaHints returns hints for schema enhancement
	GetSchemaHints() map[string]schema.SchemaHints
}

// ParameterValidator is an optional interface for tools that want to validate
// parameters before execution and provide helpful error messages.
type ParameterValidator interface {
	// ValidateParameters checks parameters and returns validation result with guidance
	ValidateParameters(ctx context.Context, args map[string]interface{}) *ValidationResult
}

// ParameterDiscoverer is an optional interface for tools that can discover
// available parameter values dynamically (e.g., available channels, files).
type ParameterDiscoverer interface {
	// DiscoverParameterValues returns available values for a specific parameter
	DiscoverParameterValues(ctx context.Context, parameter string) ([]string, error)
}

// UsageExampleProvider is an optional interface for tools that provide
// usage examples for better user guidance.
type UsageExampleProvider interface {
	// GetUsageExamples returns example invocations of the tool
	GetUsageExamples() []ToolExample
}

// ActionDoc describes a single action within a multi-action tool.
type ActionDoc struct {
	Description    string
	RequiredParams []string
	OptionalParams []string
	Returns        string
}

// ActionDocProvider is an optional interface for multi-action tools that
// document each action's parameters and return values individually.
type ActionDocProvider interface {
	GetActionDocs() map[string]ActionDoc
}
