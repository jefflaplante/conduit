package pagerduty

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"conduit/internal/config"

	"golang.org/x/time/rate"
)

// Client is an HTTP client for the PagerDuty REST API.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	apiToken    string
	rateLimiter *rate.Limiter
}

// NewClient creates a new PagerDuty API client from the given configuration.
func NewClient(cfg config.PagerDutyConfig) *Client {
	rps := cfg.RateLimitRPS
	if rps <= 0 {
		rps = 5.0
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:     cfg.EffectiveBaseURL(),
		apiToken:    cfg.APIToken,
		rateLimiter: rate.NewLimiter(rate.Limit(rps), 1),
	}
}

// Do executes an HTTP request against the PagerDuty API.
// It applies rate limiting, sets required headers, and returns the raw response.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("pagerduty rate limiter: %w", err)
	}

	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("pagerduty: create request: %w", err)
	}

	req.Header.Set("Authorization", "Token token="+c.apiToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pagerduty: request failed: %w", err)
	}

	return resp, nil
}
