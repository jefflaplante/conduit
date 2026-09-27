package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/config"
	"conduit/internal/constants"
	"conduit/internal/sessions"
)

// ExecutionEngine interface for dependency injection
type ExecutionEngine interface {
	HandleToolCallFlow(ctx context.Context, provider Provider, initialReq *GenerateRequest, initialResp *GenerateResponse) (ConversationResponse, error)
}

// ConversationResponse represents complete conversation with tool results (interface)
type ConversationResponse interface {
	GetContent() string
	GetUsage() *Usage
	GetSteps() int
	HasToolResults() bool
}

// ProviderMeta holds metadata about a configured provider.
type ProviderMeta struct {
	Name          string
	Type          string // "anthropic", "openai", "ollama"
	DefaultModel  string
	ContextWindow int    // Configured context window override (0 = auto-detect from model)
	FallbackModel string // Fallback model for quota/auth errors (bd-6tb)
}

// Router handles AI model interactions
type Router struct {
	mu              sync.RWMutex
	providers       map[string]Provider
	providerMeta    map[string]ProviderMeta
	default_        string
	agentSystem     AgentSystem     // Add agent system to router
	executionEngine ExecutionEngine // Tool execution engine (interface, not pointer)
	sessionStore    *sessions.Store // Session store for retrieving message history
	usageTracker    *UsageTracker
	historyConfig   *config.HistoryConfig // Token-aware history retrieval config
	historyCuts     historyCutCache       // per-session history cut, for a stable cached prefix (conduit-31jg.63)

	// Smart routing components
	modelSelector      ModelSelector
	complexityAnalyzer *ComplexityAnalyzer
	smartRoutingCfg    *config.SmartRoutingConfig
	contextEngine      ContextEngine
	pricingResolver    *PricingResolver

	// Per-session turn serialization: prevents concurrent LLM turns on the
	// same session (e.g., a normal user message and an inter-session wake
	// firing at the same time). Keys are session.Key; values are
	// chan struct{} (capacity 1) so a queued turn can give up waiting when
	// its context is cancelled (conduit-31jg.23).
	turnLocks sync.Map

	// maxTokensForChain caps generated output per round trip (bd-1k3o).
	// 0 = default 4000. Configurable via ai.max_tokens for report-heavy
	// workloads where 4000 truncates deliverables mid-sentence.
	maxTokensForChain int
}

// defaultChainMaxTokens is the fallback per-round-trip output cap.
const defaultChainMaxTokens = 4000

// chainMaxTokens returns the configured per-round-trip output cap, or the
// default when unset (bd-1k3o).
func (r *Router) chainMaxTokens() int {
	if r.maxTokensForChain > 0 {
		return r.maxTokensForChain
	}
	return defaultChainMaxTokens
}

// turnSem returns the capacity-1 semaphore guarding sessionKey's turns.
func (r *Router) turnSem(sessionKey string) chan struct{} {
	v, _ := r.turnLocks.LoadOrStore(sessionKey, make(chan struct{}, 1))
	return v.(chan struct{})
}

// lockSession acquires the per-session turn lock and returns an unlock function.
// Empty session keys are a no-op (returns a no-op unlock).
func (r *Router) lockSession(sessionKey string) func() {
	if sessionKey == "" {
		return func() {}
	}
	sem := r.turnSem(sessionKey)
	sem <- struct{}{}
	return func() { <-sem }
}

// turnLease marks a context as running inside a turn whose per-session lock
// was taken by AcquireTurn (conduit-31jg.35).
type turnLease struct {
	router *Router
	key    string
	held   atomic.Bool
}

type turnLeaseKey struct{}

// AcquireTurn takes the per-session turn lock on behalf of a caller that owns
// the whole turn (the gateway TurnRunner, conduit-31jg.35), which persists the
// user message and the reply while holding it so transcript order matches
// turn order (conduit-31jg.22).
//
// It blocks until the lock is free or ctx is done, so a queued turn cancelled
// by /stop stops waiting (conduit-31jg.23). The returned context carries a
// lease: GenerateResponseWithTools*/GenerateResponseStreaming (and the smart
// routing wrappers over them) called with it, or with a context derived from
// it, run UNLOCKED for that session instead of deadlocking on the lock the
// caller already holds. release is idempotent; after it, the lease no longer
// bypasses the lock, so goroutines that outlive the turn lock normally.
func (r *Router) AcquireTurn(ctx context.Context, sessionKey string) (context.Context, func(), error) {
	if sessionKey == "" {
		return ctx, func() {}, nil
	}
	sem := r.turnSem(sessionKey)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}
	if err := ctx.Err(); err != nil { // select picks randomly when both are ready
		<-sem
		return ctx, func() {}, err
	}
	lease := &turnLease{router: r, key: sessionKey}
	lease.held.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.held.Store(false)
			<-sem
		})
	}
	return context.WithValue(ctx, turnLeaseKey{}, lease), release, nil
}

// lockSessionCtx is lockSession unless ctx carries a live AcquireTurn lease
// for this router and session: the caller then already holds the lock and
// this is a no-op (conduit-31jg.35).
func (r *Router) lockSessionCtx(ctx context.Context, sessionKey string) func() {
	if sessionKey == "" {
		return func() {}
	}
	if ctx == nil { // some legacy callers/tests pass a nil context
		return r.lockSession(sessionKey)
	}
	if l, ok := ctx.Value(turnLeaseKey{}).(*turnLease); ok && l.router == r && l.key == sessionKey && l.held.Load() {
		return func() {}
	}
	return r.lockSession(sessionKey)
}

func sessionKeyOf(s *sessions.Session) string {
	if s == nil {
		return ""
	}
	return s.Key
}

// AgentProcessedResponse represents processed response from agent (to avoid circular imports)
type AgentProcessedResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Silent    bool       `json:"silent,omitempty"`
	Modified  bool       `json:"modified,omitempty"`
}

// AgentSystem interface for dependency injection
type AgentSystem interface {
	BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]SystemBlock, error)
	GetToolDefinitions(session *sessions.Session) []Tool
	ProcessResponse(ctx context.Context, response *GenerateResponse) (*AgentProcessedResponse, error)
}

// SystemBlock represents a system prompt block
type SystemBlock struct {
	Type string      `json:"type"`
	Text string      `json:"text,omitempty"`
	Meta interface{} `json:"meta,omitempty"`
	// Dynamic marks per-turn content (timestamp, wake context). Providers
	// with prompt caching place the system breakpoint on the last
	// non-dynamic block so these never invalidate the cached prefix.
	// conduit-31jg.14
	Dynamic bool `json:"dynamic,omitempty"`
}

// ProcessedResponse represents processed AI response
type ProcessedResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Silent    bool       `json:"silent,omitempty"`
	Modified  bool       `json:"modified,omitempty"`
}

// Provider defines the interface for AI providers
type Provider interface {
	Name() string
	GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
}

// StreamingProvider is an optional extension of Provider for streaming support.
// Providers that implement this interface can deliver token-by-token responses.
type StreamingProvider interface {
	Provider
	GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error)
}

// GenerateRequest represents a request to generate an AI response
type GenerateRequest struct {
	Messages  []ChatMessage `json:"messages"`
	Model     string        `json:"model,omitempty"`
	Tools     []Tool        `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// GenerateResponse represents an AI provider's response
type GenerateResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage,omitempty"`
	Partial   bool       `json:"partial,omitempty"` // True when response is incomplete due to mid-stream error
	// FinishReason is the provider-reported stop condition for the generation:
	// "stop" (natural end), "length" (hit max_tokens mid-generation — the
	// content is truncated, NOT a complete answer), "tool_calls" (model wants
	// tools), "content_filter". Empty when the provider did not report one.
	// bd-1k3o: parsed from OpenAI-compatible responses so the tool loop can
	// detect length-truncated finals instead of silently accepting them.
	FinishReason string `json:"finish_reason,omitempty"`
	// StopReason is the raw provider stop reason when it differs in
	// vocabulary from FinishReason (Anthropic stop_reason: "end_turn",
	// "max_tokens", "tool_use", "refusal", "pause_turn", ...). conduit-31jg.11.
	StopReason string `json:"stop_reason,omitempty"`
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role        string       `json:"role"` // "system", "user", "assistant", "tool"
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`   // For assistant messages with tool calls
	ToolCallID  string       `json:"tool_call_id,omitempty"` // For tool result messages
	Attachments []Attachment `json:"attachments,omitempty"`  // In-memory media attachments (images, etc.)
	// IsError marks a tool result message whose tool call failed (Go error or
	// Result.Success=false). Anthropic receives it as tool_result.is_error.
	// conduit-31jg.45
	IsError bool `json:"is_error,omitempty"`
	// SystemBlocks carries the agent's system prompt blocks on the leading
	// system message. Content still holds them joined with "\n\n" for
	// providers without block support; Anthropic sends the blocks so the
	// cache breakpoint lands on the static one. conduit-31jg.14
	SystemBlocks []SystemBlock `json:"system_blocks,omitempty"`
}

// Attachment represents media content attached to a message (e.g., images from Telegram).
// Carried in-memory only for the current request; never persisted to the database.
type Attachment struct {
	Type      string // "image", "document", "audio"
	MediaType string // MIME type: "image/jpeg", "image/png", etc.
	Data      []byte // Raw bytes
}

// Tool represents a tool/function that the AI can call
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall represents a tool function call from the AI
type ToolCall struct {
	ID   string                 `json:"id"`
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"arguments"`
}

// Usage represents token usage statistics
type Usage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	TotalTokens              int `json:"total_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	// ContextTokens is the full prompt size of the LAST round trip folded in
	// by Add (see Usage.Context). The fields above are whole-turn sums.
	// conduit-31jg.15
	ContextTokens int `json:"context_tokens,omitempty"`
	// CostUSD is the priced cost of the provider calls folded into this
	// usage; PricedCalls/UnpricedCalls count them (an unpriced call — model
	// with no known price — contributes 0 to CostUSD). Set per call by the
	// router's metering hook and summed by Add. conduit-31jg.64
	CostUSD       float64 `json:"cost_usd,omitempty"`
	PricedCalls   int     `json:"priced_calls,omitempty"`
	UnpricedCalls int     `json:"unpriced_calls,omitempty"`
}

// DefaultContextWindow is the fallback context window size in tokens.
const DefaultContextWindow = 200000

// ContextWindowSizes maps model ID prefixes to their context window sizes.
var ContextWindowSizes = map[string]int{
	// Anthropic
	"claude-opus-4-6":   200000,
	"claude-opus-4-5":   200000, // legacy
	"claude-sonnet-4":   200000,
	"claude-haiku-4-5":  200000,
	"claude-3-5-sonnet": 200000,
	"claude-3-5-haiku":  200000,
	"claude-3-opus":     200000,
	"claude-3-sonnet":   200000,
	"claude-3-haiku":    200000,
	// OpenAI
	"gpt-4o":        128000,
	"gpt-4-turbo":   128000,
	"gpt-4":         8192,
	"gpt-3.5-turbo": 16385,
	// Local / Ollama models
	"llama3":          8192,
	"llama3.1":        128000,
	"llama3.2":        128000,
	"llama3.3":        128000,
	"mistral":         32768,
	"mixtral":         32768,
	"codellama":       16384,
	"deepseek-coder":  16384,
	"deepseek-coder2": 16384,
	"qwen2.5":         32768,
	"qwen3.5":         131072,
	"phi-3":           128000,
	"gemma2":          8192,
}

// ContextWindowForModel returns the context window size for a given model.
// It tries an exact match first, then the LONGEST matching prefix, then the
// default. See LookupContextWindow.
func ContextWindowForModel(model string) int {
	size, _ := LookupContextWindow(model)
	return size
}

// LookupContextWindow resolves a model's context window and reports whether
// it matched a known entry (false = DefaultContextWindow was used).
//
// conduit-31jg.17: prefixes overlap (gpt-4/gpt-4o, llama3/llama3.1,
// deepseek-coder/deepseek-coder2), and the old first-match loop over the map
// was nondeterministic — "gpt-4o-2024-08-06" sometimes resolved to 8192 and
// trimRequestToFitContext then dropped nearly all history. The longest
// matching prefix is unique, so the result no longer depends on map order.
// A "provider/model" ID that matches nothing is retried without the prefix.
func LookupContextWindow(model string) (int, bool) {
	if model == "" {
		return DefaultContextWindow, false
	}
	if size, ok := longestPrefixContextWindow(model); ok {
		return size, true
	}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		if size, ok := longestPrefixContextWindow(model[i+1:]); ok {
			return size, true
		}
	}
	return DefaultContextWindow, false
}

func longestPrefixContextWindow(model string) (int, bool) {
	if size, ok := ContextWindowSizes[model]; ok {
		return size, true
	}
	best, bestLen := 0, 0
	for prefix, size := range ContextWindowSizes {
		if len(prefix) > bestLen && strings.HasPrefix(model, prefix) {
			best, bestLen = size, len(prefix)
		}
	}
	return best, bestLen > 0
}

// NewRouter creates a new AI router
func NewRouter(cfg config.AIConfig, agentSystem AgentSystem) (*Router, error) {
	router := &Router{
		providers:         make(map[string]Provider),
		providerMeta:      make(map[string]ProviderMeta),
		default_:          cfg.DefaultProvider,
		agentSystem:       agentSystem,
		usageTracker:      NewUsageTracker(),
		maxTokensForChain: cfg.MaxTokens,
	}
	// conduit-31jg.57: one resolver (ai.pricing_overrides + built-ins) for
	// every cost path the router owns.
	router.SetPricingResolver(NewPricingResolverFromConfig(cfg))

	return router, router.initializeProviders(cfg)
}

// NewRouterWithExecution creates a new AI router with tool execution
func NewRouterWithExecution(cfg config.AIConfig, agentSystem AgentSystem, executionEngine ExecutionEngine) (*Router, error) {
	router := &Router{
		providers:         make(map[string]Provider),
		providerMeta:      make(map[string]ProviderMeta),
		default_:          cfg.DefaultProvider,
		agentSystem:       agentSystem,
		executionEngine:   executionEngine,
		usageTracker:      NewUsageTracker(),
		maxTokensForChain: cfg.MaxTokens,
	}
	// conduit-31jg.57: one resolver (ai.pricing_overrides + built-ins) for
	// every cost path the router owns.
	router.SetPricingResolver(NewPricingResolverFromConfig(cfg))

	return router, router.initializeProviders(cfg)
}

// SetSessionStore sets the session store for retrieving message history
func (r *Router) SetSessionStore(store *sessions.Store) {
	r.sessionStore = store
}

// SetHistoryConfig sets the token-aware history retrieval configuration
func (r *Router) SetHistoryConfig(cfg *config.HistoryConfig) {
	r.historyConfig = cfg
}

// GetUsageTracker returns the router's usage tracker.
func (r *Router) GetUsageTracker() *UsageTracker {
	return r.usageTracker
}

// SetModelSelector sets the model selector for smart routing.
func (r *Router) SetModelSelector(selector ModelSelector) {
	r.modelSelector = selector
}

// SetComplexityAnalyzer sets the complexity analyzer for smart routing.
func (r *Router) SetComplexityAnalyzer(analyzer *ComplexityAnalyzer) {
	r.complexityAnalyzer = analyzer
}

// SetSmartRoutingConfig sets the smart routing configuration.
func (r *Router) SetSmartRoutingConfig(cfg *config.SmartRoutingConfig) {
	r.smartRoutingCfg = cfg
}

// SetContextEngine sets the context engine for context-aware model selection.
// When set, smart routing will query historical context to inform model selection.
// This is optional — smart routing works identically without a context engine.
func (r *Router) SetContextEngine(engine ContextEngine) {
	r.contextEngine = engine
}

// SetPricingResolver sets the pricing resolver for dynamic model pricing and
// hands it to the router's usage tracker (conduit-31jg.57).
func (r *Router) SetPricingResolver(pr *PricingResolver) {
	r.pricingResolver = pr
	if r.usageTracker != nil {
		r.usageTracker.SetPricingResolver(pr)
	}
}

// PricingResolver returns the router's resolver, or the package default.
func (r *Router) PricingResolver() *PricingResolver {
	if r != nil && r.pricingResolver != nil {
		return r.pricingResolver
	}
	return DefaultPricingResolver()
}

// ResolvePricing returns pricing for a model using the configured resolver,
// or falls back to the default pricing matrix if no resolver is set.
func (r *Router) ResolvePricing(model string) ModelPricing {
	return r.PricingResolver().PricingForModel(model)
}

// effectiveRoute returns the (provider, model) a request with the given
// provider/model overrides is served by: the provider inferred from the
// model when none is named (or the default provider), and the provider's
// configured default model when the model is empty. conduit-31jg.57
func (r *Router) effectiveRoute(provider, model string) (string, string) {
	if model != "" && (provider == "" || strings.Contains(model, "/")) {
		if p := r.ResolveProviderForModel(model); p != "" {
			provider = p
		}
	}
	if provider == "" {
		provider = r.default_
	}
	if model == "" {
		r.mu.RLock()
		model = r.providerMeta[provider].DefaultModel
		r.mu.RUnlock()
	}
	return provider, model
}

// TurnCost prices a turn's usage for the session cost counters.
// priced=false means the model has no known price: the cost is unknown (the
// returned 0 must not be read as free). conduit-31jg.57
func (r *Router) TurnCost(provider, model string, u Usage) (cost float64, priced bool) {
	// conduit-31jg.64: a metered turn carries the exact sum of its calls'
	// costs, each priced on the (provider, model) that served it (failover
	// and fallback routes included).
	if u.PricedCalls+u.UnpricedCalls > 0 {
		return u.CostUSD, u.UnpricedCalls == 0
	}
	provider, model = r.effectiveRoute(provider, model)
	return r.PricingResolver().Cost(provider, model, u)
}

// IsSmartRoutingEnabled returns true if smart routing is configured and enabled.
func (r *Router) IsSmartRoutingEnabled() bool {
	return r.smartRoutingCfg != nil && r.smartRoutingCfg.Enabled && r.modelSelector != nil
}

// initializeProviders sets up AI providers
func (r *Router) initializeProviders(cfg config.AIConfig) error {
	// Allow empty provider configs for testing
	// The router will still be valid but GenerateResponse will fail if no providers exist
	if len(cfg.Providers) == 0 {
		log.Printf("[Router] No providers configured - router will be empty (testing mode)")
		return nil
	}

	// Initialize providers
	for _, providerCfg := range cfg.Providers {
		var provider Provider
		var err error

		// conduit-3dru: provider-level prompt_caching overrides the ai-level
		// block; unset providers inherit it.
		if providerCfg.PromptCaching == nil && cfg.PromptCaching != (config.PromptCachingConfig{}) {
			providerCfg.PromptCaching = &cfg.PromptCaching
		}

		switch providerCfg.Type {
		case "anthropic":
			provider, err = NewAnthropicProvider(providerCfg)
		case "openai":
			provider, err = NewOpenAIProvider(providerCfg)
		case "ollama":
			// Ollama is OpenAI-compatible with sensible defaults
			if providerCfg.BaseURL == "" {
				providerCfg.BaseURL = "http://localhost:11434/v1"
			}
			provider, err = NewOpenAIProvider(providerCfg)
		case "claude-code":
			// Session mapper is nil during init; set later via SetSessionMapper.
			provider, err = NewClaudeCodeProvider(providerCfg, nil)
		default:
			return fmt.Errorf("unsupported provider type: %s", providerCfg.Type)
		}

		if err != nil {
			return fmt.Errorf("failed to create provider %s: %w", providerCfg.Name, err)
		}

		r.providers[providerCfg.Name] = provider
		r.providerMeta[providerCfg.Name] = ProviderMeta{
			Name:          providerCfg.Name,
			Type:          providerCfg.Type,
			DefaultModel:  providerCfg.Model,
			ContextWindow: providerCfg.ContextWindow,
			FallbackModel: providerCfg.FallbackModel, // bd-6tb
		}
	}

	return nil
}

// RegisterProvider adds a provider to the router (useful for testing with mocks)
func (r *Router) RegisterProvider(name string, provider Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = provider
}

// HasProviders returns true if the router has at least one provider configured
func (r *Router) HasProviders() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers) > 0
}

// ListProviders returns metadata for all configured providers.
func (r *Router) ListProviders() []ProviderMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProviderMeta, 0, len(r.providerMeta))
	for _, meta := range r.providerMeta {
		result = append(result, meta)
	}
	return result
}

// GetProviderMeta returns metadata for a provider by name.
func (r *Router) GetProviderMeta(name string) (ProviderMeta, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meta, ok := r.providerMeta[name]
	return meta, ok
}

// DefaultProviderName returns the name of the default provider.
func (r *Router) DefaultProviderName() string {
	return r.default_
}

// DefaultModel returns the configured model of the default provider, or ""
// when none is configured (ContextWindowForModel("") then yields
// DefaultContextWindow). conduit-31jg.17: the one place a "which model is
// this turn on" default comes from, replacing hardcoded
// "claude-sonnet-4-20250514" literals in the gateway.
func (r *Router) DefaultModel() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providerMeta[r.default_].DefaultModel
}

// contextWindowForProvider returns the configured context window for a provider,
// or 0 if no override is set (meaning auto-detect from model name).
func (r *Router) contextWindowForProvider(providerName string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.providerMeta[providerName]; ok {
		return meta.ContextWindow
	}
	return 0
}

// getProvider returns the named provider under a read lock.
func (r *Router) getProvider(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// GetProvider returns the named provider. This is the exported variant of
// getProvider, used by the gateway to access providers after initialization
// (e.g. to wire a session mapper into the claude-code provider).
func (r *Router) GetProvider(name string) (Provider, bool) {
	return r.getProvider(name)
}

// providerMetaKeys returns the keys of the providerMeta map (for debugging).
// Caller must hold r.mu.
func (r *Router) providerMetaKeys() []string {
	keys := make([]string, 0, len(r.providerMeta))
	for k := range r.providerMeta {
		keys = append(keys, k)
	}
	return keys
}

// ResolveProviderForModel attempts to find the best provider for a given model name.
// Returns the provider name, or "" if no match is found (caller should use default).
func (r *Router) ResolveProviderForModel(model string) string {
	if model == "" {
		return ""
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(model)

	// Tier 0: explicit provider prefix — "provider/model" format
	// If the model contains a slash, check if the prefix matches a known provider name.
	if idx := strings.Index(lower, "/"); idx > 0 {
		prefix := lower[:idx]
		if _, exists := r.providerMeta[prefix]; exists {
			return prefix
		}
		log.Printf("[Router] ResolveProvider: prefix %q NOT in providerMeta (keys: %v) for model %q", prefix, r.providerMetaKeys(), model)
	}

	// Tier 1: prefix heuristics → provider type
	var targetType string
	switch {
	case strings.HasPrefix(lower, "claude-"):
		targetType = "anthropic"
	case strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1-") || strings.HasPrefix(lower, "o3-"):
		targetType = "openai"
	case strings.HasPrefix(lower, "llama") || strings.HasPrefix(lower, "mistral") ||
		strings.HasPrefix(lower, "mixtral") || strings.HasPrefix(lower, "codellama") ||
		strings.HasPrefix(lower, "deepseek") || strings.HasPrefix(lower, "qwen") ||
		strings.HasPrefix(lower, "phi-") || strings.HasPrefix(lower, "gemma"):
		targetType = "ollama"
	}

	if targetType != "" {
		for name, meta := range r.providerMeta {
			if meta.Type == targetType {
				return name
			}
		}
	}

	// Tier 2: check if model matches any provider's default model
	for name, meta := range r.providerMeta {
		if meta.DefaultModel == model {
			return name
		}
	}

	// Tier 3: model string is itself a known provider name
	if _, exists := r.providerMeta[lower]; exists {
		return lower
	}

	return ""
}

// GenerateResponse generates an AI response for a session
// resolveFallbackRoute returns (fallbackModel, provider to retry on).
// bd-27ud: the fallback model must be sent to ITS OWN provider, not the
// failed one — "z-ai/glm-5.3" retried on Anthropic previously 404'd
// (journal 15:33:21Z Sep 1 2026; 5 sub-agent silent deaths).
// ok=false when the fallback model resolves to no known provider; callers
// must then surface the original error instead of blind-retrying.
func (r *Router) resolveFallbackRoute(failedProvider string) (string, Provider, bool) {
	meta, metaOk := r.GetProviderMeta(failedProvider)
	fallbackModel := "z-ai/glm-5.3" // default fallback (bd-6tb)
	if metaOk && meta.FallbackModel != "" {
		fallbackModel = meta.FallbackModel
	}

	fallbackProviderName := r.ResolveProviderForModel(fallbackModel)
	if fallbackProviderName == "" {
		return "", nil, false
	}
	p, exists := r.GetProvider(fallbackProviderName)
	if !exists {
		return "", nil, false
	}
	return fallbackModel, p, true
}

// ResolveEmptyFailover implements ai.EmptyFailoverRouter (conduit-1z0g):
// resolve the provider's configured fallback_model for the empty guard's
// cross-model attempt. Reuses the bd-6tb FallbackModel plumbing.
//
// conduit-15gt: this is a PURE RESOLVER. Same-provider routes are allowed
// here (z-ai → z-ai/glm-5.3 when glm-5.3-flash fails is meaningful failover
// on a different inference path). The guard layer owns the same-model
// refusal — only it knows which model actually failed (req.Model); the
// provider's DefaultModel is NOT the failed model (sessions override it
// per-request), so any router-level same-model check would misfire.
func (r *Router) ResolveEmptyFailover(failedProvider string) (string, Provider, bool) {
	// Unknown failed provider: the quota path defaults to the bd-6tb fallback,
	// but the empty guard only ever asks about a provider that just failed —
	// an unknown name means misconfiguration, so refuse rather than guess.
	if _, metaOk := r.GetProviderMeta(failedProvider); !metaOk {
		log.Printf("[Router] Empty-failover asked about unknown provider %q — refusing (conduit-1z0g)", failedProvider)
		return "", nil, false
	}
	model, p, ok := r.resolveFallbackRoute(failedProvider)
	if !ok {
		return "", nil, false
	}
	// conduit-31jg.18(b): the failover call carries the full tool-loop
	// history too — trim it to the failover route's window.
	return model, r.failoverGuardedProvider(providerRoute{name: p.Name(), provider: p, model: model}), true // conduit-31jg.68: no chained fallback
}

func (r *Router) GenerateResponse(ctx context.Context, session *sessions.Session, userMessage string, providerName string) (*GenerateResponse, error) {
	// Use default provider if none specified
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.getProvider(providerName)
	if !exists {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	log.Printf("[Router] Generate: provider=%q", providerName)

	// Build system prompt using agent system
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("failed to build system prompt: %w", err)
		}
		systemBlocks = blocks
	}

	// Build chat messages from session history with agent system prompt
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build chat messages: %w", err)
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions(session)
	}

	req := &GenerateRequest{
		Messages:  messages,
		Tools:     tools,
		MaxTokens: r.chainMaxTokens(),
	}
	// conduit-31jg.18: (provider, model) travel together through the
	// quota-fallback and timeout retries; each attempt is trimmed to its
	// route's window (callWithRecovery).
	response, served, _, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: provider, model: req.Model},
		req, recoveryOpts{phase: "generate"})
	// conduit-31jg.64: every provider call (each recovery attempt here, and
	// every later call through the guarded provider) is recorded to the
	// usage tracker exactly once by the metering hook (route_call.go).
	if err != nil {
		return nil, err
	}
	providerName = served.name
	provider = r.guardedProvider(served) // conduit-31jg.18(b): later calls re-trim

	// conduit-31jg.11: length-truncated reply → auto-continue (bd-1k3o parity).
	response = ContinueLengthTruncated(ctx, provider, req, response, "generate")

	// Process response through agent system
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			return nil, fmt.Errorf("failed to process response: %w", err)
		}

		// Update response based on agent processing
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = "" // Mark as silent
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	return response, nil
}

// ProgressCallback is called during long operations to provide status updates
type ProgressCallback func(status string)

// GenerateResponseWithTools generates an AI response with tool execution support
// modelOverride can be empty to use the default, or a specific model name/alias
func (r *Router) GenerateResponseWithTools(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string) (ConversationResponse, error) {
	return r.GenerateResponseWithToolsAndProgress(ctx, session, userMessage, providerName, modelOverride, nil)
}

// GenerateResponseWithToolsAndProgress is like GenerateResponseWithTools but with progress callbacks.
// Acquires the per-session turn lock for the duration of the chain.
func (r *Router) GenerateResponseWithToolsAndProgress(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onProgress ProgressCallback) (ConversationResponse, error) {
	unlock := r.lockSessionCtx(ctx, sessionKeyOf(session)) // conduit-31jg.35: no-op under an AcquireTurn lease
	defer unlock()
	return r.generateResponseWithToolsLocked(ctx, session, userMessage, providerName, modelOverride, onProgress)
}

// generateResponseWithToolsLocked is the turn-lock-holding variant. Callers must already
// hold the per-session turn lock (e.g., via lockSession). This avoids double-locking when
// GenerateResponseStreaming falls back to the non-streaming path.
func (r *Router) generateResponseWithToolsLocked(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onProgress ProgressCallback) (ConversationResponse, error) {
	chainStart := time.Now()
	log.Printf("[Router] >>> LLM CHAIN START")
	var chainErr error
	defer func() {
		if chainErr != nil {
			log.Printf("[Router] <<< LLM CHAIN END (%s) ERROR", time.Since(chainStart))
		} else {
			log.Printf("[Router] <<< LLM CHAIN END (%s)", time.Since(chainStart))
		}
	}()

	// Handle bare provider name used as model (e.g., model="ghost" where "ghost" is a provider)
	if modelOverride != "" && !strings.Contains(modelOverride, "/") {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" && strings.EqualFold(resolved, modelOverride) {
			log.Printf("[Router] Bare provider name %q used as model — routing to provider with its default model", modelOverride)
			providerName = resolved
			modelOverride = ""
		}
	}

	// Resolve provider from model only when:
	// 1. No provider explicitly specified, OR
	// 2. Model has explicit provider prefix (e.g., "ghost/model")
	if modelOverride != "" && (providerName == "" || strings.Contains(modelOverride, "/")) {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" {
			if providerName != "" && providerName != resolved {
				log.Printf("[Router] WithTools: overriding provider %q → %q (from model %q)", providerName, resolved, modelOverride)
			}
			providerName = resolved
		}
	}
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.getProvider(providerName)
	if !exists {
		chainErr = fmt.Errorf("provider not found: %s", providerName)
		return nil, chainErr
	}

	contextWindow := r.contextWindowForProvider(providerName)
	log.Printf("[Router] WithTools: provider=%q model=%q context_window=%d", providerName, modelOverride, contextWindow)

	// Build system prompt using agent system
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			chainErr = fmt.Errorf("failed to build system prompt: %w", err)
			return nil, chainErr
		}
		systemBlocks = blocks
	}

	// Build chat messages from session history with agent system prompt
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		chainErr = fmt.Errorf("failed to build chat messages: %w", err)
		return nil, chainErr
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions(session)
	}

	req := &GenerateRequest{
		Messages:  messages,
		Model:     modelOverride,
		Tools:     tools,
		MaxTokens: r.chainMaxTokens(),
	}
	// Get initial AI response. conduit-31jg.18: quota fallback and the
	// timeout retry carry (provider, model) as a pair — a timed-out fallback
	// call is retried on the FALLBACK provider, never the original.
	response, served, latencyMs, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: provider, model: req.Model},
		req, recoveryOpts{phase: "tool loop", quotaFallbackNeedsModel: true})
	if err != nil {
		// conduit-31jg.64: failed attempts were recorded by the metering hook.
		chainErr = fmt.Errorf("AI provider error: %w", err)
		return nil, chainErr
	}
	// The tool-loop continuation (HandleToolCallFlow) must stay on the
	// provider that served the response (bd-27ud: anthropic 404
	// not_found_error, 2026-09-04 sub-agent death), and every later round
	// is re-trimmed to that route's window (conduit-31jg.18(b)).
	providerName = served.name
	provider = r.guardedProvider(served)
	// conduit-31jg.64: usage for this and every later call of the turn is
	// recorded per call by the metering hook, not here.

	// conduit-18vj: a raw-empty response (no content AND no tool calls) must
	// never complete a turn silently — retry once, then deliver a visible
	// fallback so every turn ends with SOMETHING.
	response, err = GuardEmptyResponse(ctx, provider, req, response, err, "initial")

	// conduit-31jg.11: honor FinishReason on the first round trip too — the
	// bd-1k3o length guard previously only ran after tool execution.
	response = ContinueLengthTruncated(ctx, provider, req, response, "initial")

	// conduit-1z6d: per-round-trip instrumentation — dead turns diagnosable
	// from the journal alone.
	log.Printf("[RoundTrip] phase=initial model=%q duration=%dms prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d",
		req.Model, latencyMs,
		response.Usage.PromptTokens, response.Usage.CompletionTokens,
		len(response.Content), len(response.ToolCalls))

	// Process response through agent system
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			chainErr = fmt.Errorf("failed to process response: %w", err)
			return nil, chainErr
		}

		// Update response based on agent processing
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = "" // Mark as silent
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	// Handle tool calls if present and execution engine is available
	if len(response.ToolCalls) > 0 && r.executionEngine != nil {
		// Send conversational progress for significant operations
		if onProgress != nil {
			msg := r.getConversationalProgress(response.ToolCalls)
			if msg != "" {
				onProgress(msg)
			}
		}
		convResponse, err := r.executionEngine.HandleToolCallFlow(ctx, provider, req, response)
		if err != nil {
			chainErr = err
			return nil, chainErr
		}
		// Post-process for silent response patterns (HEARTBEAT_OK, NO_REPLY)
		// This applies the same logic as ProcessResponse but after tool execution
		final := r.processSilentPatterns(convResponse)
		r.recordUsageToStore(session, final)
		return final, nil
	}

	// No tools called or no execution engine - return simple response
	simple := &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}
	r.recordUsageToStore(session, simple)
	return simple, nil
}

// recordUsageToStore persists token usage from a completed generation to the
// session store (bd-27hs). Called inside the per-session turn lock, after the
// chain has succeeded, so every path that generates a tool-using response —
// channel, direct, cron, wake, sub-agent, WS, HTTP — records usage uniformly
// without per-path glue. Best-effort: failures are logged, never fatal, and a
// nil store/session/usage is a no-op.
func (r *Router) recordUsageToStore(session *sessions.Session, response ConversationResponse) {
	if r.sessionStore == nil || session == nil || response == nil {
		return
	}
	usage := response.GetUsage()
	if usage == nil {
		return
	}
	// conduit-31jg.15: usage is the whole-turn sum; the last_* context keys
	// get the last round trip's prompt size (Context()), and the cache token
	// fields are recorded too.
	if err := r.sessionStore.RecordTurnUsage(session.Key, sessions.TurnUsage{
		ContextTokens:            usage.Context(),
		PromptTokens:             usage.PromptTokens,
		CompletionTokens:         usage.CompletionTokens,
		TotalTokens:              usage.TotalTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}); err != nil {
		log.Printf("[Router] token usage recording failed for session %s: %v", session.Key, err)
	}
}

// getConversationalProgress returns a friendly status message for tool calls
// Returns empty string for routine/quick operations to avoid spamming
func (r *Router) getConversationalProgress(toolCalls []ToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}

	// Check for significant operations worth mentioning
	for _, tc := range toolCalls {
		switch tc.Name {
		case "SessionsSpawn":
			return "Spinning up a sub-agent to help with this..."
		case "Bash":
			return "Running that command..."
		case "WebSearch":
			return "Searching the web..."
		case "WebFetch":
			return "Fetching that page..."
		case "MemorySearch":
			return "Checking my memory..."
		}
	}

	// For multiple tool calls, give a general update
	if len(toolCalls) > 2 {
		return "Working on a few things..."
	}

	// Skip progress for simple/quick operations like Read, Write, Glob
	return ""
}

// GenerateResponseStreaming generates a streaming AI response.
// The onDelta callback is called with each text delta, and done=true when complete.
// Any provider implementing StreamingProvider will stream; others fall back to non-streaming.
// Acquires the per-session turn lock for the duration of the chain.
func (r *Router) GenerateResponseStreaming(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onDelta StreamCallback) (ConversationResponse, error) {
	unlock := r.lockSessionCtx(ctx, sessionKeyOf(session)) // conduit-31jg.35: no-op under an AcquireTurn lease
	defer unlock()

	chainStart := time.Now()
	log.Printf("[Router] >>> LLM CHAIN START (streaming)")
	var chainErr error
	defer func() {
		if chainErr != nil {
			log.Printf("[Router] <<< LLM CHAIN END (%s) ERROR", time.Since(chainStart))
		} else {
			log.Printf("[Router] <<< LLM CHAIN END (%s)", time.Since(chainStart))
		}
	}()

	// Handle bare provider name used as model (e.g., model="ghost" where "ghost" is a provider)
	if modelOverride != "" && !strings.Contains(modelOverride, "/") {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" && strings.EqualFold(resolved, modelOverride) {
			log.Printf("[Router] Bare provider name %q used as model — routing to provider with its default model", modelOverride)
			providerName = resolved
			modelOverride = ""
		}
	}

	// Resolve provider from model only when:
	// 1. No provider explicitly specified, OR
	// 2. Model has explicit provider prefix (e.g., "ghost/model")
	if modelOverride != "" && (providerName == "" || strings.Contains(modelOverride, "/")) {
		resolved := r.ResolveProviderForModel(modelOverride)
		if resolved != "" {
			if providerName != "" && providerName != resolved {
				log.Printf("[Router] Streaming: overriding provider %q → %q (from model %q)", providerName, resolved, modelOverride)
			}
			providerName = resolved
		}
	}
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.getProvider(providerName)
	if !exists {
		chainErr = fmt.Errorf("provider not found: %s", providerName)
		return nil, chainErr
	}

	contextWindow := r.contextWindowForProvider(providerName)
	log.Printf("[Router] Streaming: provider=%q model=%q context_window=%d", providerName, modelOverride, contextWindow)

	// Check if the provider supports streaming
	streamingProvider, canStream := provider.(StreamingProvider)
	if !canStream {
		// Fall back to non-streaming. We already hold the per-session turn lock,
		// so dispatch to the unlocked worker to avoid deadlock.
		return r.generateResponseWithToolsLocked(ctx, session, userMessage, providerName, modelOverride, nil)
	}

	// Build system prompt
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			chainErr = fmt.Errorf("failed to build system prompt: %w", err)
			return nil, chainErr
		}
		systemBlocks = blocks
	}

	// Build chat messages
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		chainErr = fmt.Errorf("failed to build messages: %w", err)
		return nil, chainErr
	}

	// Get tools
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions(session)
	}

	req := &GenerateRequest{
		Messages:  messages,
		Model:     modelOverride,
		Tools:     tools,
		MaxTokens: r.chainMaxTokens(),
	}
	// Call streaming API via the provider-agnostic interface.
	// conduit-31jg.18: recovery attempts carry (provider, model) as a pair,
	// and the stream tracker mutes retry deltas once text has reached the
	// client, so a replayed generation never duplicates streamed text.
	response, served, _, err := r.callWithRecovery(ctx,
		providerRoute{name: providerName, provider: streamingProvider, model: req.Model},
		req, recoveryOpts{phase: "streaming", quotaFallbackNeedsModel: true, stream: newStreamTracker(onDelta)})
	if err != nil {
		// conduit-31jg.64: each failed attempt was recorded by the metering
		// hook (conduit-31jg.12 streaming parity preserved there).
		chainErr = err
		return nil, chainErr
	}
	// Keep the tool-loop continuation on the provider that actually served
	// the response (bd-27ud), re-trimming every later round (conduit-31jg.18(b)).
	providerName = served.name
	provider = r.guardedProvider(served)

	// conduit-14qr: the streaming path never went through the empty guard —
	// only the non-streaming chain call sites wrap GuardEmptyResponse — so
	// raw-empty provider responses (z.ai HTTP-200 empty payloads, 2026-09-14
	// RCA) flowed straight to delivery and died as silent WARN suppressions
	// (5 dead deliveries on Sep 14 alone). Run the same retry → cross-model
	// failover → visible fallback machinery as the non-streaming path
	// (conduit-18vj/1z0g). No-op for non-empty responses, errors, tool-call
	// responses, and deliberate silence (NO_REPLY/HEARTBEAT_OK arrive here
	// non-empty and are blanked later by silent-pattern processing).
	// NOTE: deltas already streamed to the client before an empty FINAL are
	// unrecoverable — a recovered retry/failover renders as a fresh message.
	response, err = GuardEmptyResponse(ctx, provider, req, response, err, "streaming")
	if err != nil {
		chainErr = err
		return nil, chainErr
	}

	// conduit-31jg.11: length-truncated first reply → auto-continue
	// (non-streaming continuation; the gateway replaces the streamed text
	// with the final content).
	response = ContinueLengthTruncated(ctx, provider, req, response, "streaming")

	// Process response through agent system (same as non-streaming path)
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			chainErr = fmt.Errorf("failed to process streaming response: %w", err)
			return nil, chainErr
		}
		if processed.Modified {
			response.Content = processed.Content
		}
		if processed.Silent {
			response.Content = ""
		}
		if len(processed.ToolCalls) > 0 {
			response.ToolCalls = processed.ToolCalls
		}
	}

	// Check if tool calls were detected during streaming
	if len(response.ToolCalls) > 0 && r.executionEngine != nil {
		convResponse, err := r.executionEngine.HandleToolCallFlow(ctx, provider, req, response)
		if err != nil {
			chainErr = err
			return nil, chainErr
		}
		// Post-process for silent response patterns (HEARTBEAT_OK, NO_REPLY)
		final := r.processSilentPatterns(convResponse)
		r.recordUsageToStore(session, final)
		return final, nil
	}

	// No tool calls - return simple streaming response
	simple := &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}
	r.recordUsageToStore(session, simple)
	return simple, nil
}

// SimpleConversationResponse implements ConversationResponse for non-tool responses
type SimpleConversationResponse struct {
	Content string `json:"content"`
	Usage   *Usage `json:"usage"`
	Steps   int    `json:"steps"`
}

func (s *SimpleConversationResponse) GetContent() string {
	return s.Content
}

func (s *SimpleConversationResponse) GetUsage() *Usage {
	return s.Usage
}

func (s *SimpleConversationResponse) GetSteps() int {
	return s.Steps
}

func (s *SimpleConversationResponse) HasToolResults() bool {
	return false
}

// processSilentPatterns checks for HEARTBEAT_OK/NO_REPLY patterns in the response
// and returns an empty-content response if detected. Exact match after trimming,
// or contains-match only for short responses (≤40 chars) to tolerate minor LLM
// wrapping. Long responses that merely reference the token are not suppressed.
func (r *Router) processSilentPatterns(response ConversationResponse) ConversationResponse {
	upper := strings.ToUpper(strings.TrimSpace(response.GetContent()))

	silent := upper == constants.SilentReplyToken || upper == constants.HeartbeatOKToken
	if !silent && len(upper) <= 40 {
		silent = strings.Contains(upper, constants.SilentReplyToken) || strings.Contains(upper, constants.HeartbeatOKToken)
	}

	if silent {
		log.Printf("[Router] Silent response pattern detected (suppressing)")
		return &SimpleConversationResponse{
			Content: "",
			Usage:   response.GetUsage(),
			Steps:   response.GetSteps(),
		}
	}

	// Return original response if no silent patterns detected
	return response
}

// contextKey is a private type for context keys in the ai package.
type contextKey string

const attachmentsContextKey contextKey = "ai_attachments"

// WithAttachments stores attachments in the context for the current request.
func WithAttachments(ctx context.Context, attachments []Attachment) context.Context {
	return context.WithValue(ctx, attachmentsContextKey, attachments)
}

// AttachmentsFromContext retrieves attachments from the context, or nil if none.
func AttachmentsFromContext(ctx context.Context) []Attachment {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(attachmentsContextKey).([]Attachment); ok {
		return v
	}
	return nil
}
