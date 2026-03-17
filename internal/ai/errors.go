package ai

import (
	"encoding/json"
	"strings"
)

// UserFriendlyError extracts a human-readable message from an AI provider error.
// API errors typically have the format "API error: 400 - {json body}" where the
// JSON body contains an error.message field with a descriptive message.
// Falls back to the original error string if parsing fails.
func UserFriendlyError(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()

	// Try to extract JSON body from "API error: NNN - {json}" format
	idx := strings.Index(errStr, " - {")
	if idx == -1 {
		return errStr
	}

	jsonBody := errStr[idx+3:]
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(jsonBody), &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}

	return errStr
}
