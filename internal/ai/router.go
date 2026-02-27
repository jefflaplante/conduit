package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

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

// Router handles AI model interactions
type Router struct {
	providers       map[string]Provider
	default_        string
	agentSystem     AgentSystem     // Add agent system to router
	executionEngine ExecutionEngine // Tool execution engine (interface, not pointer)
	sessionStore    *sessions.Store // Session store for retrieving message history
	usageTracker    *UsageTracker

	// Smart routing components
	modelSelector      ModelSelector
	complexityAnalyzer *ComplexityAnalyzer
	smartRoutingCfg    *config.SmartRoutingConfig
	contextEngine      ContextEngine
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
	GetToolDefinitions() []Tool
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
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role       string     `json:"role"` // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // For assistant messages with tool calls
	ToolCallID string     `json:"tool_call_id,omitempty"` // For tool result messages
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
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// DefaultContextWindow is the fallback context window size in tokens.
const DefaultContextWindow = 200000

// ContextWindowSizes maps model ID prefixes to their context window sizes.
var ContextWindowSizes = map[string]int{
	"claude-opus-4-6":   200000,
	"claude-opus-4-5":   200000, // legacy
	"claude-sonnet-4":   200000,
	"claude-haiku-4-5":  200000,
	"claude-3-5-sonnet": 200000,
	"claude-3-5-haiku":  200000,
	"claude-3-opus":     200000,
	"claude-3-sonnet":   200000,
	"claude-3-haiku":    200000,
	"gpt-4o":            128000,
	"gpt-4-turbo":       128000,
	"gpt-4":             8192,
	"gpt-3.5-turbo":     16385,
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
		default:
			return fmt.Errorf("unsupported provider type: %s", providerCfg.Type)
		}

		if err != nil {
			return fmt.Errorf("failed to create provider %s: %w", providerCfg.Name, err)
		}

		r.providers[providerCfg.Name] = provider
	}

	return nil
}

// RegisterProvider adds a provider to the router (useful for testing with mocks)
func (r *Router) RegisterProvider(name string, provider Provider) {
	r.providers[name] = provider
}

// HasProviders returns true if the router has at least one provider configured
func (r *Router) HasProviders() bool {
	return len(r.providers) > 0
}

// GenerateResponse generates an AI response for a session
func (r *Router) GenerateResponse(ctx context.Context, session *sessions.Session, userMessage string, providerName string) (*GenerateResponse, error) {
	// Use default provider if none specified
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.providers[providerName]
	if !exists {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

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
	messages, err := r.buildChatMessagesWithSystemPrompt(session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build chat messages: %w", err)
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions()
	}

	req := &GenerateRequest{
		Messages:  messages,
		Tools:     tools,
		MaxTokens: 4000,
	}

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
		r.usageTracker.RecordUsage(providerName, req.Model, response.Usage.PromptTokens, response.Usage.CompletionTokens, latencyMs)
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
	// Use default provider if none specified
	if providerName == "" {
		providerName = r.default_
	}

	provider, exists := r.providers[providerName]
	if !exists {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

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
	messages, err := r.buildChatMessagesWithSystemPrompt(session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build chat messages: %w", err)
	}

	// Include tool definitions from agent system
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions()
	}

	req := &GenerateRequest{
		Messages:  messages,
		Model:     modelOverride,
		Tools:     tools,
		MaxTokens: 4000,
	}

	// Get initial AI response
	start := time.Now()
	response, err := provider.GenerateResponse(ctx, req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		if r.usageTracker != nil {
			r.usageTracker.RecordError(providerName, modelOverride)
		}
		return nil, fmt.Errorf("AI provider error: %w", err)
	}
	if r.usageTracker != nil {
		r.usageTracker.RecordUsage(providerName, modelOverride, response.Usage.PromptTokens, response.Usage.CompletionTokens, latencyMs)
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
			return nil, err
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

// GenerateResponseStreaming generates a streaming AI response
// The onDelta callback is called with each text delta, and done=true when complete
func (r *Router) GenerateResponseStreaming(ctx context.Context, session *sessions.Session, userMessage string, modelOverride string, onDelta StreamCallback) (ConversationResponse, error) {
	provider, exists := r.providers[r.default_]
	if !exists {
		return nil, fmt.Errorf("default provider not found")
	}

	// Only Anthropic provider supports streaming currently
	anthropicProvider, ok := provider.(*AnthropicProvider)
	if !ok {
		// Fall back to non-streaming
		return r.GenerateResponseWithTools(ctx, session, userMessage, "", modelOverride)
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
	messages, err := r.buildChatMessagesWithSystemPrompt(session, userMessage, systemBlocks)
	if err != nil {
		return nil, fmt.Errorf("failed to build messages: %w", err)
	}

	// Get tools
	var tools []Tool
	if r.agentSystem != nil {
		tools = r.agentSystem.GetToolDefinitions()
	}

	// Build system prompt string
	var systemPrompt string
	for _, block := range systemBlocks {
		if systemPrompt != "" {
			systemPrompt += "\n\n"
		}
		systemPrompt += block.Text
	}

	// Call streaming API
	response, err := anthropicProvider.generateWithStreamOAuth(ctx, messages, tools, systemPrompt, modelOverride, onDelta)
	if err != nil {
		return nil, err
	}

	// Check if tool calls were detected during streaming
	if len(response.ToolCalls) > 0 && r.executionEngine != nil {
		// Tool calls found! Transition to tool execution mode

		// Build the request that would produce this response
		req := &GenerateRequest{
			Messages:  messages,
			Model:     modelOverride,
			Tools:     tools,
			MaxTokens: 4000,
		}

		// Use the execution engine to handle tool calls
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
// and returns an empty-content response if detected. This applies the same logic
// as AgentSystem.ProcessResponse but for responses that come from tool execution.
func (r *Router) processSilentPatterns(response ConversationResponse) ConversationResponse {
	upper := strings.ToUpper(strings.TrimSpace(response.GetContent()))

	// Check for silent response patterns using contains check,
	// because the LLM sometimes wraps tokens in surrounding text.
	if strings.Contains(upper, "HEARTBEAT_OK") || strings.Contains(upper, "NO_REPLY") {
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
