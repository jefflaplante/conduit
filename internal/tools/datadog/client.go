package datadog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"conduit/internal/config"

	"golang.org/x/time/rate"
)

// Client is an HTTP client for the Datadog API with rate limiting.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	apiKey      string
	appKey      string
	rateLimiter *rate.Limiter
}

// NewClient creates a new Datadog API client from the given configuration.
func NewClient(cfg config.DatadogConfig) *Client {
	rps := cfg.RateLimitRPS
	if rps <= 0 {
		rps = 5.0
	}

	return &Client{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     cfg.BaseURL(),
		apiKey:      cfg.APIKey,
		appKey:      cfg.AppKey,
		rateLimiter: rate.NewLimiter(rate.Limit(rps), int(rps)),
	}
}

// Do executes an HTTP request against the Datadog API with rate limiting
// and authentication headers.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait: %w", err)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("DD-API-KEY", c.apiKey)
	req.Header.Set("DD-APPLICATION-KEY", c.appKey)
	req.Header.Set("Content-Type", "application/json")

	return c.httpClient.Do(req)
}
