package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
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
	ContextWindow int // Configured context window override (0 = auto-detect from model)
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

	// Smart routing components
	modelSelector      ModelSelector
	complexityAnalyzer *ComplexityAnalyzer
	smartRoutingCfg    *config.SmartRoutingConfig
	contextEngine      ContextEngine
	pricingResolver    *PricingResolver
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
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role        string       `json:"role"` // "system", "user", "assistant", "tool"
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`   // For assistant messages with tool calls
	ToolCallID  string       `json:"tool_call_id,omitempty"` // For tool result messages
	Attachments []Attachment `json:"attachments,omitempty"`  // In-memory media attachments (images, etc.)
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
// It tries an exact match first, then prefix matching, then returns the default.
func ContextWindowForModel(model string) int {
	if model == "" {
		return DefaultContextWindow
	}
	// Exact match
	if size, ok := ContextWindowSizes[model]; ok {
		return size
	}
	// Prefix match (handles date-suffixed models like claude-sonnet-4-20250514)
	for prefix, size := range ContextWindowSizes {
		if strings.HasPrefix(model, prefix) {
			return size
		}
	}
	return DefaultContextWindow
}

// NewRouter creates a new AI router
func NewRouter(cfg config.AIConfig, agentSystem AgentSystem) (*Router, error) {
	router := &Router{
		providers:    make(map[string]Provider),
		providerMeta: make(map[string]ProviderMeta),
		default_:     cfg.DefaultProvider,
		agentSystem:  agentSystem,
		usageTracker: NewUsageTracker(),
	}

	return router, router.initializeProviders(cfg)
}

// NewRouterWithExecution creates a new AI router with tool execution
func NewRouterWithExecution(cfg config.AIConfig, agentSystem AgentSystem, executionEngine ExecutionEngine) (*Router, error) {
	router := &Router{
		providers:       make(map[string]Provider),
		providerMeta:    make(map[string]ProviderMeta),
		default_:        cfg.DefaultProvider,
		agentSystem:     agentSystem,
		executionEngine: executionEngine,
		usageTracker:    NewUsageTracker(),
	}

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

// SetPricingResolver sets the pricing resolver for dynamic model pricing.
func (r *Router) SetPricingResolver(pr *PricingResolver) {
	r.pricingResolver = pr
}

// ResolvePricing returns pricing for a model using the configured resolver,
// or falls back to the default pricing matrix if no resolver is set.
func (r *Router) ResolvePricing(model string) ModelPricing {
	if r.pricingResolver != nil {
		return r.pricingResolver.PricingForModel(model)
	}
	return PricingForModel(model)
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
		MaxTokens: 4000,
	}
	trimRequestToFitContext(req, r.contextWindowForProvider(providerName))

	start := time.Now()
	response, err := provider.GenerateResponse(ctx, req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		if r.usageTracker != nil {
			r.usageTracker.RecordError(providerName, req.Model)
		}
		return nil, err
	}
	if r.usageTracker != nil {
		r.usageTracker.RecordUsage(providerName, req.Model, response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.CacheCreationInputTokens, response.Usage.CacheReadInputTokens, latencyMs)
	}

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

// GenerateResponseWithToolsAndProgress is like GenerateResponseWithTools but with progress callbacks
func (r *Router) GenerateResponseWithToolsAndProgress(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onProgress ProgressCallback) (ConversationResponse, error) {
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
		MaxTokens: 4000,
	}
	trimRequestToFitContext(req, r.contextWindowForProvider(providerName))

	// Get initial AI response
	start := time.Now()
	response, err := provider.GenerateResponse(ctx, req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		if r.usageTracker != nil {
			r.usageTracker.RecordError(providerName, modelOverride)
		}
		chainErr = fmt.Errorf("AI provider error: %w", err)
		return nil, chainErr
	}
	if r.usageTracker != nil {
		r.usageTracker.RecordUsage(providerName, modelOverride, response.Usage.PromptTokens, response.Usage.CompletionTokens, response.Usage.CacheCreationInputTokens, response.Usage.CacheReadInputTokens, latencyMs)
	}

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
		return r.processSilentPatterns(convResponse), nil
	}

	// No tools called or no execution engine - return simple response
	return &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}, nil
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
func (r *Router) GenerateResponseStreaming(ctx context.Context, session *sessions.Session, userMessage string, providerName string, modelOverride string, onDelta StreamCallback) (ConversationResponse, error) {
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
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	contextWindow := r.contextWindowForProvider(providerName)
	log.Printf("[Router] Streaming: provider=%q model=%q context_window=%d", providerName, modelOverride, contextWindow)

	// Check if the provider supports streaming
	streamingProvider, canStream := provider.(StreamingProvider)
	if !canStream {
		// Fall back to non-streaming
		return r.GenerateResponseWithTools(ctx, session, userMessage, providerName, modelOverride)
	}

	// Build system prompt
	var systemBlocks []SystemBlock
	if r.agentSystem != nil {
		blocks, err := r.agentSystem.BuildSystemPrompt(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("failed to build system prompt: %w", err)
		}
		systemBlocks = blocks
	}

	// Build chat messages
	messages, err := r.buildChatMessagesWithSystemPrompt(ctx, session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build messages: %w", err)
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
		MaxTokens: 4000,
	}
	trimRequestToFitContext(req, contextWindow)

	// Call streaming API via the provider-agnostic interface
	response, err := streamingProvider.GenerateResponseStreaming(ctx, req, onDelta)
	if err != nil {
		return nil, err
	}

	// Process response through agent system (same as non-streaming path)
	if r.agentSystem != nil {
		processed, err := r.agentSystem.ProcessResponse(ctx, response)
		if err != nil {
			return nil, fmt.Errorf("failed to process streaming response: %w", err)
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
			return nil, err
		}
		// Post-process for silent response patterns (HEARTBEAT_OK, NO_REPLY)
		return r.processSilentPatterns(convResponse), nil
	}

	// No tool calls - return simple streaming response
	return &SimpleConversationResponse{
		Content: response.Content,
		Usage:   &response.Usage,
		Steps:   1,
	}, nil
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
