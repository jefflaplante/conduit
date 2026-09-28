package sessions

import (
	"encoding/json"
	"fmt"
	"strings"

	"conduit/internal/ftsquery"
)

// SearchMessagesResult represents a search result from session messages
type SearchMessagesResult struct {
	Message    Message `json:"message"`
	SessionKey string  `json:"session_key"`
	MatchScore float64 `json:"match_score"`
}

// SearchMessages searches for messages across all sessions containing the query keywords.
// Uses FTS5 full-text search for efficient querying with BM25 ranking.
func (s *Store) SearchMessages(query string, limit int) ([]SearchMessagesResult, error) {
	if limit <= 0 {
		limit = 50
	}

	// Build FTS5 query: escape special characters and join with OR for flexible matching
	ftsQuery := s.buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	// Try FTS5 search first (uses messages_fts virtual table with BM25 ranking)
	rows, err := s.db.Query(`
		SELECT m.id, m.session_key, m.role, m.content, m.timestamp, m.metadata, s.key, fts.rank
		FROM messages_fts fts
		JOIN messages m ON fts.message_id = m.id
		JOIN sessions s ON m.session_key = s.key
		WHERE messages_fts MATCH ?
		ORDER BY fts.rank
		LIMIT ?
	`, ftsquery.Column("content", ftsQuery), limit) // conduit-31jg.31: scope every OR'd phrase

	if err != nil {
		// Fall back to LIKE search if FTS fails (e.g., table doesn't exist)
		return s.searchMessagesLIKE(query, limit)
	}
	defer rows.Close()

	var results []SearchMessagesResult

	for rows.Next() {
		var message Message
		var metadataJSON string
		var sessionKey string
		var rank float64

		err := rows.Scan(
			&message.ID,
			&message.SessionKey,
			&message.Role,
			&message.Content,
			&message.Timestamp,
			&metadataJSON,
			&sessionKey,
			&rank,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Parse metadata JSON
		if err := json.Unmarshal([]byte(metadataJSON), &message.Metadata); err != nil {
			message.Metadata = make(map[string]string)
		}

		// Convert BM25 rank to 0-1 score (rank is negative, more negative = better match)
		score := -rank / 20.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}

		results = append(results, SearchMessagesResult{
			Message:    message,
			SessionKey: sessionKey,
			MatchScore: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// buildFTSQuery converts a user query into FTS5 query syntax.
// Escapes special characters and handles multi-word queries.
func (s *Store) buildFTSQuery(query string) string {
	// conduit-31jg.31: shared quoted-phrase builder (the old partial escaper
	// left . / @ % # : unquoted -> "fts5: syntax error" -> silent LIKE fallback).
	return ftsquery.Build(query)
}

// likeEscaper makes %, _ and the escape char literal in a LIKE ... ESCAPE '\'
// pattern. conduit-31jg.73.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// searchMessagesLIKE is a fallback search using LIKE when FTS5 is unavailable.
func (s *Store) searchMessagesLIKE(query string, limit int) ([]SearchMessagesResult, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.session_key, m.role, m.content, m.timestamp, m.metadata, s.key
		FROM messages m
		JOIN sessions s ON m.session_key = s.key
		WHERE LOWER(m.content) LIKE LOWER(?) ESCAPE '\'
		ORDER BY m.timestamp DESC, m.rowid DESC
		LIMIT ?
	`, "%"+likeEscaper.Replace(query)+"%", limit)

	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	defer rows.Close()

	var results []SearchMessagesResult

	for rows.Next() {
		var message Message
		var metadataJSON string
		var sessionKey string

		err := rows.Scan(
			&message.ID,
			&message.SessionKey,
			&message.Role,
			&message.Content,
			&message.Timestamp,
			&metadataJSON,
			&sessionKey,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Parse metadata JSON
		if err := json.Unmarshal([]byte(metadataJSON), &message.Metadata); err != nil {
			message.Metadata = make(map[string]string)
		}

		// Calculate basic match score based on keyword frequency
		score := s.calculateMessageMatchScore(message.Content, query)

		results = append(results, SearchMessagesResult{
			Message:    message,
			SessionKey: sessionKey,
			MatchScore: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// calculateMessageMatchScore calculates a simple match score for message content
func (s *Store) calculateMessageMatchScore(content, query string) float64 {
	if query == "" {
		return 0.0
	}

	contentLower := strings.ToLower(content)
	queryLower := strings.ToLower(query)

	// Split query into keywords
	keywords := strings.Fields(queryLower)
	if len(keywords) == 0 {
		return 0.0
	}

	matches := 0
	for _, keyword := range keywords {
		if strings.Contains(contentLower, keyword) {
			matches++
		}
	}

	return float64(matches) / float64(len(keywords))
}
