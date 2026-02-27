package search_test

import (
	"context"
	"fmt"
	"os"

	"conduit/internal/search"
)

// Example shows how to use the Brave search fallback implementation
func ExampleBraveDirectSearch() {
	// Create Brave search configuration
	braveConfig := search.BraveSearchConfig{
		APIKey:         os.Getenv("BRAVE_API_KEY"), // Required
		DefaultResults: 5,
		MaxResults:     10,
	}

	// Create cache configuration
	cacheConfig := search.CacheConfig{
		TTLMinutes: 15,
		Enabled:    true,
	}

	// Create the Brave search strategy
	braveSearch, err := search.NewBraveDirectSearch(braveConfig, cacheConfig)
	if err != nil {
		fmt.Printf("Error creating Brave search: %v\n", err)
		return
	}

	// Check if the strategy is available
	if !braveSearch.IsAvailable() {
		fmt.Println("Brave search is not available (missing API key?)")
		return
	}

	// Create search parameters
	params := search.SearchParameters{
		Query:      "Conduit AI assistant platform",
		Count:      5,
		Country:    "US",
		SearchLang: "en",
		Freshness:  "pw", // Past week
	}

	// Perform the search
	ctx := context.Background()
	response, err := braveSearch.Search(ctx, params)
	if err != nil {
		fmt.Printf("Search error: %v\n", err)
		return
	}

	// Display results
	fmt.Printf("Found %d results for '%s' (provider: %s, cached: %v)\n",
		response.Total, response.Query, response.Provider, response.Cached)

	for i, result := range response.Results {
		fmt.Printf("%d. %s\n   %s\n", i+1, result.Title, result.URL)
		if result.Description != "" {
			fmt.Printf("   %s\n", result.Description)
		}
		fmt.Println()
	}

	// Show capabilities
	caps := braveSearch.GetCapabilities()
	fmt.Printf("Capabilities: Country=%t, Language=%t, Freshness=%t, MaxResults=%d\n",
		caps.SupportsCountry, caps.SupportsLanguage, caps.SupportsFreshness, caps.MaxResults)
}

// Example showing configuration loading from environment
func ExampleLoadSearchConfigFromEnv() {
	// Load configuration from environment variables
	config := search.LoadSearchConfigFromEnv()

	// Validate the configuration
	if err := config.Validate(); err != nil {
		fmt.Printf("Invalid configuration: %v\n", err)
		return
	}

	// Check if Brave is enabled
	if config.IsBraveEnabled() {
		fmt.Println("Brave search is enabled and configured")

		// Get Brave-specific config
		braveConfig := config.GetBraveConfig()
		cacheConfig := config.GetCacheConfig()

		// Create the search strategy
		braveSearch, err := search.NewBraveDirectSearch(braveConfig, cacheConfig)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}

		fmt.Printf("Brave search initialized successfully (%s)\n", braveSearch.Name())
	} else {
		fmt.Println("Brave search is not enabled or configured")
	}
}

// Example showing error handling
func ExampleBraveDirectSearch_errorHandling() {
	// Try to create with missing API key
	braveConfig := search.BraveSearchConfig{
		// APIKey deliberately omitted
	}
	cacheConfig := search.DefaultCacheConfig()

	_, err := search.NewBraveDirectSearch(braveConfig, cacheConfig)
	if err != nil {
		fmt.Printf("Expected error: %v\n", err)
	}

	// Example of search parameter validation
	params := search.SearchParameters{
		// Query deliberately omitted - will cause validation error
		Count: 5,
	}

	if err := params.Validate(); err != nil {
		fmt.Printf("Parameter validation error: %v\n", err)
	}
}
