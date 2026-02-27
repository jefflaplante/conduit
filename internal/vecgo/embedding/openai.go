package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/jefflaplante/vecgo/embedder"
)

// Compile-time interface check.
var _ embedder.Embedder = (*OpenAIEmbedder)(nil)

const (
	defaultOpenAIModel = "text-embedding-3-small"
	defaultDimensions  = 1536
	openAIEmbedURL     = "https://api.openai.com/v1/embeddings"
	maxRetries         = 3
)

// OpenAIEmbedder implements embedder.Embedder using the OpenAI embeddings API.
type OpenAIEmbedder struct {
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
	baseURL    string // configurable for testing; defaults to openAIEmbedURL
}

// NewOpenAIEmbedder creates a new OpenAI embedding provider.
// model can be empty (defaults to "text-embedding-3-small").
// dims can be 0 (defaults to 1536).
func NewOpenAIEmbedder(apiKey, model string, dims int) *OpenAIEmbedder {
	if model == "" {
		model = defaultOpenAIModel
	}
	if dims <= 0 {
		dims = defaultDimensions
	}
	return &OpenAIEmbedder{
		apiKey:     apiKey,
		model:      model,
		dimensions: dims,
		client:     &http.Client{Timeout: 30 * time.Second},
		baseURL:    openAIEmbedURL,
	}
}

func (o *OpenAIEmbedder) Name() string    { return "openai:" + o.model }
func (o *OpenAIEmbedder) Dimensions() int { return o.dimensions }

// Embed sends texts to the OpenAI embeddings API and returns vectors.
func (o *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := openAIEmbedRequest{
		Model:      o.model,
		Input:      texts,
		Dimensions: o.dimensions,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}

	var resp openAIEmbedResponse
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai embed: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+o.apiKey)

		httpResp, err := o.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("openai embed: request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("openai embed: read response: %w", err)
			continue
		}

		if httpResp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("openai embed: rate limited (429)")
			continue
		}

		if httpResp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("openai embed: API error %d: %s", httpResp.StatusCode, string(respBody))
			// Don't retry non-retryable errors
			if httpResp.StatusCode >= 400 && httpResp.StatusCode < 500 && httpResp.StatusCode != 429 {
				return nil, lastErr
			}
			continue
		}

		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("openai embed: unmarshal response: %w", err)
		}

		// Success
		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	// Convert response to [][]float32
	vectors := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		vectors[i] = d.Embedding
	}

	return vectors, nil
}

// OpenAI API types

type openAIEmbedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedData `json:"data"`
}

type openAIEmbedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}
