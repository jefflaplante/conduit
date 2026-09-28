package types

import (
	"context"
	"time"

	"conduit/internal/fts"
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
