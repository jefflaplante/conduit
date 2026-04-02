// Package args provides shared argument parsing helpers for tool implementations.
// These helpers handle type coercion from JSON-decoded map[string]interface{} values
// to Go types, with sensible defaults when values are missing or have unexpected types.
package args

import (
	"encoding/json"
	"strconv"
)

// GetString extracts a string value from args, returning defaultVal if not found or wrong type.
func GetString(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

// GetInt extracts an int value from args, handling float64 (JSON default), int, int64,
// json.Number, and string representations. Returns defaultVal if not found or unparseable.
func GetInt(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			if parsed, err := strconv.Atoi(n); err == nil {
				return parsed
			}
		}
	}
	return defaultVal
}

// GetInt64 extracts an int64 value from args, handling float64, int, int64, and json.Number.
// Returns defaultVal if not found or wrong type.
func GetInt64(args map[string]interface{}, key string, defaultVal int64) int64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int:
			return int64(n)
		case int64:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return i
			}
		case string:
			if parsed, err := strconv.ParseInt(n, 10, 64); err == nil {
				return parsed
			}
		}
	}
	return defaultVal
}

// GetFloat64 extracts a float64 value from args, handling float64 and string representations.
// Returns defaultVal if not found or unparseable.
func GetFloat64(args map[string]interface{}, key string, defaultVal float64) float64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case json.Number:
			if f, err := n.Float64(); err == nil {
				return f
			}
		case string:
			if parsed, err := strconv.ParseFloat(n, 64); err == nil {
				return parsed
			}
		}
	}
	return defaultVal
}

// GetBool extracts a bool value from args, returning defaultVal if not found or wrong type.
func GetBool(args map[string]interface{}, key string, defaultVal bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultVal
}

// GetStringSlice extracts a []string from args. Handles both []string and []interface{}.
// Returns nil if not found or wrong type.
func GetStringSlice(args map[string]interface{}, key string) []string {
	if v, ok := args[key]; ok {
		// Direct []string
		if ss, ok := v.([]string); ok {
			return ss
		}
		// JSON-decoded []interface{}
		if arr, ok := v.([]interface{}); ok {
			result := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					result = append(result, s)
				}
			}
			return result
		}
	}
	return nil
}
