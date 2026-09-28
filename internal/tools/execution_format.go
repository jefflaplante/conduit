package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// formatToolResultForAI formats tool results for AI consumption
func (e *ExecutionEngine) formatToolResultForAI(result *ExecutionResult) string {
	if result.Error != nil {
		msg := fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Error.Error())
		// conduit-31jg.47: surface output a tool returned alongside its error.
		if result.Result != nil {
			if out := strings.TrimSpace(result.Result.Content); out != "" && !strings.Contains(msg, out) {
				msg += "\nOutput:\n" + e.truncateForModel(out)
			}
		}
		return msg
	}

	if result.Result == nil {
		return fmt.Sprintf("Tool '%s' executed but returned no result", result.ToolCall.Name)
	}

	if !result.Result.Success {
		msg := fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Result.Error)

		// Surface rich error details when available
		if details := result.Result.ErrorDetails; details != nil {
			if details.Type != "" {
				msg += fmt.Sprintf("\nError type: %s", details.Type)
			}
			if len(details.Suggestions) > 0 {
				msg += "\nSuggestions:"
				for _, s := range details.Suggestions {
					msg += fmt.Sprintf("\n- %s", s)
				}
			}
			if len(details.AvailableValues) > 0 {
				msg += fmt.Sprintf("\nAvailable values: %s", strings.Join(details.AvailableValues, ", "))
			}
			if len(details.Examples) > 0 {
				msg += fmt.Sprintf("\nExamples: %s", strings.Join(details.Examples, ", "))
			}
		}

		// conduit-31jg.10: surface the tool's output (e.g. compiler/test stderr
		// from a non-zero Bash exit) so the model isn't blind to why it failed.
		// ErrorDetails.Context["output"] is intentionally not rendered, so the
		// output appears exactly once.
		if out := strings.TrimSpace(result.Result.Content); out != "" && out != strings.TrimSpace(result.Result.Error) {
			maxChars := e.maxResultChars
			if maxChars <= 0 {
				maxChars = DefaultMaxToolResultChars
			}
			if len(out) > maxChars {
				out = e.smartTruncate(out, maxChars)
			}
			msg += "\nOutput:\n" + out
		}

		return msg
	}

	// conduit-31jg.39: Data is appended only for tools that opt in (their
	// payload lives in Data) or when Content is empty. Appending it to every
	// result doubled Glob's file list and re-sent Chain step outputs.
	content := result.Result.Content
	if len(result.Result.Data) > 0 && (strings.TrimSpace(content) == "" || e.toolWantsDataInOutput(result.ToolCall.Name)) {
		if dataJSON, err := json.Marshal(result.Result.Data); err == nil {
			content += fmt.Sprintf("\n\nStructured data: %s", string(dataJSON))
		}
	}

	// Smart truncation to protect context window while preserving important content
	maxChars := e.maxResultChars
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}
	if len(content) > maxChars {
		content = e.smartTruncate(content, maxChars)
	}

	return content
}

// truncateForModel applies the result budget to s (conduit-31jg.47).
func (e *ExecutionEngine) truncateForModel(s string) string {
	maxChars := e.maxResultChars
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}
	if len(s) > maxChars {
		return e.smartTruncate(s, maxChars)
	}
	return s
}

// toolWantsDataInOutput reports whether the named tool opted into having
// ToolResult.Data rendered for the model (Registry.IncludeDataInModelOutput).
func (e *ExecutionEngine) toolWantsDataInOutput(name string) bool {
	p, ok := e.registry.(interface{ IncludeDataInModelOutput(name string) bool })
	return ok && p.IncludeDataInModelOutput(name)
}

// headTailRunes keeps headSize bytes from the start and tailSize from the
// end of s with marker between, never splitting a UTF-8 rune (conduit-31jg.39).
func headTailRunes(s string, headSize, tailSize int, marker string) string {
	if headSize < 0 {
		headSize = 0
	}
	if tailSize < 0 {
		tailSize = 0
	}
	if headSize+tailSize >= len(s) {
		return s
	}
	for headSize > 0 && !utf8.RuneStart(s[headSize]) {
		headSize--
	}
	tailStart := len(s) - tailSize
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	return s[:headSize] + marker + s[tailStart:]
}

// smartTruncate performs intelligent truncation preserving head, tail, and error lines.
func (e *ExecutionEngine) smartTruncate(content string, maxChars int) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	cfg := e.truncationConfig
	headLines := cfg.HeadLines
	tailLines := cfg.TailLines
	if headLines <= 0 {
		headLines = 20
	}
	if tailLines <= 0 {
		tailLines = 20
	}

	// If content is small enough by line count, fall back to char-based truncation
	if totalLines <= headLines+tailLines {
		// Simple char truncation: keep first 80% and last 20%
		headSize := maxChars * 4 / 5
		tailSize := maxChars / 5
		return headTailRunes(content, headSize, tailSize,
			fmt.Sprintf("\n\n...(truncated, showing %d of %d chars)...\n\n", maxChars, len(content)))
	}

	// Collect head lines
	head := lines[:headLines]

	// Collect tail lines
	tail := lines[totalLines-tailLines:]

	// Find important lines in the middle section
	middleStart := headLines
	middleEnd := totalLines - tailLines
	var preservedMiddle []string

	for i := middleStart; i < middleEnd; i++ {
		if e.lineContainsPattern(lines[i]) {
			preservedMiddle = append(preservedMiddle, lines[i])
		}
	}

	// Calculate truncated line count
	truncatedCount := (middleEnd - middleStart) - len(preservedMiddle)

	// Build result
	var result strings.Builder
	result.WriteString(strings.Join(head, "\n"))

	if len(preservedMiddle) > 0 || truncatedCount > 0 {
		result.WriteString(fmt.Sprintf("\n\n[...truncated %d lines, preserved %d lines with errors/warnings...]\n\n",
			truncatedCount, len(preservedMiddle)))

		if len(preservedMiddle) > 0 {
			result.WriteString(strings.Join(preservedMiddle, "\n"))
			result.WriteString("\n\n[...end of preserved section...]\n\n")
		}
	} else {
		result.WriteString("\n")
	}

	result.WriteString(strings.Join(tail, "\n"))

	// Final char limit check
	finalContent := result.String()
	if len(finalContent) > maxChars {
		// Truncate preserved middle if still too long
		headSize := maxChars * 4 / 5
		tailSize := maxChars / 5
		return headTailRunes(finalContent, headSize, tailSize,
			fmt.Sprintf("\n\n...(final truncation, showing %d of %d chars)...\n\n", maxChars, len(finalContent)))
	}

	return finalContent
}

// lineContainsPattern checks if a line contains any of the configured preserve patterns.
func (e *ExecutionEngine) lineContainsPattern(line string) bool {
	for _, pattern := range e.truncationConfig.PreservePatterns {
		if strings.Contains(line, pattern) {
			return true
		}
	}
	return false
}
