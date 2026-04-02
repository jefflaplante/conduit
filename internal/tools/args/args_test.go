package args

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetString(t *testing.T) {
	args := map[string]interface{}{
		"name":   "test",
		"number": 42,
	}

	assert.Equal(t, "test", GetString(args, "name", "default"))
	assert.Equal(t, "default", GetString(args, "missing", "default"))
	assert.Equal(t, "default", GetString(args, "number", "default")) // wrong type
}

func TestGetInt(t *testing.T) {
	args := map[string]interface{}{
		"float":      float64(42),
		"int":        int(42),
		"int64":      int64(42),
		"jsonNumber": json.Number("42"),
		"string":     "42",
		"invalid":    "not a number",
		"bool":       true,
	}

	assert.Equal(t, 42, GetInt(args, "float", 0))
	assert.Equal(t, 42, GetInt(args, "int", 0))
	assert.Equal(t, 42, GetInt(args, "int64", 0))
	assert.Equal(t, 42, GetInt(args, "jsonNumber", 0))
	assert.Equal(t, 42, GetInt(args, "string", 0))
	assert.Equal(t, 0, GetInt(args, "invalid", 0))   // unparseable string
	assert.Equal(t, 0, GetInt(args, "bool", 0))      // wrong type
	assert.Equal(t, 99, GetInt(args, "missing", 99)) // not found
}

func TestGetInt64(t *testing.T) {
	args := map[string]interface{}{
		"float":      float64(9223372036854775807),
		"int64":      int64(9223372036854775807),
		"jsonNumber": json.Number("9223372036854775807"),
		"string":     "9223372036854775807",
	}

	assert.Equal(t, int64(9223372036854775807), GetInt64(args, "int64", 0))
	assert.Equal(t, int64(9223372036854775807), GetInt64(args, "jsonNumber", 0))
	assert.Equal(t, int64(9223372036854775807), GetInt64(args, "string", 0))
	assert.Equal(t, int64(99), GetInt64(args, "missing", 99))
}

func TestGetFloat64(t *testing.T) {
	args := map[string]interface{}{
		"float":      float64(3.14),
		"int":        int(42),
		"int64":      int64(42),
		"jsonNumber": json.Number("3.14"),
		"string":     "3.14",
	}

	assert.InDelta(t, 3.14, GetFloat64(args, "float", 0), 0.001)
	assert.InDelta(t, 42.0, GetFloat64(args, "int", 0), 0.001)
	assert.InDelta(t, 42.0, GetFloat64(args, "int64", 0), 0.001)
	assert.InDelta(t, 3.14, GetFloat64(args, "jsonNumber", 0), 0.001)
	assert.InDelta(t, 3.14, GetFloat64(args, "string", 0), 0.001)
	assert.InDelta(t, 1.5, GetFloat64(args, "missing", 1.5), 0.001)
}

func TestGetBool(t *testing.T) {
	args := map[string]interface{}{
		"true":   true,
		"false":  false,
		"string": "true",
	}

	assert.True(t, GetBool(args, "true", false))
	assert.False(t, GetBool(args, "false", true))
	assert.True(t, GetBool(args, "string", true))   // wrong type, uses default
	assert.False(t, GetBool(args, "missing", false)) // not found
}

func TestGetStringSlice(t *testing.T) {
	args := map[string]interface{}{
		"direct": []string{"a", "b", "c"},
		"interface": []interface{}{"x", "y", "z"},
		"mixed":     []interface{}{"a", 1, "b"}, // non-strings ignored
		"string":    "not a slice",
	}

	assert.Equal(t, []string{"a", "b", "c"}, GetStringSlice(args, "direct"))
	assert.Equal(t, []string{"x", "y", "z"}, GetStringSlice(args, "interface"))
	assert.Equal(t, []string{"a", "b"}, GetStringSlice(args, "mixed"))
	assert.Nil(t, GetStringSlice(args, "string"))
	assert.Nil(t, GetStringSlice(args, "missing"))
}
