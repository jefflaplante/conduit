// Package validation provides smart parameter validation with helpful error messages.
package validation

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ValidationResult represents the result of parameter validation.
type ValidationResult struct {
	Valid       bool             `json:"valid"`
	Errors      []ParameterError `json:"errors,omitempty"`
	Suggestions []string         `json:"suggestions,omitempty"`
}

// ParameterError provides detailed information about a validation failure.
type ParameterError struct {
	Parameter       string      `json:"parameter"`
	Message         string      `json:"message"`
	ProvidedValue   interface{} `json:"provided_value"`
	ExpectedFormat  string      `json:"expected_format,omitempty"`
	Examples        []string    `json:"examples,omitempty"`
	AvailableValues []string    `json:"available_values,omitempty"`
}

// Error implements the error interface for ParameterError.
func (e ParameterError) Error() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Parameter '%s': %s", e.Parameter, e.Message))

	if e.ProvidedValue != nil {
		parts = append(parts, fmt.Sprintf("Provided: '%v'", e.ProvidedValue))
	}

	if e.ExpectedFormat != "" {
		parts = append(parts, fmt.Sprintf("Expected format: %s", e.ExpectedFormat))
	}

	if len(e.Examples) > 0 {
		parts = append(parts, fmt.Sprintf("Examples: %s", strings.Join(e.Examples, ", ")))
	}

	if len(e.AvailableValues) > 0 {
		if len(e.AvailableValues) <= 5 {
			parts = append(parts, fmt.Sprintf("Available values: %s", strings.Join(e.AvailableValues, ", ")))
		} else {
			first5 := e.AvailableValues[:5]
			parts = append(parts, fmt.Sprintf("Available values: %s... (%d total)", strings.Join(first5, ", "), len(e.AvailableValues)))
		}
	}

	return strings.Join(parts, ". ")
}

// ValidatorFunc is a function that validates a parameter value.
type ValidatorFunc func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError

// ValidatorOptions provides context for validation.
type ValidatorOptions struct {
	Parameter    string
	Required     bool
	AllowedPaths []string // For file path validation
	SystemState  SystemState
}

// SystemState provides real-time system information for validation.
type SystemState struct {
	AvailableChannels []ChannelInfo
	WorkspaceDir      string
	AllowedPaths      []string
}

// ChannelInfo represents information about an available channel.
type ChannelInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"` // "online", "offline", "error"
	Type   string `json:"type"`   // "group", "private", "channel"
}

// Validator provides parameter validation functionality.
type Validator struct {
	systemState SystemState
}

// NewValidator creates a new parameter validator.
func NewValidator() *Validator {
	return &Validator{}
}

// SetSystemState updates the system state for real-time validation.
func (v *Validator) SetSystemState(state SystemState) {
	v.systemState = state
}

// ValidateParameters validates a map of parameters against their validation rules.
func (v *Validator) ValidateParameters(ctx context.Context, args map[string]interface{}, rules map[string][]ValidatorFunc) *ValidationResult {
	result := &ValidationResult{Valid: true}

	for param, validators := range rules {
		value := args[param]

		for _, validator := range validators {
			if err := validator(ctx, value, ValidatorOptions{
				Parameter:    param,
				SystemState:  v.systemState,
				AllowedPaths: v.systemState.AllowedPaths,
			}); err != nil {
				result.Valid = false
				result.Errors = append(result.Errors, *err)
				break // Stop on first validation error for this parameter
			}
		}
	}

	// Add general suggestions if there are errors
	if !result.Valid {
		result.Suggestions = v.generateSuggestions(result.Errors)
	}

	return result
}

// generateSuggestions creates helpful suggestions based on validation errors.
func (v *Validator) generateSuggestions(errors []ParameterError) []string {
	var suggestions []string

	for _, err := range errors {
		switch {
		case strings.Contains(err.Message, "target") && len(err.AvailableValues) > 0:
			suggestions = append(suggestions, "Use action 'status' to see current channel availability")
		case strings.Contains(err.Message, "file") || strings.Contains(err.Message, "path"):
			suggestions = append(suggestions, "Check file permissions and sandbox restrictions")
		case strings.Contains(err.Message, "email"):
			suggestions = append(suggestions, "Use format: user@domain.com")
		case strings.Contains(err.Message, "URL"):
			suggestions = append(suggestions, "Include protocol (http:// or https://)")
		}
	}

	return suggestions
}

// Common validator functions

// Required validates that a parameter is present and not empty.
func Required() ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError {
		if value == nil {
			return &ParameterError{
				Parameter: options.Parameter,
				Message:   "is required",
			}
		}

		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "cannot be empty",
				ProvidedValue: value,
			}
		}

		return nil
	}
}

// StringFormat validates string format (email, URL, etc.).
func StringFormat(format string) ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError {
		str, ok := value.(string)
		if !ok {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
			}
		}

		switch format {
		case "email":
			return validateEmail(str, options.Parameter)
		case "url":
			return validateURL(str, options.Parameter)
		case "file_path":
			return validateFilePath(str, options.Parameter, options.AllowedPaths)
		default:
			return nil
		}
	}
}

// ChannelTarget validates that a target channel exists and is available.
func ChannelTarget() ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError {
		target, ok := value.(string)
		if !ok {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
			}
		}

		if target == "" {
			return nil // Let Required() handle empty validation
		}

		// Get available channels from system state
		availableChannels := options.SystemState.AvailableChannels
		if len(availableChannels) == 0 {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "no channels available",
				ProvidedValue: target,
				Examples:      []string{"Check channel configuration"},
			}
		}

		// Check if target matches any available channel
		var channelIds []string
		var offlineChannels []string

		for _, channel := range availableChannels {
			channelIds = append(channelIds, channel.ID)
			if channel.ID == target {
				if channel.Status == "offline" {
					return &ParameterError{
						Parameter:       options.Parameter,
						Message:         fmt.Sprintf("channel '%s' is offline", target),
						ProvidedValue:   target,
						AvailableValues: getOnlineChannelIds(availableChannels),
						Examples:        []string{"Try an online channel", "Use action 'status' to check availability"},
					}
				}
				return nil // Valid and online
			}
			if channel.Status == "offline" {
				offlineChannels = append(offlineChannels, fmt.Sprintf("%s (offline)", channel.ID))
			}
		}

		// Target not found
		onlineChannels := getOnlineChannelIds(availableChannels)
		allChannels := append(onlineChannels, offlineChannels...)

		return &ParameterError{
			Parameter:       options.Parameter,
			Message:         fmt.Sprintf("channel '%s' not found", target),
			ProvidedValue:   target,
			AvailableValues: allChannels,
			Examples:        []string{"Use action 'status' to list available channels"},
		}
	}
}

// FilePathSandbox validates that a file path is within sandbox restrictions.
func FilePathSandbox() ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError {
		path, ok := value.(string)
		if !ok {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
			}
		}

		if path == "" {
			return nil // Let Required() handle empty validation
		}

		return validateFilePath(path, options.Parameter, options.AllowedPaths)
	}
}

// OneOf validates that a value is one of the allowed options.
func OneOf(allowedValues []string) ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *ParameterError {
		str, ok := value.(string)
		if !ok {
			return &ParameterError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
			}
		}

		for _, allowed := range allowedValues {
			if str == allowed {
				return nil
			}
		}

		return &ParameterError{
			Parameter:       options.Parameter,
			Message:         fmt.Sprintf("'%s' is not a valid option", str),
			ProvidedValue:   str,
			AvailableValues: allowedValues,
		}
	}
}

// Helper functions

func validateEmail(email, parameter string) *ParameterError {
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return &ParameterError{
			Parameter:      parameter,
			Message:        "invalid email format",
			ProvidedValue:  email,
			ExpectedFormat: "user@domain.com",
			Examples:       []string{"user@example.com", "john.doe@company.org"},
		}
	}
	return nil
}

func validateURL(urlStr, parameter string) *ParameterError {
	if _, err := url.Parse(urlStr); err != nil {
		return &ParameterError{
			Parameter:      parameter,
			Message:        "invalid URL format",
			ProvidedValue:  urlStr,
			ExpectedFormat: "http://example.com or https://example.com",
			Examples:       []string{"https://example.com", "http://localhost:8080"},
		}
	}

	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return &ParameterError{
			Parameter:      parameter,
			Message:        "URL must include protocol",
			ProvidedValue:  urlStr,
			ExpectedFormat: "http://... or https://...",
			Examples:       []string{fmt.Sprintf("https://%s", urlStr)},
		}
	}

	return nil
}

func validateFilePath(path, parameter string, allowedPaths []string) *ParameterError {
	// Resolve absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return &ParameterError{
			Parameter:     parameter,
			Message:       "invalid path format",
			ProvidedValue: path,
			Examples:      []string{"./file.txt", "/absolute/path", "relative/path"},
		}
	}

	// Check sandbox restrictions
	allowed := false
	for _, allowedPath := range allowedPaths {
		if strings.HasPrefix(absPath, allowedPath) {
			allowed = true
			break
		}
	}

	if !allowed {
		return &ParameterError{
			Parameter:       parameter,
			Message:         fmt.Sprintf("path '%s' not allowed in sandbox", path),
			ProvidedValue:   path,
			AvailableValues: allowedPaths,
			Examples:        []string{"Use relative paths from workspace", "Check sandbox configuration"},
		}
	}

	// Check if parent directory exists for write operations
	dir := filepath.Dir(absPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return &ParameterError{
			Parameter:     parameter,
			Message:       fmt.Sprintf("parent directory does not exist: %s", dir),
			ProvidedValue: path,
			Examples:      []string{"Create parent directory first", "Use existing directory path"},
		}
	}

	return nil
}

func getOnlineChannelIds(channels []ChannelInfo) []string {
	var online []string
	for _, channel := range channels {
		if channel.Status == "online" {
			online = append(online, channel.ID)
		}
	}
	return online
}
