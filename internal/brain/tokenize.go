package brain

import (
	"strings"
)

// stopwords contains common English words that add no recall signal.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "can": true, "shall": true,
	"of": true, "in": true, "to": true, "for": true, "with": true,
	"on": true, "at": true, "from": true, "by": true, "about": true,
	"what": true, "who": true, "which": true, "that": true, "this": true,
	"how": true, "when": true, "where": true, "why": true,
	"it": true, "its": true, "my": true, "your": true, "his": true, "her": true,
	"i": true, "me": true, "we": true, "us": true, "you": true, "they": true, "them": true,
	"and": true, "or": true, "but": true, "not": true, "no": true,
	"so": true, "if": true, "then": true, "just": true, "also": true,
	"very": true, "really": true, "recently": true,
}

// delimiters are characters that should split compound tokens.
const delimiters = "/-_,"

// TokenizeQuery processes a recall query into clean search terms.
// It lowercases, splits on whitespace and delimiters, strips stopwords,
// and deduplicates. If all tokens are stopwords, the original tokens
// are returned as a fallback.
func TokenizeQuery(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}

	// Split each word on delimiters and flatten.
	var tokens []string
	for _, w := range words {
		tokens = append(tokens, splitOnDelimiters(w)...)
	}

	// Strip stopwords.
	filtered := stripStopwords(tokens)

	// If everything was a stopword, fall back to original tokens.
	if len(filtered) == 0 {
		filtered = tokens
	}

	return deduplicate(filtered)
}

// splitOnDelimiters splits a single token on /, -, _, and comma.
func splitOnDelimiters(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune(delimiters, r)
	})
	var result []string
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 && s != "" {
		return []string{s}
	}
	return result
}

// stripStopwords removes common English stopwords from a token list.
func stripStopwords(tokens []string) []string {
	var result []string
	for _, t := range tokens {
		if !stopwords[t] {
			result = append(result, t)
		}
	}
	return result
}

// deduplicate removes duplicate tokens while preserving order.
func deduplicate(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	var result []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			result = append(result, t)
		}
	}
	return result
}
