package types

import (
	"context"
	"net/http"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/tools/schema"
)

// ChannelSender interface for sending messages via channels
type ChannelSender interface {
	SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error
	GetChannelStatusMap() map[string]string
	GetAvailableTargets() []string
}

// GatewayService interface for gateway operations (implemented by gateway package)
type GatewayService interface {
	SendToSession(ctx context.Context, sessionKey, label, message string) error
	SpawnSubAgent(ctx context.Context, task, agentId, model, label string, timeoutSeconds int) (string, error)
	SpawnSubAgentWithCallback(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, parentChannelID, parentUserID string, announce bool) (string, error)
	GetSessionStatus(ctx context.Context, sessionKey string) (map[string]interface{}, error)
	GetGatewayStatus() (map[string]interface{}, error)
	RestartGateway(ctx context.Context) error
	GetChannelStatus() (map[string]interface{}, error)
	EnableChannel(ctx context.Context, channelID string) error
	DisableChannel(ctx context.Context, channelID string) error
	GetConfiguration() (map[string]interface{}, error)
	UpdateConfiguration(ctx context.Context, config map[string]interface{}) error
	GetMetrics() (map[string]interface{}, error)
	GetVersion() string

	// Scheduler operations
	ScheduleJob(job *SchedulerJob) error
	CancelJob(jobID string) error
	ListJobs() []*SchedulerJob
	EnableJob(jobID string) error
	DisableJob(jobID string) error
	RunJobNow(jobID string) error
	GetSchedulerStatus() map[string]interface{}
}

// SchedulerJob represents a scheduled job (mirrors scheduler.Job to avoid import cycle)
type SchedulerJob struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Schedule string `json:"schedule"`
	Type     string `json:"type"` // "go" or "system"
	Command  string `json:"command"`
	Model    string `json:"model,omitempty"`
	Target   string `json:"target,omitempty"`
	Enabled  bool   `json:"enabled"`
	OneShot  bool   `json:"oneshot,omitempty"`
}

// SearchService provides FTS5-backed full-text search over documents, messages, and beads.
type SearchService interface {
	SearchDocuments(ctx context.Context, query string, limit int) ([]fts.DocumentResult, error)
	SearchMessages(ctx context.Context, query string, limit int) ([]fts.MessageResult, error)
	SearchBeads(ctx context.Context, query string, limit int, statusFilter string) ([]fts.BeadsResult, error)
	Search(ctx context.Context, query string, limit int) ([]fts.SearchResult, error)
}

// VectorSearchResult represents a single result from vector/semantic search.
type VectorSearchResult struct {
	ID       string            `json:"id"`
	Score    float64           `json:"score"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata"`
}

// VectorService provides vector/semantic search capabilities.
type VectorService interface {
	Search(ctx context.Context, query string, limit int) ([]VectorSearchResult, error)
	Index(ctx context.Context, id, content string, metadata map[string]string) error
	Remove(ctx context.Context, id string) error
	Close() error
}

// ToolServices provides access to services for tools (no direct gateway dependency)
type ToolServices struct {
	SessionStore  *sessions.Store
	ConfigMgr     *config.Config
	WebClient     *http.Client
	SkillsManager *skills.Manager
	ChannelSender ChannelSender  // Interface for channel operations
	Gateway       GatewayService // Interface for gateway operations
	Searcher      SearchService  // FTS5 full-text search
	VectorSearch  VectorService  // Optional vector/semantic search

	// Schema enhancement
	SchemaBuilder *schema.Builder // For enhancing tool schemas with discovery data
}

// Context key types for per-request values (replaces shared mutable fields).
type ctxKeyChannelID struct{}
type ctxKeyUserID struct{}
type ctxKeySessionKey struct{}

// WithRequestContext attaches per-request channel, user, and session info to ctx.
func WithRequestContext(ctx context.Context, channelID, userID, sessionKey string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyChannelID{}, channelID)
	ctx = context.WithValue(ctx, ctxKeyUserID{}, userID)
	ctx = context.WithValue(ctx, ctxKeySessionKey{}, sessionKey)
	return ctx
}

// RequestChannelID returns the channel ID from ctx, or "" if unset.
func RequestChannelID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyChannelID{}).(string); ok {
		return v
	}
	return ""
}

// RequestUserID returns the user ID from ctx, or "" if unset.
func RequestUserID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyUserID{}).(string); ok {
		return v
	}
	return ""
}

// RequestSessionKey returns the session key from ctx, or "" if unset.
func RequestSessionKey(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeySessionKey{}).(string); ok {
		return v
	}
	return ""
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	Success      bool                   `json:"success"`
	Content      string                 `json:"content"`
	Error        string                 `json:"error,omitempty"`
	Data         map[string]interface{} `json:"data,omitempty"`
	FallbackUsed bool                   `json:"fallback_used,omitempty"`
	CacheHit     bool                   `json:"cache_hit,omitempty"`
	Retries      int                    `json:"retries,omitempty"`

	// Enhanced error information (OCGO-033)
	ErrorDetails *ToolErrorDetails `json:"error_details,omitempty"`
}

// ToolErrorDetails provides structured error information for rich error messages
type ToolErrorDetails struct {
	Type            string                 `json:"error_type"`
	Parameter       string                 `json:"parameter,omitempty"`
	ProvidedValue   interface{}            `json:"provided_value,omitempty"`
	AvailableValues []string               `json:"available_values,omitempty"`
	Examples        []string               `json:"examples,omitempty"`
	Suggestions     []string               `json:"suggestions,omitempty"`
	Context         map[string]interface{} `json:"context,omitempty"`
}

// NewErrorResult creates a ToolResult with rich error information
func NewErrorResult(errorType, message string) *ToolResult {
	return &ToolResult{
		Success: false,
		Error:   message,
		ErrorDetails: &ToolErrorDetails{
			Type: errorType,
		},
	}
}

// WithParameter adds parameter information to an error result
func (r *ToolResult) WithParameter(name string, value interface{}) *ToolResult {
	if r.ErrorDetails != nil {
		r.ErrorDetails.Parameter = name
		r.ErrorDetails.ProvidedValue = value
	}
	return r
}

// WithAvailableValues adds available alternatives to an error result
func (r *ToolResult) WithAvailableValues(values []string) *ToolResult {
	if r.ErrorDetails != nil {
		r.ErrorDetails.AvailableValues = values
	}
	return r
}

// WithExamples adds example values to an error result
func (r *ToolResult) WithExamples(examples []string) *ToolResult {
	if r.ErrorDetails != nil {
		r.ErrorDetails.Examples = examples
	}
	return r
}

// WithSuggestions adds actionable suggestions to an error result
func (r *ToolResult) WithSuggestions(suggestions []string) *ToolResult {
	if r.ErrorDetails != nil {
		r.ErrorDetails.Suggestions = suggestions
	}
	return r
}

// WithContext adds system state context to an error result
func (r *ToolResult) WithContext(context map[string]interface{}) *ToolResult {
	if r.ErrorDetails != nil {
		r.ErrorDetails.Context = context
	}
	return r
}

// ValidationResult contains parameter validation results with helpful guidance
type ValidationResult struct {
	Valid       bool              `json:"valid"`
	Errors      []ValidationError `json:"errors,omitempty"`
	Suggestions []string          `json:"suggestions,omitempty"`
	Warnings    []ValidationError `json:"warnings,omitempty"` // Non-fatal issues
}

// ValidationError provides detailed information about a parameter validation failure
type ValidationError struct {
	Parameter       string        `json:"parameter"`
	Message         string        `json:"message"`
	ProvidedValue   interface{}   `json:"provided_value,omitempty"`
	AvailableValues []string      `json:"available_values,omitempty"`
	Examples        []interface{} `json:"examples,omitempty"`
	DiscoveryHint   string        `json:"discovery_hint,omitempty"` // CLI command to discover values
	ErrorType       string        `json:"error_type,omitempty"`     // "missing", "invalid_format", "permission_denied", etc.
}

// ToolExample represents an example usage of a tool
type ToolExample struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Args        map[string]interface{} `json:"args"`
	Expected    string                 `json:"expected,omitempty"` // Expected outcome description
}

// Tool defines the interface for executable tools
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error)
}

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
