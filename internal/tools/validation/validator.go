package validation

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"conduit/internal/tools/types"
)

// Common validation errors
const (
	ErrorTypeMissing            = "missing"
	ErrorTypeInvalidFormat      = "invalid_format"
	ErrorTypePermissionDenied   = "permission_denied"
	ErrorTypeResourceNotFound   = "resource_not_found"
	ErrorTypeServiceUnavailable = "service_unavailable"
	ErrorTypeInvalidValue       = "invalid_value"
)

// CommonValidator provides common validation functions for tools
type CommonValidator struct {
	services *types.ToolServices
}

// NewCommonValidator creates a new common validator
func NewCommonValidator(services *types.ToolServices) *CommonValidator {
	return &CommonValidator{services: services}
}

// ValidateRequired checks if required parameters are present
func (v *CommonValidator) ValidateRequired(args map[string]interface{}, required []string) []types.ValidationError {
	var errors []types.ValidationError

	for _, param := range required {
		if _, exists := args[param]; !exists {
			errors = append(errors, types.ValidationError{
				Parameter: param,
				Message:   fmt.Sprintf("Parameter '%s' is required", param),
				ErrorType: ErrorTypeMissing,
			})
		} else if val, ok := args[param].(string); ok && strings.TrimSpace(val) == "" {
			errors = append(errors, types.ValidationError{
				Parameter: param,
				Message:   fmt.Sprintf("Parameter '%s' cannot be empty", param),
				ErrorType: ErrorTypeMissing,
			})
		}
	}

	return errors
}

// ValidateStringParameter validates a string parameter with format checking
func (v *CommonValidator) ValidateStringParameter(args map[string]interface{}, param string, required bool, format string) *types.ValidationError {
	val, exists := args[param]
	if !exists {
		if required {
			return &types.ValidationError{
				Parameter: param,
				Message:   fmt.Sprintf("Parameter '%s' is required", param),
				ErrorType: ErrorTypeMissing,
			}
		}
		return nil
	}

	strVal, ok := val.(string)
	if !ok {
		return &types.ValidationError{
			Parameter:     param,
			Message:       fmt.Sprintf("Parameter '%s' must be a string", param),
			ProvidedValue: val,
			ErrorType:     ErrorTypeInvalidFormat,
		}
	}

	if required && strings.TrimSpace(strVal) == "" {
		return &types.ValidationError{
			Parameter: param,
			Message:   fmt.Sprintf("Parameter '%s' cannot be empty", param),
			ErrorType: ErrorTypeMissing,
		}
	}

	// Format-specific validation
	switch format {
	case "email":
		return v.validateEmail(param, strVal)
	case "url":
		return v.validateURL(param, strVal)
	case "file_path":
		return v.validateFilePath(param, strVal)
	case "channel_target":
		return v.validateChannelTarget(param, strVal)
	}

	return nil
}

// ValidateEnum validates an enum parameter
func (v *CommonValidator) ValidateEnum(args map[string]interface{}, param string, validValues []string, required bool) *types.ValidationError {
	val, exists := args[param]
	if !exists {
		if required {
			return &types.ValidationError{
				Parameter:       param,
				Message:         fmt.Sprintf("Parameter '%s' is required", param),
				AvailableValues: validValues,
				ErrorType:       ErrorTypeMissing,
				Examples:        convertStringsToInterfaces(validValues),
			}
		}
		return nil
	}

	strVal, ok := val.(string)
	if !ok {
		return &types.ValidationError{
			Parameter:       param,
			Message:         fmt.Sprintf("Parameter '%s' must be a string", param),
			ProvidedValue:   val,
			AvailableValues: validValues,
			ErrorType:       ErrorTypeInvalidFormat,
		}
	}

	for _, valid := range validValues {
		if strVal == valid {
			return nil
		}
	}

	return &types.ValidationError{
		Parameter:       param,
		Message:         fmt.Sprintf("Invalid value '%s' for parameter '%s'", strVal, param),
		ProvidedValue:   strVal,
		AvailableValues: validValues,
		Examples:        convertStringsToInterfaces(validValues[:min(3, len(validValues))]), // Show first 3 as examples
		ErrorType:       ErrorTypeInvalidValue,
	}
}

// Specific format validators
func (v *CommonValidator) validateEmail(param, value string) *types.ValidationError {
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(value) {
		return &types.ValidationError{
			Parameter:     param,
			Message:       fmt.Sprintf("Invalid email format: '%s'", value),
			ProvidedValue: value,
			Examples:      []interface{}{"user@example.com", "admin@domain.org"},
			ErrorType:     ErrorTypeInvalidFormat,
		}
	}
	return nil
}

func (v *CommonValidator) validateURL(param, value string) *types.ValidationError {
	parsedURL, err := url.Parse(value)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return &types.ValidationError{
			Parameter:     param,
			Message:       fmt.Sprintf("Invalid URL format: '%s'", value),
			ProvidedValue: value,
			Examples:      []interface{}{"https://example.com", "http://api.service.com/endpoint"},
			ErrorType:     ErrorTypeInvalidFormat,
		}
	}
	return nil
}

func (v *CommonValidator) validateFilePath(param, value string) *types.ValidationError {
	// Clean the path
	cleanPath := filepath.Clean(value)

	// Check for unsafe patterns
	if strings.Contains(cleanPath, "..") {
		return &types.ValidationError{
			Parameter:     param,
			Message:       "File path cannot contain '..' (parent directory references)",
			ProvidedValue: value,
			Examples:      []interface{}{"file.txt", "/workspace/data.json", "output/results.csv"},
			ErrorType:     ErrorTypePermissionDenied,
		}
	}

	// TODO: Add sandbox validation when we have access to sandbox config
	// This would check if the path is within allowed directories

	return nil
}

func (v *CommonValidator) validateChannelTarget(param, value string) *types.ValidationError {
	// Basic format validation for channel targets
	if !strings.Contains(value, ":") {
		return &types.ValidationError{
			Parameter:     param,
			Message:       fmt.Sprintf("Channel target must include provider prefix: '%s'", value),
			ProvidedValue: value,
			Examples:      []interface{}{"telegram:group:123", "discord:channel:456", "@username"},
			ErrorType:     ErrorTypeInvalidFormat,
			DiscoveryHint: "Use 'conduit tools discover Message target' to see available channels",
		}
	}

	// TODO: Add real-time channel validation when we have access to channel manager
	// This would check if the channel exists and is accessible

	return nil
}

// ValidateChannelAvailability validates if a channel target is available and accessible
func (v *CommonValidator) ValidateChannelAvailability(ctx context.Context, target string) *types.ValidationError {
	if v.services == nil || v.services.Gateway == nil {
		return &types.ValidationError{
			Parameter: "target",
			Message:   "Cannot validate channel availability: gateway service unavailable",
			ErrorType: ErrorTypeServiceUnavailable,
		}
	}

	// Get current channel status
	status, err := v.services.Gateway.GetChannelStatus()
	if err != nil || status == nil {
		return &types.ValidationError{
			Parameter: "target",
			Message:   "Cannot retrieve channel status",
			ErrorType: ErrorTypeServiceUnavailable,
		}
	}

	// Extract provider from target (e.g., "telegram:group:123" -> "telegram")
	parts := strings.SplitN(target, ":", 2)
	if len(parts) < 1 {
		return &types.ValidationError{
			Parameter:     "target",
			Message:       fmt.Sprintf("Invalid target format: '%s'", target),
			ProvidedValue: target,
			Examples:      []interface{}{"telegram:group:123", "discord:channel:456"},
			ErrorType:     ErrorTypeInvalidFormat,
		}
	}

	provider := parts[0]

	// Check if provider is available
	if providerStatus, exists := status[provider]; exists {
		if statusMap, ok := providerStatus.(map[string]interface{}); ok {
			if enabled, ok := statusMap["enabled"].(bool); ok && !enabled {
				return &types.ValidationError{
					Parameter:     "target",
					Message:       fmt.Sprintf("Channel provider '%s' is disabled", provider),
					ProvidedValue: target,
					ErrorType:     ErrorTypeServiceUnavailable,
					DiscoveryHint: "Use 'conduit channels status' to see available channels",
				}
			}
		}
	} else {
		// Provider not found, get available providers for suggestion
		var availableProviders []string
		for providerName := range status {
			availableProviders = append(availableProviders, providerName)
		}

		return &types.ValidationError{
			Parameter:       "target",
			Message:         fmt.Sprintf("Unknown channel provider '%s'", provider),
			ProvidedValue:   target,
			AvailableValues: availableProviders,
			ErrorType:       ErrorTypeResourceNotFound,
			DiscoveryHint:   "Use 'conduit channels status' to see available channels",
		}
	}

	return nil
}

// Helper functions
func convertStringsToInterfaces(strs []string) []interface{} {
	result := make([]interface{}, len(strs))
	for i, s := range strs {
		result[i] = s
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
