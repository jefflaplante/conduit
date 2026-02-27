package search

import (
	"context"
	"fmt"
	"time"
)

// SearchStrategy defines the interface for different search implementations
type SearchStrategy interface {
	// Name returns the strategy identifier (e.g., "brave", "anthropic")
	Name() string

	// Search performs a search and returns results
	Search(ctx context.Context, params SearchParameters) (*SearchResponse, error)

	// IsAvailable checks if the strategy is currently available
	IsAvailable() bool

	// GetCapabilities returns the search capabilities of this strategy
	GetCapabilities() SearchCapabilities
}

// SearchParameters contains all possible search parameters
type SearchParameters struct {
	Query      string `json:"query" validate:"required"`
	Count      int    `json:"count,omitempty"`
	Country    string `json:"country,omitempty"`
	SearchLang string `json:"search_lang,omitempty"`
	UILang     string `json:"ui_lang,omitempty"`
	Freshness  string `json:"freshness,omitempty"` // pd, pw, pm, py
}

// SearchParams is an alias for backward compatibility
type SearchParams = SearchParameters

// SearchResult represents a single search result
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Published   string `json:"published,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
}

// SearchResponse contains search results and metadata
type SearchResponse struct {
	Results   []SearchResult `json:"results"`
	Query     string         `json:"query"`
	Total     int            `json:"total"`
	Provider  string         `json:"provider"`
	Cached    bool           `json:"cached,omitempty"`
	Timestamp time.Time      `json:"timestamp"`

	// Provider-specific metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SearchError represents a search-specific error with additional context
type SearchError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type"`
	Err     error  `json:"-"`
}

func (e *SearchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Type, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *SearchError) Unwrap() error {
	return e.Err
}

// SearchCapabilities describes what a search strategy can do
type SearchCapabilities struct {
	SupportsCountry   bool `json:"supports_country"`
	SupportsLanguage  bool `json:"supports_language"`
	SupportsFreshness bool `json:"supports_freshness"`
	MaxResults        int  `json:"max_results"`
	DefaultResults    int  `json:"default_results"`
	HasCaching        bool `json:"has_caching"`
	RequiresAPIKey    bool `json:"requires_api_key"`
}

// DefaultSearchParams returns default search parameters
func DefaultSearchParams() SearchParams {
	return SearchParams{
		Count:   5,
		Country: "US",
	}
}

// Validate checks if search parameters are valid
func (p *SearchParameters) Validate() error {
	if p.Query == "" {
		return ErrInvalidQuery
	}

	if p.Count < 1 || p.Count > 10 {
		p.Count = 5 // Default to 5 results
	}

	if p.Freshness != "" {
		switch p.Freshness {
		case "pd", "pw", "pm", "py":
			// Valid freshness values
		default:
			return ErrInvalidFreshness
		}
	}

	return nil
}
