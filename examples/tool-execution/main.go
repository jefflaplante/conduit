package main

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools"
)

// DemoTool implements a simple calculation tool for demonstration
type DemoTool struct {
	name        string
	description string
}

func NewDemoTool() *DemoTool {
	return &DemoTool{
		name:        "calculate",
		description: "Perform basic arithmetic calculations",
	}
}

func (d *DemoTool) Name() string {
	return d.name
}

func (d *DemoTool) Description() string {
	return d.description
}

func (d *DemoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "The arithmetic operation to perform",
				"enum":        []string{"add", "subtract", "multiply", "divide"},
			},
			"a": map[string]interface{}{
				"type":        "number",
				"description": "First number",
			},
			"b": map[string]interface{}{
				"type":        "number",
				"description": "Second number",
			},
		},
		"required": []string{"operation", "a", "b"},
	}
}

func (d *DemoTool) Execute(ctx context.Context, args map[string]interface{}) (*tools.ToolResult, error) {
	operation, ok := args["operation"].(string)
	if !ok {
		return &tools.ToolResult{
			Success: false,
			Error:   "operation must be a string",
		}, nil
	}

	a, ok := args["a"].(float64)
	if !ok {
		return &tools.ToolResult{
			Success: false,
			Error:   "a must be a number",
		}, nil
	}

	b, ok := args["b"].(float64)
	if !ok {
		return &tools.ToolResult{
			Success: false,
			Error:   "b must be a number",
		}, nil
	}

	var result float64
	var err error

	switch operation {
	case "add":
		result = a + b
	case "subtract":
		result = a - b
	case "multiply":
		result = a * b
	case "divide":
		if b == 0 {
			return &tools.ToolResult{
				Success: false,
				Error:   "division by zero",
			}, nil
		}
		result = a / b
	default:
		return &tools.ToolResult{
			Success: false,
			Error:   "unsupported operation: " + operation,
		}, nil
	}

	return &tools.ToolResult{
		Success: true,
		Content: fmt.Sprintf("%.2f %s %.2f = %.2f", a, operation, b, result),
		Data: map[string]interface{}{
			"operation": operation,
			"a":         a,
			"b":         b,
			"result":    result,
		},
	}, err
}

// WeatherTool demonstrates a tool that might call an external service
type WeatherTool struct{}

func NewWeatherTool() *WeatherTool {
	return &WeatherTool{}
}

func (w *WeatherTool) Name() string {
	return "get_weather"
}

func (w *WeatherTool) Description() string {
	return "Get current weather information for a location"
}

func (w *WeatherTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"location": map[string]interface{}{
				"type":        "string",
				"description": "City name or location to get weather for",
			},
		},
		"required": []string{"location"},
	}
}

func (w *WeatherTool) Execute(ctx context.Context, args map[string]interface{}) (*tools.ToolResult, error) {
	location, ok := args["location"].(string)
	if !ok {
		return &tools.ToolResult{
			Success: false,
			Error:   "location must be a string",
		}, nil
	}

	// Simulate API call delay
	time.Sleep(100 * time.Millisecond)

	// Mock weather data (in a real implementation, this would call a weather API)
	mockWeatherData := map[string]interface{}{
		"location":    location,
		"temperature": 72.5,
		"condition":   "partly cloudy",
		"humidity":    65,
		"windSpeed":   8.2,
	}

	content := fmt.Sprintf("Weather in %s: %.1f°F, %s, humidity %d%%, wind %.1f mph",
		location,
		mockWeatherData["temperature"].(float64),
		mockWeatherData["condition"].(string),
		mockWeatherData["humidity"].(int),
		mockWeatherData["windSpeed"].(float64),
	)

	return &tools.ToolResult{
		Success: true,
		Content: content,
		Data:    mockWeatherData,
	}, nil
}

// MockRegistry creates a registry with demo tools
type MockRegistry struct {
	tools        map[string]tools.Tool
	enabledTools map[string]bool
}

func NewMockRegistry() *MockRegistry {
	registry := &MockRegistry{
		tools:        make(map[string]tools.Tool),
		enabledTools: make(map[string]bool),
	}

	// Add demo tools
	calcTool := NewDemoTool()
	weatherTool := NewWeatherTool()

	registry.tools[calcTool.Name()] = calcTool
	registry.tools[weatherTool.Name()] = weatherTool

	registry.enabledTools[calcTool.Name()] = true
	registry.enabledTools[weatherTool.Name()] = true

	return registry
}

func (m *MockRegistry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*tools.ToolResult, error) {
	if !m.enabledTools[name] {
		return &tools.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool '%s' is not enabled", name),
		}, nil
	}

	tool, exists := m.tools[name]
	if !exists {
		return &tools.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool '%s' not found", name),
		}, nil
	}

	return tool.Execute(ctx, args)
}

func (m *MockRegistry) GetAvailableTools() []ai.Tool {
	var availableTools []ai.Tool

	for name, tool := range m.tools {
		if m.enabledTools[name] {
			availableTools = append(availableTools, ai.Tool{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  tool.Parameters(),
			})
		}
	}

	return availableTools
}

func main() {
	fmt.Println("🔧 Conduit Tool Execution Demo")
	fmt.Println("================================")

	// Create mock registry with demo tools
	registry := NewMockRegistry()

	// Create execution engine
	executionEngine := tools.NewExecutionEngine(
		registry,
		3,              // max parallel executions
		30*time.Second, // timeout
		10,             // max tool chains (for demo)
	)

	// Add middleware
	executionEngine.AddMiddleware(tools.NewLoggingMiddleware())
	executionEngine.AddMiddleware(tools.NewMetricsMiddleware())

	// Create execution engine adapter
	engineAdapter := tools.NewExecutionEngineAdapter(executionEngine)

	// Create AI tools from registry
	aiTools := registry.GetAvailableTools()

	// For demo purposes, we'll use a mock setup
	// In practice, you'd configure real AI providers (Anthropic, OpenAI, etc.)
	mockProvider := &MockProvider{}

	fmt.Printf("🤖 Mock AI Provider: %s\n", mockProvider.Name())
	fmt.Printf("🔧 Available Tools: %d\n", len(aiTools))
	for _, t := range aiTools {
		fmt.Printf("   - %s: %s\n", t.Name, t.Description)
	}

	// Demo scenarios
	scenarios := []struct {
		name    string
		message string
	}{
		{
			name:    "Single Tool Call",
			message: "Please calculate 25 + 17 for me",
		},
		{
			name:    "Multiple Tool Calls",
			message: "Get the weather for Seattle and then calculate 10 * 5",
		},
		{
			name:    "Tool Chaining",
			message: "First calculate 100 / 4, then get weather for New York",
		},
	}

	ctx := context.Background()

	for i, scenario := range scenarios {
		fmt.Printf("\n--- Scenario %d: %s ---\n", i+1, scenario.name)
		fmt.Printf("User: %s\n", scenario.message)

		// Simulate the complete tool execution flow
		err := demonstrateToolExecution(ctx, mockProvider, engineAdapter, aiTools, scenario.message)
		if err != nil {
			fmt.Printf("❌ Error: %v\n", err)
		}
	}

	fmt.Println("\n🎯 Demo completed! Tool execution integration is working.")
}

// demonstrateToolExecution shows the complete tool execution flow
func demonstrateToolExecution(ctx context.Context, provider ai.Provider, engine ai.ExecutionEngine, tools []ai.Tool, userMessage string) error {
	// Step 1: Generate initial AI response
	req := &ai.GenerateRequest{
		Messages: []ai.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant with access to tools."},
			{Role: "user", Content: userMessage},
		},
		Tools:     tools,
		MaxTokens: 200,
	}

	fmt.Printf("🧠 AI Processing...\n")
	initialResp, err := provider.GenerateResponse(ctx, req)
	if err != nil {
		return fmt.Errorf("AI response failed: %w", err)
	}

	fmt.Printf("🎯 Initial Response: %s\n", initialResp.Content)

	// Step 2: Check for tool calls
	if len(initialResp.ToolCalls) == 0 {
		fmt.Printf("✅ No tools needed - conversation complete\n")
		return nil
	}

	fmt.Printf("🔧 Tool calls detected: %d\n", len(initialResp.ToolCalls))
	for _, call := range initialResp.ToolCalls {
		fmt.Printf("   - %s: %v\n", call.Name, call.Args)
	}

	// Step 3: Execute tool calls via execution engine
	fmt.Printf("⚡ Executing tools...\n")
	finalResp, err := engine.HandleToolCallFlow(ctx, provider, req, initialResp)
	if err != nil {
		return fmt.Errorf("tool execution failed: %w", err)
	}

	// Step 4: Display final results
	fmt.Printf("✅ Final Response (%d steps): %s\n", finalResp.GetSteps(), finalResp.GetContent())
	fmt.Printf("📊 Has tool results: %v\n", finalResp.HasToolResults())

	if usage := finalResp.GetUsage(); usage != nil {
		fmt.Printf("🔤 Token usage: %d total (%d prompt + %d completion)\n",
			usage.TotalTokens, usage.PromptTokens, usage.CompletionTokens)
	}

	return nil
}

// MockProvider implements a simple mock AI provider for demonstration
type MockProvider struct{}

func (m *MockProvider) Name() string {
	return "mock"
}

func (m *MockProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	// Analyze the user message to determine which tools to call
	message := req.Messages[len(req.Messages)-1].Content

	var toolCalls []ai.ToolCall
	content := "I'll help you with that request. "

	// Simple pattern matching to demonstrate tool calling
	if containsAny(message, []string{"calculate", "add", "subtract", "multiply", "divide", "+", "-", "*", "/"}) {
		content += "Let me do that calculation for you."
		toolCalls = append(toolCalls, ai.ToolCall{
			ID:   "calc_001",
			Name: "calculate",
			Args: map[string]interface{}{
				"operation": extractOperation(message),
				"a":         extractFirstNumber(message),
				"b":         extractSecondNumber(message),
			},
		})
	}

	if containsAny(message, []string{"weather", "temperature", "forecast"}) {
		content += " I'll check the weather for you."
		toolCalls = append(toolCalls, ai.ToolCall{
			ID:   "weather_001",
			Name: "get_weather",
			Args: map[string]interface{}{
				"location": extractLocation(message),
			},
		})
	}

	// If no tool calls, provide a generic response
	if len(toolCalls) == 0 {
		content = "I understand your request. How can I assist you further?"
	}

	return &ai.GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage: ai.Usage{
			PromptTokens:     50,
			CompletionTokens: 25,
			TotalTokens:      75,
		},
	}, nil
}

// Helper functions for the mock provider
func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if len(text) > 0 && len(keyword) > 0 {
			// Simple substring search
			for i := 0; i <= len(text)-len(keyword); i++ {
				if text[i:i+len(keyword)] == keyword {
					return true
				}
			}
		}
	}
	return false
}

func extractOperation(message string) string {
	if containsAny(message, []string{"add", "+"}) {
		return "add"
	}
	if containsAny(message, []string{"subtract", "-"}) {
		return "subtract"
	}
	if containsAny(message, []string{"multiply", "*"}) {
		return "multiply"
	}
	if containsAny(message, []string{"divide", "/"}) {
		return "divide"
	}
	return "add" // default
}

func extractFirstNumber(message string) float64 {
	// Simple extraction - in practice this would be more sophisticated
	numbers := []float64{25, 10, 100, 15, 42}
	return numbers[0] // Return first number for demo
}

func extractSecondNumber(message string) float64 {
	// Simple extraction - in practice this would be more sophisticated
	numbers := []float64{17, 5, 4, 8, 13}
	return numbers[0] // Return first number for demo
}

func extractLocation(message string) string {
	locations := []string{"Seattle", "New York", "San Francisco", "Chicago", "Boston"}
	for _, loc := range locations {
		if containsAny(message, []string{loc}) {
			return loc
		}
	}
	return "Seattle" // default
}
