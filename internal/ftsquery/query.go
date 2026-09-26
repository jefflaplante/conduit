// Package ftsquery turns free-text user queries into safe SQLite FTS5 MATCH
// expressions, and holds the shared recall tokenizer (conduit-179p).
//
// conduit-31jg.31: every FTS5 MATCH site (fts.Searcher, searchdb brain/beads
// indexers, sessions.Store) used to run its own denylist "cleaner" that left
// characters like . / @ % # ' unquoted, so ordinary queries such as
// "jeff.birthday", "e@x.com" or "don't" failed with "fts5: syntax error", and
// bead IDs like "conduit-3dru" were mangled into "conduit3dru" (no match).
// Build instead quotes every term as an FTS5 string ("..." with embedded "
// doubled), which FTS5 always parses as a phrase: operators, column filters,
// NEAR, ^, * and punctuation are all inert, and the table's tokenizer
// (unicode61) splits the phrase exactly like it split the indexed text.
package ftsquery

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTerms caps the number of OR'd phrases in one MATCH expression.
const MaxTerms = 50

// MaxTermBytes caps the length of a single term.
const MaxTermBytes = 200

// Terms returns the search terms for query, in order, deduplicated:
//
//   - the conduit-179p tokens (Tokenize: lowercase, delimiter splitting on
//     / - _ , , stopword stripping with all-stopword fallback);
//   - plus, for every whitespace-separated word that Tokenize split on a
//     delimiter, the whole compound word, so "conduit-3dru" also searches the
//     exact phrase "conduit 3dru" and ranks the precise hit first.
//
// Terms with no letter or digit (they would tokenize to an empty phrase) are
// dropped. Control characters are stripped and terms are capped at
// MaxTermBytes; at most MaxTerms terms are returned.
func Terms(query string) []string {
	query = stripControlChars(query)
	var out []string
	seen := make(map[string]bool)
	add := func(t string) {
		t = truncate(t, MaxTermBytes)
		if t == "" || seen[t] || !hasAlnum(t) || len(out) >= MaxTerms {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if len(splitOnDelimiters(w)) > 1 {
			add(strings.TrimFunc(w, isNotAlnum))
		}
	}
	for _, t := range Tokenize(query) {
		add(t)
	}
	return out
}

// Quote returns term as an FTS5 string literal: wrapped in double quotes
// with any embedded double quote doubled. FTS5 treats it as a phrase.
func Quote(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

// Build returns an FTS5 MATCH expression for a free-text query: every term
// from Terms quoted as a phrase and OR-joined (FTS5 bm25 rank then orders
// documents that match more / rarer terms first). Returns "" when the query
// has nothing searchable; callers should skip the MATCH in that case.
func Build(query string) string {
	terms := Terms(query)
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = Quote(t)
	}
	return strings.Join(quoted, " OR ")
}

// Column restricts a Build expression to a single column: `col : (expr)`.
// (Prefixing "col:" to an OR list would only scope the first phrase.)
// col must be a trusted identifier, never user input. Empty expr stays empty.
func Column(col, expr string) string {
	if expr == "" {
		return ""
	}
	return col + " : (" + expr + ")"
}

func hasAlnum(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func isNotAlnum(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// stripControlChars removes NUL and other ASCII control characters except
// tab/newline/CR (which strings.Fields treats as separators).
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, ch := range s {
		if ch < 32 && ch != '\t' && ch != '\n' && ch != '\r' {
			continue
		}
		b.WriteRune(ch)
	}
	return b.String()
}
