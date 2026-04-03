package telegram

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeUserFacingText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "plain text unchanged",
			input:    "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name:     "strips standalone bash tag",
			input:    "Before <bash> After",
			expected: "Before  After",
		},
		{
			name:     "strips closing bash tag",
			input:    "Before </bash> After",
			expected: "Before  After",
		},
		{
			name:     "strips thinking tag",
			input:    "Before <thinking> After",
			expected: "Before  After",
		},
		{
			name:     "strips tool_call tag",
			input:    "Before <tool_call> After",
			expected: "Before  After",
		},
		{
			name:     "strips invoke tag with attributes",
			input:    "Before <invoke name=\"test\"> After",
			expected: "Before  After",
		},
		{
			name:     "strips parameter tag",
			input:    "Before <parameter name=\"foo\"> After",
			expected: "Before  After",
		},
		{
			name:     "strips Tool Call markers",
			input:    "Before [Tool Call: bash] After",
			expected: "Before  After",
		},
		{
			name:     "strips Tool Result markers",
			input:    "Before [Tool Result: success] After",
			expected: "Before  After",
		},
		{
			name:     "collapses excessive newlines",
			input:    "Line 1\n\n\n\nLine 2",
			expected: "Line 1\n\nLine 2",
		},
		{
			name:     "trims leading/trailing whitespace",
			input:    "  Hello  ",
			expected: "Hello",
		},
		{
			name:     "handles complex mixed content",
			input:    "Start <bash>cmd</bash> middle [Tool Call: test] end",
			expected: "Start cmd middle  end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeUserFacingText(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertToTelegramMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text unchanged",
			input:    "Hello, world!",
			expected: "Hello, world!",
		},
		{
			name:     "converts double asterisk bold to single",
			input:    "This is **bold** text",
			expected: "This is *bold* text",
		},
		{
			name:     "converts header to bold",
			input:    "# Header",
			expected: "*Header*",
		},
		{
			name:     "converts h2 header to bold",
			input:    "## Second Header",
			expected: "*Second Header*",
		},
		{
			name:     "converts h3 header to bold",
			input:    "### Third Header",
			expected: "*Third Header*",
		},
		{
			name:     "converts links to text (url) format",
			input:    "Check [this link](https://example.com)",
			expected: "Check this link (https://example.com)",
		},
		{
			name:     "preserves code blocks",
			input:    "```\ncode here\n```",
			expected: "```\ncode here\n```",
		},
		{
			name:     "preserves inline code",
			input:    "Run `echo hello`",
			expected: "Run `echo hello`",
		},
		{
			name:     "collapses excessive newlines",
			input:    "Line 1\n\n\n\nLine 2",
			expected: "Line 1\n\nLine 2",
		},
		{
			name:     "multiple bold conversions",
			input:    "**First** and **second**",
			expected: "*First* and *second*",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToTelegramMarkdown(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertToTelegramMarkdown_Tables(t *testing.T) {
	// Tables should be wrapped in code blocks
	input := "| Header 1 | Header 2 |\n|----------|----------|\n| Cell 1   | Cell 2   |"
	result := convertToTelegramMarkdown(input)

	// Should contain code block markers
	assert.Contains(t, result, "```")
	assert.Contains(t, result, "Header 1")
}

func TestConvertToTelegramMarkdown_MixedContent(t *testing.T) {
	input := "# Welcome\n\n**Important:** Check [docs](https://docs.example.com)"
	result := convertToTelegramMarkdown(input)

	assert.Contains(t, result, "*Welcome*")      // header converted
	assert.Contains(t, result, "*Important:*")   // bold converted
	assert.Contains(t, result, "docs (https://") // link converted
}
