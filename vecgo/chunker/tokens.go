package chunker

import (
	"strings"
	"unicode"
)

// CountTokens estimates token count using whitespace splitting.
// This is approximate but fast and good enough for chunking decisions.
func CountTokens(text string) int {
	count := 0
	inWord := false

	for _, r := range text {
		if unicode.IsSpace(r) {
			if inWord {
				count++
				inWord = false
			}
		} else {
			inWord = true
		}
	}
	if inWord {
		count++
	}

	return count
}

// TruncateToTokens truncates text to approximately maxTokens.
func TruncateToTokens(text string, maxTokens int) string {
	words := strings.Fields(text)
	if len(words) <= maxTokens {
		return text
	}
	return strings.Join(words[:maxTokens], " ")
}
