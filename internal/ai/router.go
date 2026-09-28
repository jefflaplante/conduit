package ai

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"conduit/internal/config"
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

	pricingResolver *PricingResolver // conduit-31jg.57

	throttle *ProviderThrottle // per-provider/model concurrency slots (conduit-38cz)

	callLog atomic.Pointer[CallLog] // persistent per-call JSONL log (conduit-2lzv)

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
