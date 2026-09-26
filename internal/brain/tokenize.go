package brain

import "conduit/internal/ftsquery"

// TokenizeQuery processes a recall query into clean search terms: lowercase,
// split on whitespace and the delimiters / - _ , , strip stopwords (falling
// back to the raw tokens when everything is a stopword), deduplicate.
//
// The implementation lives in internal/ftsquery so the FTS5 MATCH builders
// share exactly the same tokenization (conduit-179p, conduit-31jg.31).
func TokenizeQuery(query string) []string {
	return ftsquery.Tokenize(query)
}
