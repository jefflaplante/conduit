package telegram

import (
	"regexp"
	"strings"
)

// Patterns for sanitizing user-facing text
var (
	// Strip complete XML blocks: <claude_function_calls>...</claude_function_calls>
	claudeFunctionCallsRe  = regexp.MustCompile(`(?s)<\s*claude_function_calls\s*>.*?</\s*claude_function_calls\s*>`)
	claudeFunctionResultRe = regexp.MustCompile(`(?s)<\s*claude_function_result\s*>.*?</\s*claude_function_result\s*>`)
	antmlFunctionCallsRe   = regexp.MustCompile(`(?s)<\s*antml:function_calls\s*>.*?</\s*antml:function_calls\s*>`)
	antmlInvokeRe          = regexp.MustCompile(`(?s)<\s*antml:invoke[^>]*>.*?</\s*antml:invoke\s*>`)

	// Strip standalone XML-like tags that shouldn't be visible to users
	// Matches: <bash>, </bash>, <thinking>, <invoke name="...">, <parameter name="...">, etc.
	xmlTagRe = regexp.MustCompile(`<\s*/?(?:bash|thinking|final|tool_call|invoke|parameter|claude_function_calls|claude_function_result|antml:function_calls|antml:invoke|antml:parameter)[^>]*>`)

	// Strip [Tool Call: ...] and [Tool Result: ...] markers
	toolMarkerRe = regexp.MustCompile(`\[Tool (?:Call|Result)[^\]]*\]`)

	// Collapse multiple newlines into double newlines
	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// sanitizeUserFacingText cleans up AI output before sending to users
// Strips internal markers, XML-like tags, and normalizes whitespace
func sanitizeUserFacingText(text string) string {
	if text == "" {
		return text
	}

	// First: Strip complete XML blocks with their content
	cleaned := claudeFunctionCallsRe.ReplaceAllString(text, "")
	cleaned = claudeFunctionResultRe.ReplaceAllString(cleaned, "")
	cleaned = antmlFunctionCallsRe.ReplaceAllString(cleaned, "")
	cleaned = antmlInvokeRe.ReplaceAllString(cleaned, "")

	// Second: Strip any remaining standalone XML-like tags
	cleaned = xmlTagRe.ReplaceAllString(cleaned, "")

	// Strip tool call markers like [Tool Call: ...]
	cleaned = toolMarkerRe.ReplaceAllString(cleaned, "")

	// Collapse excessive newlines
	cleaned = multiNewlineRe.ReplaceAllString(cleaned, "\n\n")

	// Trim leading/trailing whitespace
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// Regex patterns for Telegram markdown conversion
var (
	// Headers: # Header -> *Header* (bold with single asterisk)
	headerRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	// Standard **bold** -> *bold* (Telegram uses single asterisk)
	doubleBoldRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// Links [text](url) -> text (url) - Telegram markdown doesn't support links
	linkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	// Markdown tables: lines starting with |
	tableRe = regexp.MustCompile(`(?m)((?:^\|.+\|$\n?)+)`)
)

// convertToTelegramMarkdown converts standard markdown to Telegram's limited subset
// Telegram: *bold/italic* (single asterisk), `code`, ```code blocks```
func convertToTelegramMarkdown(text string) string {
	result := text

	// Wrap markdown tables in code blocks for monospace alignment
	result = tableRe.ReplaceAllStringFunc(result, func(table string) string {
		// Don't double-wrap if already in a code block
		if strings.Contains(table, "```") {
			return table
		}
		return "```\n" + strings.TrimSpace(table) + "\n```"
	})

	// Convert **bold** to *bold* (single asterisk for Telegram)
	result = doubleBoldRe.ReplaceAllString(result, "*$1*")

	// Convert headers to bold text (strip the # symbols)
	result = headerRe.ReplaceAllString(result, "*$1*")

	// Convert links to "text (url)" format since Telegram markdown doesn't do links
	result = linkRe.ReplaceAllString(result, "$1 ($2)")

	// Clean up excessive whitespace
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)

	return result
}
