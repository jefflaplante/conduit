package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		input      string
		wantPrefix string
		wantDetail string
	}{
		{"file:MEMORY.md#About Jeff", "file", "MEMORY.md#About Jeff"},
		{"skill:brain-seed-memory", "skill", "brain-seed-memory"},
		{"tool", "tool", ""},
		{"user:manual", "user", "manual"},
		{"llm:generated", "llm", "generated"},
		{"sub-agent:research", "sub-agent", "research"},
		{"", "", ""},
		{"unknown:something", "unknown", "something"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			prefix, detail := ParseSource(tt.input)
			assert.Equal(t, tt.wantPrefix, prefix)
			assert.Equal(t, tt.wantDetail, detail)
		})
	}
}

func TestValidateSource(t *testing.T) {
	// Known prefixes should pass
	for _, src := range []string{"file:test.md", "skill:foo", "tool", "user:manual", "llm:gen", "sub-agent:x"} {
		assert.NoError(t, ValidateSource(src), "source %q should be valid", src)
	}
	// Empty is valid (legacy)
	assert.NoError(t, ValidateSource(""))

	// Unknown prefix returns error
	assert.Error(t, ValidateSource("bogus:thing"))
}
