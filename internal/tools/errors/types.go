// Package errors provides structured error types for rich tool error messages.
// This enables AI agents to provide specific, actionable guidance when tools fail.
package errors

import (
	"fmt"
	"strings"
	"time"
)

// ErrorType categorizes different types of tool failures
type ErrorType string

const (
	// Parameter-related errors
	InvalidParameter    ErrorType = "invalid_parameter"
	MissingParameter    ErrorType = "missing_parameter"
	ParameterOutOfRange ErrorType = "parameter_out_of_range"
	InvalidFormat       ErrorType = "invalid_format"

	// Permission and access errors
	PermissionDenied ErrorType = "permission_denied"
	FileNotFound     ErrorType = "file_not_found"
	PathNotAllowed   ErrorType = "path_not_allowed"

	// Service and connectivity errors
	ServiceUnavailable   ErrorType = "service_unavailable"
	ChannelOffline       ErrorType = "channel_offline"
	AuthenticationFailed ErrorType = "authentication_failed"
	RateLimitExceeded    ErrorType = "rate_limit_exceeded"

	// Resource errors
	ResourceNotFound  ErrorType = "resource_not_found"
	ResourceConflict  ErrorType = "resource_conflict"
	InsufficientQuota ErrorType = "insufficient_quota"

	// System errors
	InternalError      ErrorType = "internal_error"
	TimeoutError       ErrorType = "timeout_error"
	ConfigurationError ErrorType = "configuration_error"
)

// ToolError represents a structured error with rich context and suggestions
type ToolError struct {
	Type            ErrorType     `json:"error_type"`
	Message         string        `json:"message"`
	Parameter       string        `json:"parameter,omitempty"`        // Which parameter caused the error
	ProvidedValue   interface{}   `json:"provided_value,omitempty"`   // What value was provided
	AvailableValues []string      `json:"available_values,omitempty"` // Valid alternatives
	Examples        []string      `json:"examples,omitempty"`         // Example valid values
	Suggestions     []string      `json:"suggestions,omitempty"`      // Actionable next steps
	Context         *ErrorContext `json:"context,omitempty"`          // Current system state context
	Timestamp       time.Time     `json:"timestamp"`
}

// ErrorContext provides current system state relevant to the error
type ErrorContext struct {
	ChannelStatus  map[string]string `json:"channel_status,omitempty"`  // Available channels and their status
	AllowedPaths   []string          `json:"allowed_paths,omitempty"`   // Workspace-accessible paths
	CurrentUser    string            `json:"current_user,omitempty"`    // Current user context
	CurrentChannel string            `json:"current_channel,omitempty"` // Current channel context
	SystemLimits   map[string]int64  `json:"system_limits,omitempty"`   // Relevant quotas or limits
	Configuration  map[string]string `json:"configuration,omitempty"`   // Relevant config settings
}

// Error implements the error interface with rich formatting
func (e *ToolError) Error() string {
	var parts []string
	parts = append(parts, e.Message)

	if len(e.AvailableValues) > 0 {
		if len(e.AvailableValues) <= 5 {
			parts = append(parts, fmt.Sprintf("Available: %s", strings.Join(e.AvailableValues, ", ")))
		} else {
			// Show first 5 with "and X more"
			preview := e.AvailableValues[:5]
			remaining := len(e.AvailableValues) - 5
			parts = append(parts, fmt.Sprintf("Available: %s (and %d more)",
				strings.Join(preview, ", "), remaining))
		}
	}

	if len(e.Examples) > 0 {
		if len(e.Examples) <= 3 {
			parts = append(parts, fmt.Sprintf("Examples: %s", strings.Join(e.Examples, ", ")))
		} else {
			examples := e.Examples[:3]
			parts = append(parts, fmt.Sprintf("Examples: %s", strings.Join(examples, ", ")))
		}
	}

	if len(e.Suggestions) > 0 {
		if len(e.Suggestions) == 1 {
			parts = append(parts, fmt.Sprintf("Try: %s", e.Suggestions[0]))
		} else {
			parts = append(parts, fmt.Sprintf("Try: %s", strings.Join(e.Suggestions[:1], ", ")))
		}
	}

	return strings.Join(parts, ". ")
}

// NewToolError creates a new ToolError with the basic required fields
func NewToolError(errorType ErrorType, message string) *ToolError {
	return &ToolError{
		Type:      errorType,
		Message:   message,
		Timestamp: time.Now(),
	}
}

// WithParameter adds parameter-specific information to the error
func (e *ToolError) WithParameter(name string, providedValue interface{}) *ToolError {
	e.Parameter = name
	e.ProvidedValue = providedValue
	return e
}

// WithAvailableValues adds the list of valid values for this parameter
func (e *ToolError) WithAvailableValues(values []string) *ToolError {
	e.AvailableValues = values
	return e
}

// WithExamples adds example valid values
func (e *ToolError) WithExamples(examples []string) *ToolError {
	e.Examples = examples
	return e
}

// WithSuggestions adds actionable suggestions for fixing the error
func (e *ToolError) WithSuggestions(suggestions []string) *ToolError {
	e.Suggestions = suggestions
	return e
}

// WithContext adds system state context to help understand the error
func (e *ToolError) WithContext(context *ErrorContext) *ToolError {
	e.Context = context
	return e
}

// ToMap converts the ToolError to a map for inclusion in ToolResult.Data
func (e *ToolError) ToMap() map[string]interface{} {
	result := map[string]interface{}{
		"error_type": string(e.Type),
		"message":    e.Message,
		"timestamp":  e.Timestamp,
	}

	if e.Parameter != "" {
		result["parameter"] = e.Parameter
	}
	if e.ProvidedValue != nil {
		result["provided_value"] = e.ProvidedValue
	}
	if len(e.AvailableValues) > 0 {
		result["available_values"] = e.AvailableValues
	}
	if len(e.Examples) > 0 {
		result["examples"] = e.Examples
	}
	if len(e.Suggestions) > 0 {
		result["suggestions"] = e.Suggestions
	}
	if e.Context != nil {
		result["context"] = e.Context
	}

	return result
}

// ParameterError creates a parameter-specific error with common patterns
func ParameterError(paramName string, providedValue interface{}, message string) *ToolError {
	return NewToolError(InvalidParameter, message).
		WithParameter(paramName, providedValue)
}

// MissingParameterError creates an error for required parameters that are missing
func MissingParameterError(paramName string, examples []string) *ToolError {
	message := fmt.Sprintf("Parameter '%s' is required", paramName)
	return NewToolError(MissingParameter, message).
		WithParameter(paramName, nil).
		WithExamples(examples)
}

// ServiceUnavailableError creates an error for when a service is not available
func ServiceUnavailableError(serviceName string, availableServices []string) *ToolError {
	message := fmt.Sprintf("Service '%s' is not available", serviceName)
	return NewToolError(ServiceUnavailable, message).
		WithParameter("service", serviceName).
		WithAvailableValues(availableServices)
}

// FilePermissionError creates an error for file access issues
func FilePermissionError(path string, allowedPaths []string, operation string) *ToolError {
	message := fmt.Sprintf("Cannot %s '%s': path not allowed or insufficient permissions", operation, path)
	return NewToolError(PermissionDenied, message).
		WithParameter("path", path).
		WithContext(&ErrorContext{AllowedPaths: allowedPaths}).
		WithSuggestions([]string{
			"Use a path within the workspace directory",
			"Check file permissions",
		})
}
