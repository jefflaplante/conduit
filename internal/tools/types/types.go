package types

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/schema"
)

// ErrMQTTPublishNotAllowed is returned when publish is attempted but not configured.
var ErrMQTTPublishNotAllowed = errors.New("mqtt: publishing is not allowed (publish_allowed is false)")

// ChannelSender interface for sending messages via channels
type ChannelSender interface {
	SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error
	GetChannelStatusMap() map[string]string
	GetAvailableTargets() []string
}

// GatewayService interface for gateway operations (implemented by gateway package)
type GatewayService interface {
	SendToSession(ctx context.Context, sessionKey, label, message string) error
	SendToSessionWake(ctx context.Context, sessionKey, label, message string) error
	SpawnSubAgent(ctx context.Context, task, agentId, model, label string, timeoutSeconds int) (string, error)
	SpawnSubAgentWithCallback(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, parentChannelID, parentUserID string, announce bool, skills []string) (string, error)
	SpawnSubAgentWithSkills(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, skills []string) (string, error)
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
	GetSystemPromptDebug(ctx context.Context, sessionKey string) (map[string]interface{}, error)

	// GetContextBudget returns a point-in-time snapshot of the session's
	// context-window consumption: latest prompt/completion tokens, the
	// model's declared window, percent used, and remaining tokens. Returns
	// the snapshot as a map for tool-layer consumption without exposing
	// the concrete ContextBudget struct (avoids cross-package coupling).
	GetContextBudget(ctx context.Context, sessionKey string) (map[string]interface{}, error)

	// GetFuelGaugeMap returns a point-in-time snapshot of rate-limit headroom
	// and rolling-window AI token consumption as a JSON-serializable map.
	// topN caps the number of per-tier rate-limit identifiers returned
	// (most-pressured first); pass 0 or a negative value to include all.
	// Returns the snapshot as a map to avoid cross-package coupling with the
	// gateway.FuelGauge struct.
	GetFuelGaugeMap(topN int) map[string]interface{}

	// Skill hot-reload
	ReloadSkillTools(ctx context.Context) (int, error)

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
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Schedule string   `json:"schedule"`
	Type     string   `json:"type"` // "go" or "system"
	Command  string   `json:"command"`
	Model    string   `json:"model,omitempty"`
	Target   string   `json:"target,omitempty"`
	Enabled  bool     `json:"enabled"`
	OneShot  bool     `json:"oneshot,omitempty"`
	Skills   []string `json:"skills,omitempty"`
}

// SearchService provides FTS5-backed full-text search over documents, messages, and beads.
type SearchService interface {
	SearchDocuments(ctx context.Context, query string, limit int) ([]fts.DocumentResult, error)
	SearchMessages(ctx context.Context, query string, limit int) ([]fts.MessageResult, error)
	SearchBeads(ctx context.Context, query string, limit int, statusFilter string) ([]fts.BeadsResult, error)
	Search(ctx context.Context, query string, limit int) ([]fts.SearchResult, error)
}

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

// MQTTEvent represents a single MQTT message for the tool layer.
type MQTTEvent struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
	Retained  bool            `json:"retained,omitempty"`
}

// MQTTTopicSummary provides an overview of a single MQTT topic.
type MQTTTopicSummary struct {
	Topic      string          `json:"topic"`
	EventCount int             `json:"event_count"`
	LastEvent  time.Time       `json:"last_event"`
	LastValue  json.RawMessage `json:"last_value"`
}

// MQTTServiceStatus reports the current state of the MQTT service.
type MQTTServiceStatus struct {
	Connected        bool     `json:"connected"`
	BrokerURL        string   `json:"broker_url"`
	SubscribedTopics []string `json:"subscribed_topics"`
	ActiveTopics     int      `json:"active_topics"`
	TotalEvents      int64    `json:"total_events"`
	PublishAllowed   bool     `json:"publish_allowed"`
}

// MQTTPublishResult confirms broker acknowledgement of a published message.
type MQTTPublishResult struct {
	Topic       string `json:"topic"`
	QoS         byte   `json:"qos"`
	Retained    bool   `json:"retained"`
	PayloadSize int    `json:"payload_size"`
	BrokerAck   bool   `json:"broker_ack"` // true = broker confirmed receipt (QoS >= 1)
}

// MQTTDevice represents a parsed zigbee2mqtt device for the tool layer.
type MQTTDevice struct {
	IEEEAddress  string `json:"ieee_address"`
	FriendlyName string `json:"friendly_name"`
	Type         string `json:"type"`
	ModelID      string `json:"model_id"`
	Manufacturer string `json:"manufacturer"`
	Description  string `json:"description"`
	Supported    bool   `json:"supported"`
	Disabled     bool   `json:"disabled"`
	MQTTTopic    string `json:"mqtt_topic"` // synthetic: zigbee2mqtt/<friendly_name>
}

// MQTTRetainedMessage represents a retained MQTT message for the tool layer.
type MQTTRetainedMessage struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// MQTTService provides MQTT event data to tools.
type MQTTService interface {
	Status() MQTTServiceStatus
	Recent(limit int) []MQTTEvent
	RecentForTopic(topic string, limit int) []MQTTEvent
	RecentMatching(pattern string, limit int) []MQTTEvent
	Topics() []MQTTTopicSummary
	Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*MQTTPublishResult, error)
	Devices() []MQTTDevice
	RetainedByPrefix(prefix string) []MQTTRetainedMessage
	RetainedPrefixes() []string
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

// VectorIndexer provides on-demand vector indexing of workspace files.
type VectorIndexer interface {
	EnsureIndexed(ctx context.Context, relativePath string)
}

// BrainTier represents a memory tier in the cognitive architecture.
type BrainTier string

const (
	BrainTierLongTerm BrainTier = "longterm"
	BrainTierWorking  BrainTier = "working"
	BrainTierScratch  BrainTier = "scratch"
)

// BrainEntry represents a single fact stored in the brain.
type BrainEntry struct {
	Key             string     `json:"key"`
	Value           string     `json:"value"`
	Tier            BrainTier  `json:"tier"`
	CreatedAt       time.Time  `json:"created_at"`
	AccessedAt      time.Time  `json:"accessed_at"`
	AccessCount     int        `json:"access_count"`
	Salience        float64    `json:"salience"`
	Warmth          float64    `json:"warmth,omitempty"`
	Source          string     `json:"source,omitempty"`
	Stale           bool       `json:"stale,omitempty"`
	ClusterExpanded bool       `json:"cluster_expanded,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

// BrainStatus reports the current state of the brain service.
type BrainStatus struct {
	LTMEntries   int      `json:"ltm_entries"`
	WMEntries    int      `json:"wm_entries"`
	ScratchDepth int      `json:"scratch_depth"`
	AvgSalience  float64  `json:"avg_salience,omitempty"`
	HottestKeys  []string `json:"hottest_keys,omitempty"`
	ColdestKeys  []string `json:"coldest_keys,omitempty"`
	ExpiringSoon int      `json:"expiring_soon,omitempty"`
}

// ConsolidationReport summarizes a consolidation sweep.
type ConsolidationReport struct {
	PromotedCount int      `json:"promoted_count"`
	EvictedCount  int      `json:"evicted_count"`
	LTMSize       int      `json:"ltm_size"`
	PromotedKeys  []string `json:"promoted_keys,omitempty"`
	EvictedKeys   []string `json:"evicted_keys,omitempty"`
}

// BrainClusterResult holds the result of a namespace-clustered recall.
// Direct entries matched the query keywords; Cluster entries share a namespace
// prefix with a direct match but didn't match the keywords themselves.
type BrainClusterResult struct {
	Direct  []*BrainEntry `json:"direct"`
	Cluster []*BrainEntry `json:"cluster"`
}

// BrainBulkEntry is a single entry passed to BrainService.StoreBulk. Tier
// defaults to BrainTierWorking when empty; BrainTierScratch is rejected.
type BrainBulkEntry struct {
	Key    string    `json:"key"`
	Value  string    `json:"value"`
	Tier   BrainTier `json:"tier,omitempty"`
	Source string    `json:"source,omitempty"`
}

// BrainService provides tiered memory (LTM + working + scratchpad) to tools.
type BrainService interface {
	Store(ctx context.Context, key, value string, tier BrainTier, source string) error
	// StoreWithTTL stores an entry that expires after the given duration.
	// A zero ttl means no expiry (equivalent to Store).
	StoreWithTTL(ctx context.Context, key, value string, tier BrainTier, source string, ttl time.Duration) error
	StoreBulk(ctx context.Context, entries []BrainBulkEntry) error
	Get(ctx context.Context, key string) (*BrainEntry, error)
	Recall(ctx context.Context, query string, limit int) ([]*BrainEntry, error)
	// RecallWithContext performs fuzzy recall with an optional context string.
	// If contextStr is non-empty, entries whose key or value contain any context
	// token get a ranking boost; it never filters results. An empty contextStr
	// is equivalent to Recall.
	RecallWithContext(ctx context.Context, query string, limit int, contextStr string) ([]*BrainEntry, error)
	// RecallWithCluster performs a recall augmented by namespace clustering.
	// Returns both direct keyword matches and neighbouring entries that share
	// a namespace prefix with direct matches (BFS expansion).
	RecallWithCluster(ctx context.Context, query string, limit int) (*BrainClusterResult, error)
	List(ctx context.Context, prefix string, sourcePrefix string) ([]*BrainEntry, error)
	Delete(ctx context.Context, key string) error
	Push(ctx context.Context, userID, value string) error
	Pop(ctx context.Context, userID string) (string, error)
	Peek(ctx context.Context, userID string) (string, error)
	Promote(ctx context.Context, key string) error
	Consolidate(ctx context.Context, autoPromote bool) (*ConsolidationReport, error)
	WorkingMemoryEntries(ctx context.Context) []*BrainEntry
	Status(ctx context.Context) (*BrainStatus, error)
	Close() error
}

// BrainFTSResult represents a single FTS5 search result from brain LTM.
type BrainFTSResult struct {
	Key    string  `json:"key"`
	Value  string  `json:"value"`
	Source string  `json:"source"`
	Rank   float64 `json:"rank"`
}

// BrainFTSSearcher provides FTS5-backed search over brain LTM entries.
type BrainFTSSearcher interface {
	SearchBrain(ctx context.Context, query string, limit int) ([]BrainFTSResult, error)
}

// REMCycleReport is the tool-layer representation of a REM cycle execution report.
type REMCycleReport struct {
	Date          string                 `json:"date"`
	DryRun        bool                   `json:"dry_run"`
	Triage        map[string]interface{} `json:"triage,omitempty"`
	Consolidation map[string]interface{} `json:"consolidation,omitempty"`
	Pruning       map[string]interface{} `json:"pruning,omitempty"`
	Integration   map[string]interface{} `json:"integration,omitempty"`
	Grooming      map[string]interface{} `json:"grooming,omitempty"`
}

// REMCycleRunner executes the REM sleep consolidation cycle.
type REMCycleRunner interface {
	RunREMCycle(ctx context.Context, phases []string, dryRun bool) (*REMCycleReport, error)
}

// ReflectionEntry is the tool-layer representation of a reflection data point.
type ReflectionEntry struct {
	ID          string        `json:"id"`
	SessionKey  string        `json:"session_key"`
	Timestamp   time.Time     `json:"timestamp"`
	Source      string        `json:"source"`
	Type        string        `json:"type"`
	Tool        string        `json:"tool,omitempty"`
	Outcome     string        `json:"outcome"`
	RetryCount  int           `json:"retry_count"`
	Duration    time.Duration `json:"duration"`
	Insight     string        `json:"insight,omitempty"`
	Score       int           `json:"score"`
	Tags        []string      `json:"tags,omitempty"`
	RelatedKeys []string      `json:"related_keys,omitempty"`
}

// ReflectionToolStat holds aggregated tool outcome statistics.
type ReflectionToolStat struct {
	Tool        string        `json:"tool"`
	Outcome     string        `json:"outcome"`
	Count       int           `json:"count"`
	AvgDuration time.Duration `json:"avg_duration"`
	AvgRetries  float64       `json:"avg_retries"`
}

// ReflectionService provides access to the reflection store for tools and middleware.
type ReflectionService interface {
	Insert(ctx context.Context, entry *ReflectionEntry) error
	InsertBatch(ctx context.Context, entries []*ReflectionEntry) error
	QueryBySession(ctx context.Context, sessionKey string) ([]*ReflectionEntry, error)
	QueryUnprocessed(ctx context.Context) ([]*ReflectionEntry, error)
	MarkProcessed(ctx context.Context, ids []string) error
	Groom(ctx context.Context, retentionDays int) (int, error)
	QueryToolStats(ctx context.Context, since time.Time) ([]ReflectionToolStat, error)
}

// VisionAnalyzer performs multimodal image analysis.
// Implementations wrap a multimodal LLM (e.g., Anthropic Claude with vision) and
// return a natural-language description or answer for the supplied image + prompt.
//
// Intentionally narrow (single method) so the tool layer does not depend on the
// full ai.Provider surface. The gateway wires a concrete adapter that delegates
// to the configured AI router.
type VisionAnalyzer interface {
	// AnalyzeImage sends the image bytes and prompt to a multimodal model and
	// returns the textual analysis. mediaType is the image MIME type (e.g.,
	// "image/jpeg", "image/png"); callers should pass "" if unknown, in which
	// case implementations MAY reject the request or fall back to "image/jpeg".
	AnalyzeImage(ctx context.Context, image []byte, mediaType string, prompt string) (string, error)
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

	// Schema enhancement
	SchemaBuilder *schema.Builder // For enhancing tool schemas with discovery data

	// Debug
	DebugLog *debuglog.RingBuffer // In-memory ring buffer for debug log entries (nil-safe)
}

// Context key types for per-request values (replaces shared mutable fields).
type ctxKeyChannelID struct{}
type ctxKeyUserID struct{}
type ctxKeySessionKey struct{}
type ctxKeyWakeSource struct{}

// Wake source values attached to the context when a session is woken. Lets the
// agent system and tools tell a normal user message apart from a callback, and
// further tell announced sub-agent results (user already saw them) from silent
// ones (user hasn't — parent must decide whether to surface).
const (
	WakeSourceInterSession      = "inter_session"
	WakeSourceSubAgentCallback  = "sub_agent_callback"  // kept for backwards compatibility
	WakeSourceSubAgentAnnounced = "sub_agent_announced" // raw result already posted to channel
	WakeSourceSubAgentSilent    = "sub_agent_silent"    // raw result NOT posted; parent must decide
	WakeSourceHeartbeat         = "heartbeat"
)

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

// WithWakeSource attaches a wake-source tag to ctx (e.g. "sub_agent_callback").
// An empty source is a no-op so callers don't have to branch on it.
func WithWakeSource(ctx context.Context, source string) context.Context {
	if source == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyWakeSource{}, source)
}

// WakeSource returns the wake-source tag from ctx, or "" if the request is not a wake.
func WakeSource(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyWakeSource{}).(string); ok {
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

// SelfTestStatus represents the functional status of a tool.
type SelfTestStatus string

const (
	// SelfTestStatusOK indicates the tool is fully functional.
	SelfTestStatusOK SelfTestStatus = "ok"

	// SelfTestStatusDegraded indicates partial functionality (some features work).
	SelfTestStatusDegraded SelfTestStatus = "degraded"

	// SelfTestStatusFailed indicates the tool is not functional.
	SelfTestStatusFailed SelfTestStatus = "failed"
)

// SelfTestOptions configures how the self-test runs.
type SelfTestOptions struct {
	// Verbose requests additional detail in the result.
	Verbose bool

	// IncludeExamples requests that the result include usage examples if the test passes.
	IncludeExamples bool

	// CheckDependencies requests explicit dependency verification.
	CheckDependencies bool
}

// DefaultSelfTestOptions returns sensible defaults.
func DefaultSelfTestOptions() *SelfTestOptions {
	return &SelfTestOptions{
		Verbose:           false,
		IncludeExamples:   true,
		CheckDependencies: true,
	}
}

// DependencyStatus describes the health of a single dependency.
type DependencyStatus struct {
	// Name identifies the dependency (e.g., "ChannelSender", "MQTTService").
	Name string `json:"name"`

	// Required indicates if this dependency is essential for basic functionality.
	Required bool `json:"required"`

	// Available indicates if the dependency is currently accessible.
	Available bool `json:"available"`

	// Status provides more detail (e.g., "connected", "offline", "rate_limited").
	Status string `json:"status,omitempty"`

	// Message provides additional context about the dependency state.
	Message string `json:"message,omitempty"`
}

// SelfTestResult contains the outcome of a tool's self-test.
type SelfTestResult struct {
	// Status indicates whether the tool is functional.
	Status SelfTestStatus `json:"status"`

	// Message provides a human-readable summary of the test result.
	Message string `json:"message"`

	// Dependencies lists the status of required services/resources.
	Dependencies []DependencyStatus `json:"dependencies,omitempty"`

	// Capabilities lists what the tool can currently do.
	Capabilities []string `json:"capabilities,omitempty"`

	// UnavailableCapabilities lists features that are currently non-functional.
	UnavailableCapabilities []string `json:"unavailable_capabilities,omitempty"`

	// Examples provides usage examples when the tool is functional.
	Examples []ToolExample `json:"examples,omitempty"`

	// Suggestions provides actionable hints when the tool has issues.
	Suggestions []string `json:"suggestions,omitempty"`

	// TestDuration records how long the self-test took.
	TestDuration time.Duration `json:"test_duration"`

	// TestedAt records when the test was performed.
	TestedAt time.Time `json:"tested_at"`

	// Details contains additional diagnostic information (verbose mode).
	Details map[string]interface{} `json:"details,omitempty"`
}

// IsOK returns true if the tool is fully functional.
func (r *SelfTestResult) IsOK() bool {
	return r.Status == SelfTestStatusOK
}

// IsFunctional returns true if the tool has at least partial functionality.
func (r *SelfTestResult) IsFunctional() bool {
	return r.Status == SelfTestStatusOK || r.Status == SelfTestStatusDegraded
}

// SelfTester is an optional interface for tools that can verify their own functionality.
// AI models may call this to verify a tool works before relying on it for critical tasks.
type SelfTester interface {
	// SelfTest performs a functional check of the tool and returns a diagnostic result.
	// The test should be lightweight (ideally < 1 second) and non-destructive.
	SelfTest(ctx context.Context, opts *SelfTestOptions) *SelfTestResult
}
