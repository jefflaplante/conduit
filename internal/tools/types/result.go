package types

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
