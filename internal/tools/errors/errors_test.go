package errors

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *ToolError
		contains []string
	}{
		{
			name: "message only",
			err: &ToolError{
				Message: "Something went wrong",
			},
			contains: []string{"Something went wrong"},
		},
		{
			name: "with few available values",
			err: &ToolError{
				Message:         "Invalid channel",
				AvailableValues: []string{"slack", "telegram", "discord"},
			},
			contains: []string{"Invalid channel", "Available: slack, telegram, discord"},
		},
		{
			name: "with many available values (more than 5)",
			err: &ToolError{
				Message:         "Invalid channel",
				AvailableValues: []string{"one", "two", "three", "four", "five", "six", "seven"},
			},
			contains: []string{"Invalid channel", "Available: one, two, three, four, five (and 2 more)"},
		},
		{
			name: "with exactly 5 available values",
			err: &ToolError{
				Message:         "Invalid option",
				AvailableValues: []string{"a", "b", "c", "d", "e"},
			},
			contains: []string{"Available: a, b, c, d, e"},
		},
		{
			name: "with few examples",
			err: &ToolError{
				Message:  "Invalid format",
				Examples: []string{"2024-01-01", "2024-12-31"},
			},
			contains: []string{"Invalid format", "Examples: 2024-01-01, 2024-12-31"},
		},
		{
			name: "with many examples (more than 3)",
			err: &ToolError{
				Message:  "Invalid format",
				Examples: []string{"ex1", "ex2", "ex3", "ex4", "ex5"},
			},
			contains: []string{"Examples: ex1, ex2, ex3"},
		},
		{
			name: "with exactly 3 examples",
			err: &ToolError{
				Message:  "Invalid format",
				Examples: []string{"ex1", "ex2", "ex3"},
			},
			contains: []string{"Examples: ex1, ex2, ex3"},
		},
		{
			name: "with single suggestion",
			err: &ToolError{
				Message:     "Error occurred",
				Suggestions: []string{"Try this instead"},
			},
			contains: []string{"Error occurred", "Try: Try this instead"},
		},
		{
			name: "with multiple suggestions",
			err: &ToolError{
				Message:     "Error occurred",
				Suggestions: []string{"First option", "Second option", "Third option"},
			},
			contains: []string{"Error occurred", "Try: First option"},
		},
		{
			name: "with all fields",
			err: &ToolError{
				Message:         "Parameter 'channel' is invalid",
				AvailableValues: []string{"slack", "telegram"},
				Examples:        []string{"slack", "telegram"},
				Suggestions:     []string{"Use one of the available channels"},
			},
			contains: []string{
				"Parameter 'channel' is invalid",
				"Available: slack, telegram",
				"Examples: slack, telegram",
				"Try: Use one of the available channels",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			for _, s := range tt.contains {
				assert.Contains(t, result, s)
			}
		})
	}
}

func TestNewToolError(t *testing.T) {
	tests := []struct {
		name      string
		errorType ErrorType
		message   string
	}{
		{
			name:      "invalid parameter",
			errorType: InvalidParameter,
			message:   "Invalid parameter value",
		},
		{
			name:      "missing parameter",
			errorType: MissingParameter,
			message:   "Required parameter missing",
		},
		{
			name:      "permission denied",
			errorType: PermissionDenied,
			message:   "Access denied",
		},
		{
			name:      "service unavailable",
			errorType: ServiceUnavailable,
			message:   "Service is down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			err := NewToolError(tt.errorType, tt.message)
			after := time.Now()

			require.NotNil(t, err)
			assert.Equal(t, tt.errorType, err.Type)
			assert.Equal(t, tt.message, err.Message)
			assert.True(t, err.Timestamp.After(before) || err.Timestamp.Equal(before))
			assert.True(t, err.Timestamp.Before(after) || err.Timestamp.Equal(after))
		})
	}
}

func TestToolError_WithParameter(t *testing.T) {
	err := NewToolError(InvalidParameter, "Invalid value")

	result := err.WithParameter("channel", "invalid_channel")

	assert.Equal(t, "channel", result.Parameter)
	assert.Equal(t, "invalid_channel", result.ProvidedValue)
	// Verify fluent interface returns same instance
	assert.Same(t, err, result)
}

func TestToolError_WithAvailableValues(t *testing.T) {
	err := NewToolError(InvalidParameter, "Invalid value")
	values := []string{"option1", "option2", "option3"}

	result := err.WithAvailableValues(values)

	assert.Equal(t, values, result.AvailableValues)
	assert.Same(t, err, result)
}

func TestToolError_WithExamples(t *testing.T) {
	err := NewToolError(InvalidFormat, "Invalid date format")
	examples := []string{"2024-01-01", "2024-12-31"}

	result := err.WithExamples(examples)

	assert.Equal(t, examples, result.Examples)
	assert.Same(t, err, result)
}

func TestToolError_WithSuggestions(t *testing.T) {
	err := NewToolError(PermissionDenied, "Access denied")
	suggestions := []string{"Check file permissions", "Use a different path"}

	result := err.WithSuggestions(suggestions)

	assert.Equal(t, suggestions, result.Suggestions)
	assert.Same(t, err, result)
}

func TestToolError_WithContext(t *testing.T) {
	err := NewToolError(PermissionDenied, "Access denied")
	ctx := &ErrorContext{
		AllowedPaths:   []string{"/workspace", "/tmp"},
		CurrentUser:    "testuser",
		CurrentChannel: "telegram",
		ChannelStatus: map[string]string{
			"telegram": "online",
			"slack":    "offline",
		},
		SystemLimits: map[string]int64{
			"max_file_size": 10485760,
		},
		Configuration: map[string]string{
			"workspace": "/workspace",
		},
	}

	result := err.WithContext(ctx)

	require.NotNil(t, result.Context)
	assert.Equal(t, ctx.AllowedPaths, result.Context.AllowedPaths)
	assert.Equal(t, ctx.CurrentUser, result.Context.CurrentUser)
	assert.Equal(t, ctx.CurrentChannel, result.Context.CurrentChannel)
	assert.Equal(t, ctx.ChannelStatus, result.Context.ChannelStatus)
	assert.Equal(t, ctx.SystemLimits, result.Context.SystemLimits)
	assert.Equal(t, ctx.Configuration, result.Context.Configuration)
	assert.Same(t, err, result)
}

func TestToolError_FluentChaining(t *testing.T) {
	err := NewToolError(InvalidParameter, "Invalid channel").
		WithParameter("channel", "bad_channel").
		WithAvailableValues([]string{"slack", "telegram"}).
		WithExamples([]string{"slack"}).
		WithSuggestions([]string{"Try using 'slack' or 'telegram'"}).
		WithContext(&ErrorContext{CurrentUser: "test"})

	assert.Equal(t, InvalidParameter, err.Type)
	assert.Equal(t, "Invalid channel", err.Message)
	assert.Equal(t, "channel", err.Parameter)
	assert.Equal(t, "bad_channel", err.ProvidedValue)
	assert.Equal(t, []string{"slack", "telegram"}, err.AvailableValues)
	assert.Equal(t, []string{"slack"}, err.Examples)
	assert.Equal(t, []string{"Try using 'slack' or 'telegram'"}, err.Suggestions)
	assert.Equal(t, "test", err.Context.CurrentUser)
}

func TestToolError_ToMap(t *testing.T) {
	timestamp := time.Now()
	err := &ToolError{
		Type:            InvalidParameter,
		Message:         "Invalid channel",
		Parameter:       "channel",
		ProvidedValue:   "bad_channel",
		AvailableValues: []string{"slack", "telegram"},
		Examples:        []string{"slack"},
		Suggestions:     []string{"Use a valid channel"},
		Context: &ErrorContext{
			CurrentUser: "testuser",
		},
		Timestamp: timestamp,
	}

	result := err.ToMap()

	assert.Equal(t, string(InvalidParameter), result["error_type"])
	assert.Equal(t, "Invalid channel", result["message"])
	assert.Equal(t, "channel", result["parameter"])
	assert.Equal(t, "bad_channel", result["provided_value"])
	assert.Equal(t, []string{"slack", "telegram"}, result["available_values"])
	assert.Equal(t, []string{"slack"}, result["examples"])
	assert.Equal(t, []string{"Use a valid channel"}, result["suggestions"])
	assert.Equal(t, timestamp, result["timestamp"])
	assert.NotNil(t, result["context"])
}

func TestToolError_ToMap_MinimalFields(t *testing.T) {
	timestamp := time.Now()
	err := &ToolError{
		Type:      ServiceUnavailable,
		Message:   "Service down",
		Timestamp: timestamp,
	}

	result := err.ToMap()

	assert.Equal(t, string(ServiceUnavailable), result["error_type"])
	assert.Equal(t, "Service down", result["message"])
	assert.Equal(t, timestamp, result["timestamp"])

	// These should not be present
	_, hasParameter := result["parameter"]
	_, hasProvidedValue := result["provided_value"]
	_, hasAvailableValues := result["available_values"]
	_, hasExamples := result["examples"]
	_, hasSuggestions := result["suggestions"]
	_, hasContext := result["context"]

	assert.False(t, hasParameter)
	assert.False(t, hasProvidedValue)
	assert.False(t, hasAvailableValues)
	assert.False(t, hasExamples)
	assert.False(t, hasSuggestions)
	assert.False(t, hasContext)
}

func TestParameterError(t *testing.T) {
	err := ParameterError("channel", "bad_value", "Channel is invalid")

	assert.Equal(t, InvalidParameter, err.Type)
	assert.Equal(t, "Channel is invalid", err.Message)
	assert.Equal(t, "channel", err.Parameter)
	assert.Equal(t, "bad_value", err.ProvidedValue)
	assert.False(t, err.Timestamp.IsZero())
}

func TestParameterError_WithNilValue(t *testing.T) {
	err := ParameterError("data", nil, "Data is invalid")

	assert.Equal(t, InvalidParameter, err.Type)
	assert.Equal(t, "data", err.Parameter)
	assert.Nil(t, err.ProvidedValue)
}

func TestParameterError_WithComplexValue(t *testing.T) {
	complexValue := map[string]int{"key": 123}
	err := ParameterError("config", complexValue, "Config is invalid")

	assert.Equal(t, InvalidParameter, err.Type)
	assert.Equal(t, "config", err.Parameter)
	assert.Equal(t, complexValue, err.ProvidedValue)
}

func TestMissingParameterError(t *testing.T) {
	examples := []string{"example1", "example2"}
	err := MissingParameterError("api_key", examples)

	assert.Equal(t, MissingParameter, err.Type)
	assert.Equal(t, "Parameter 'api_key' is required", err.Message)
	assert.Equal(t, "api_key", err.Parameter)
	assert.Nil(t, err.ProvidedValue)
	assert.Equal(t, examples, err.Examples)
	assert.False(t, err.Timestamp.IsZero())
}

func TestMissingParameterError_WithEmptyExamples(t *testing.T) {
	err := MissingParameterError("token", []string{})

	assert.Equal(t, MissingParameter, err.Type)
	assert.Equal(t, "Parameter 'token' is required", err.Message)
	assert.Equal(t, "token", err.Parameter)
	assert.Empty(t, err.Examples)
}

func TestMissingParameterError_WithNilExamples(t *testing.T) {
	err := MissingParameterError("secret", nil)

	assert.Equal(t, MissingParameter, err.Type)
	assert.Equal(t, "Parameter 'secret' is required", err.Message)
	assert.Nil(t, err.Examples)
}

func TestServiceUnavailableError(t *testing.T) {
	availableServices := []string{"slack", "telegram"}
	err := ServiceUnavailableError("discord", availableServices)

	assert.Equal(t, ServiceUnavailable, err.Type)
	assert.Equal(t, "Service 'discord' is not available", err.Message)
	assert.Equal(t, "service", err.Parameter)
	assert.Equal(t, "discord", err.ProvidedValue)
	assert.Equal(t, availableServices, err.AvailableValues)
	assert.False(t, err.Timestamp.IsZero())
}

func TestServiceUnavailableError_WithEmptyAvailable(t *testing.T) {
	err := ServiceUnavailableError("any_service", []string{})

	assert.Equal(t, ServiceUnavailable, err.Type)
	assert.Contains(t, err.Message, "any_service")
	assert.Empty(t, err.AvailableValues)
}

func TestFilePermissionError(t *testing.T) {
	allowedPaths := []string{"/workspace", "/tmp"}
	err := FilePermissionError("/etc/passwd", allowedPaths, "read")

	assert.Equal(t, PermissionDenied, err.Type)
	assert.Contains(t, err.Message, "read")
	assert.Contains(t, err.Message, "/etc/passwd")
	assert.Equal(t, "path", err.Parameter)
	assert.Equal(t, "/etc/passwd", err.ProvidedValue)
	require.NotNil(t, err.Context)
	assert.Equal(t, allowedPaths, err.Context.AllowedPaths)
	assert.Len(t, err.Suggestions, 2)
	assert.Contains(t, err.Suggestions[0], "workspace")
	assert.Contains(t, err.Suggestions[1], "permissions")
}

func TestFilePermissionError_DifferentOperations(t *testing.T) {
	operations := []string{"read", "write", "delete", "execute"}

	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			err := FilePermissionError("/forbidden/path", []string{"/allowed"}, op)
			assert.Contains(t, err.Message, op)
			assert.Contains(t, err.Message, "/forbidden/path")
		})
	}
}

func TestErrorType_Constants(t *testing.T) {
	// Verify all error type constants are unique and have expected string values
	types := map[ErrorType]string{
		InvalidParameter:     "invalid_parameter",
		MissingParameter:     "missing_parameter",
		ParameterOutOfRange:  "parameter_out_of_range",
		InvalidFormat:        "invalid_format",
		PermissionDenied:     "permission_denied",
		FileNotFound:         "file_not_found",
		PathNotAllowed:       "path_not_allowed",
		ServiceUnavailable:   "service_unavailable",
		ChannelOffline:       "channel_offline",
		AuthenticationFailed: "authentication_failed",
		RateLimitExceeded:    "rate_limit_exceeded",
		ResourceNotFound:     "resource_not_found",
		ResourceConflict:     "resource_conflict",
		InsufficientQuota:    "insufficient_quota",
		InternalError:        "internal_error",
		TimeoutError:         "timeout_error",
		ConfigurationError:   "configuration_error",
	}

	for errType, expectedStr := range types {
		assert.Equal(t, expectedStr, string(errType), "ErrorType %s has unexpected string value", errType)
	}

	// Verify all values are unique
	seen := make(map[string]bool)
	for _, str := range types {
		assert.False(t, seen[str], "Duplicate ErrorType string value: %s", str)
		seen[str] = true
	}
}

func TestToolError_ErrorInterface(t *testing.T) {
	// Verify ToolError implements error interface
	var err error = NewToolError(InternalError, "Test error")
	assert.NotNil(t, err)
	assert.Equal(t, "Test error", err.Error())
}

func TestToolError_Error_DotsAsSeparators(t *testing.T) {
	// Verify parts are joined with ". "
	err := &ToolError{
		Message:         "Main error",
		AvailableValues: []string{"a", "b"},
		Examples:        []string{"x"},
		Suggestions:     []string{"Do this"},
	}

	result := err.Error()

	// Count the number of ". " separators
	parts := strings.Split(result, ". ")
	assert.Len(t, parts, 4, "Expected 4 parts separated by '. '")
}

func TestErrorContext_AllFields(t *testing.T) {
	ctx := &ErrorContext{
		ChannelStatus: map[string]string{
			"slack":    "online",
			"telegram": "offline",
		},
		AllowedPaths:   []string{"/workspace", "/home/user"},
		CurrentUser:    "admin",
		CurrentChannel: "slack",
		SystemLimits: map[string]int64{
			"max_file_size":    1048576,
			"rate_limit":       100,
			"max_connections":  50,
		},
		Configuration: map[string]string{
			"debug":    "true",
			"log_level": "info",
		},
	}

	// Verify all fields are accessible
	assert.Len(t, ctx.ChannelStatus, 2)
	assert.Equal(t, "online", ctx.ChannelStatus["slack"])
	assert.Equal(t, "offline", ctx.ChannelStatus["telegram"])
	assert.Len(t, ctx.AllowedPaths, 2)
	assert.Equal(t, "admin", ctx.CurrentUser)
	assert.Equal(t, "slack", ctx.CurrentChannel)
	assert.Len(t, ctx.SystemLimits, 3)
	assert.Equal(t, int64(1048576), ctx.SystemLimits["max_file_size"])
	assert.Len(t, ctx.Configuration, 2)
	assert.Equal(t, "true", ctx.Configuration["debug"])
}

func TestToolError_WithParameter_NilValue(t *testing.T) {
	err := NewToolError(InvalidParameter, "Test").WithParameter("test", nil)

	assert.Equal(t, "test", err.Parameter)
	assert.Nil(t, err.ProvidedValue)
}

func TestToolError_WithParameter_EmptyName(t *testing.T) {
	err := NewToolError(InvalidParameter, "Test").WithParameter("", "value")

	assert.Equal(t, "", err.Parameter)
	assert.Equal(t, "value", err.ProvidedValue)
}

func TestToolError_ToMap_WithEmptyContext(t *testing.T) {
	err := &ToolError{
		Type:      InternalError,
		Message:   "Error",
		Context:   &ErrorContext{},
		Timestamp: time.Now(),
	}

	result := err.ToMap()

	// Context should be present even if empty
	assert.NotNil(t, result["context"])
}

func TestToolError_WithAvailableValues_Empty(t *testing.T) {
	err := NewToolError(InvalidParameter, "Test").WithAvailableValues([]string{})

	assert.Empty(t, err.AvailableValues)
	// Error() should not include "Available:" for empty slice
	assert.NotContains(t, err.Error(), "Available:")
}

func TestToolError_WithExamples_Empty(t *testing.T) {
	err := NewToolError(InvalidParameter, "Test").WithExamples([]string{})

	assert.Empty(t, err.Examples)
	// Error() should not include "Examples:" for empty slice
	assert.NotContains(t, err.Error(), "Examples:")
}

func TestToolError_WithSuggestions_Empty(t *testing.T) {
	err := NewToolError(InvalidParameter, "Test").WithSuggestions([]string{})

	assert.Empty(t, err.Suggestions)
	// Error() should not include "Try:" for empty slice
	assert.NotContains(t, err.Error(), "Try:")
}
