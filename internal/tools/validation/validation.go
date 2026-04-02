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

	"conduit/internal/tools/types"
)

// ValidatorFunc is a function that validates a parameter value.
type ValidatorFunc func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError

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
func (v *Validator) ValidateParameters(ctx context.Context, args map[string]interface{}, rules map[string][]ValidatorFunc) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

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
func (v *Validator) generateSuggestions(errors []types.ValidationError) []string {
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
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError {
		if value == nil {
			return &types.ValidationError{
				Parameter: options.Parameter,
				Message:   "is required",
				ErrorType: "missing",
			}
		}

		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "cannot be empty",
				ProvidedValue: value,
				ErrorType:     "missing",
			}
		}

		return nil
	}
}

// StringFormat validates string format (email, URL, etc.).
func StringFormat(format string) ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError {
		str, ok := value.(string)
		if !ok {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
				ErrorType:     "invalid_format",
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
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError {
		target, ok := value.(string)
		if !ok {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
				ErrorType:     "invalid_format",
			}
		}

		if target == "" {
			return nil // Let Required() handle empty validation
		}

		// Get available channels from system state
		availableChannels := options.SystemState.AvailableChannels
		if len(availableChannels) == 0 {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "no channels available",
				ProvidedValue: target,
				Examples:      []interface{}{"Check channel configuration"},
				ErrorType:     "service_unavailable",
			}
		}

		// Check if target matches any available channel
		var channelIds []string
		var offlineChannels []string

		for _, channel := range availableChannels {
			channelIds = append(channelIds, channel.ID)
			if channel.ID == target {
				if channel.Status == "offline" {
					return &types.ValidationError{
						Parameter:       options.Parameter,
						Message:         fmt.Sprintf("channel '%s' is offline", target),
						ProvidedValue:   target,
						AvailableValues: getOnlineChannelIds(availableChannels),
						Examples:        []interface{}{"Try an online channel", "Use action 'status' to check availability"},
						ErrorType:       "service_unavailable",
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

		return &types.ValidationError{
			Parameter:       options.Parameter,
			Message:         fmt.Sprintf("channel '%s' not found", target),
			ProvidedValue:   target,
			AvailableValues: allChannels,
			Examples:        []interface{}{"Use action 'status' to list available channels"},
			ErrorType:       "resource_not_found",
		}
	}
}

// FilePathSandbox validates that a file path is within sandbox restrictions.
func FilePathSandbox() ValidatorFunc {
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError {
		path, ok := value.(string)
		if !ok {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
				ErrorType:     "invalid_format",
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
	return func(ctx context.Context, value interface{}, options ValidatorOptions) *types.ValidationError {
		str, ok := value.(string)
		if !ok {
			return &types.ValidationError{
				Parameter:     options.Parameter,
				Message:       "must be a string",
				ProvidedValue: value,
				ErrorType:     "invalid_format",
			}
		}

		for _, allowed := range allowedValues {
			if str == allowed {
				return nil
			}
		}

		return &types.ValidationError{
			Parameter:       options.Parameter,
			Message:         fmt.Sprintf("'%s' is not a valid option", str),
			ProvidedValue:   str,
			AvailableValues: allowedValues,
			ErrorType:       "invalid_value",
		}
	}
}

// Helper functions

func validateEmail(email, parameter string) *types.ValidationError {
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return &types.ValidationError{
			Parameter:     parameter,
			Message:       "invalid email format",
			ProvidedValue: email,
			Examples:      []interface{}{"user@example.com", "john.doe@company.org"},
			ErrorType:     "invalid_format",
		}
	}
	return nil
}

func validateURL(urlStr, parameter string) *types.ValidationError {
	if _, err := url.Parse(urlStr); err != nil {
		return &types.ValidationError{
			Parameter:     parameter,
			Message:       "invalid URL format",
			ProvidedValue: urlStr,
			Examples:      []interface{}{"https://example.com", "http://localhost:8080"},
			ErrorType:     "invalid_format",
		}
	}

	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return &types.ValidationError{
			Parameter:     parameter,
			Message:       "URL must include protocol",
			ProvidedValue: urlStr,
			Examples:      []interface{}{fmt.Sprintf("https://%s", urlStr)},
			ErrorType:     "invalid_format",
		}
	}

	return nil
}

func validateFilePath(path, parameter string, allowedPaths []string) *types.ValidationError {
	// Resolve absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return &types.ValidationError{
			Parameter:     parameter,
			Message:       "invalid path format",
			ProvidedValue: path,
			Examples:      []interface{}{"./file.txt", "/absolute/path", "relative/path"},
			ErrorType:     "invalid_format",
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
		return &types.ValidationError{
			Parameter:       parameter,
			Message:         fmt.Sprintf("path '%s' not allowed in sandbox", path),
			ProvidedValue:   path,
			AvailableValues: allowedPaths,
			Examples:        []interface{}{"Use relative paths from workspace", "Check sandbox configuration"},
			ErrorType:       "permission_denied",
		}
	}

	// Check if parent directory exists for write operations
	dir := filepath.Dir(absPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return &types.ValidationError{
			Parameter:     parameter,
			Message:       fmt.Sprintf("parent directory does not exist: %s", dir),
			ProvidedValue: path,
			Examples:      []interface{}{"Create parent directory first", "Use existing directory path"},
			ErrorType:     "resource_not_found",
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
