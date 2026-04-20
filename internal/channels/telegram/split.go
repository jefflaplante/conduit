package telegram

import (
	"strings"
)

const (
	// TelegramMessageLimit is the maximum message length allowed by Telegram's API.
	TelegramMessageLimit = 4096
)

// splitMessage splits text into chunks that fit within Telegram's message limit.
// It prefers splitting at paragraph breaks (\n\n), then sentence breaks (. ),
// then falls back to hard cuts at the limit boundary.
func splitMessage(text string, limit int) []string {
	if limit <= 0 {
		limit = TelegramMessageLimit
	}

	if len(text) <= limit {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > limit {
		// Try to find the best split point within the limit
		splitAt := findSplitPoint(remaining, limit)
		chunk := remaining[:splitAt]
		chunks = append(chunks, strings.TrimSpace(chunk))

		// Move past the split point
		remaining = remaining[splitAt:]
		remaining = strings.TrimPrefix(remaining, "\n")
	}

	// Add the final chunk if non-empty
	remaining = strings.TrimSpace(remaining)
	if remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks
}

// findSplitPoint finds the best position to split text within the given limit.
// Priority: paragraph break > sentence break > hard cut.
func findSplitPoint(text string, limit int) int {
	// If the text is within limit, return its full length
	if len(text) <= limit {
		return len(text)
	}

	searchRegion := text[:limit]

	// 1. Try paragraph break (\n\n) — find the last one in the region
	if idx := strings.LastIndex(searchRegion, "\n\n"); idx > 0 {
		return idx + 2 // include the \n\n so we can trim it after
	}

	// 2. Try single newline
	if idx := strings.LastIndex(searchRegion, "\n"); idx > 0 {
		return idx + 1 // include the newline so we can trim it after
	}

	// 3. Try sentence end (". " or "! " or "? ")
	bestSentence := -1
	for _, sep := range []string{". ", "! ", "? "} {
		if idx := strings.LastIndex(searchRegion, sep); idx > 0 {
			if idx > bestSentence {
				bestSentence = idx + len(sep)
			}
		}
	}
	if bestSentence > 0 {
		return bestSentence
	}

	// 4. Try space (word boundary)
	if idx := strings.LastIndex(searchRegion, " "); idx > 0 {
		return idx + 1
	}

	// 5. Hard cut at limit
	return limit
}
