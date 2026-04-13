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
var _ embedder.Embedder = (*OllamaEmbedder)(nil)

const (
	defaultOllamaHost  = "http://localhost:11434"
	defaultOllamaModel = "nomic-embed-text"
	defaultOllamaDims  = 768
)

// OllamaEmbedder implements embedder.Embedder using the Ollama /api/embed endpoint.
type OllamaEmbedder struct {
	host       string
	model      string
	dimensions int
	client     *http.Client
}

// NewOllamaEmbedder creates a new Ollama embedding provider.
// host can be empty (defaults to "http://localhost:11434").
// model can be empty (defaults to "nomic-embed-text").
// dims can be 0 (defaults to 768 for nomic-embed-text).
func NewOllamaEmbedder(host, model string, dims int) *OllamaEmbedder {
	if host == "" {
		host = defaultOllamaHost
	}
	if model == "" {
		model = defaultOllamaModel
	}
	if dims <= 0 {
		dims = defaultOllamaDims
	}
	return &OllamaEmbedder{
		host:       host,
		model:      model,
		dimensions: dims,
		client:     &http.Client{Timeout: 60 * time.Second},
	}
}

func (o *OllamaEmbedder) Name() string    { return "ollama:" + o.model }
func (o *OllamaEmbedder) Dimensions() int { return o.dimensions }

// Embed sends texts to the Ollama /api/embed endpoint and returns vectors.
func (o *OllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := ollamaEmbedRequest{
		Model: o.model,
		Input: texts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: marshal request: %w", err)
	}

	var resp ollamaEmbedResponse
	var lastErr error
	url := o.host + "/api/embed"

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ollama embed: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		httpResp, err := o.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("ollama embed: request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("ollama embed: read response: %w", err)
			continue
		}

		if httpResp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("ollama embed: rate limited (429)")
			continue
		}

		if httpResp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("ollama embed: API error %d: %s", httpResp.StatusCode, string(respBody))
			if httpResp.StatusCode >= 400 && httpResp.StatusCode < 500 && httpResp.StatusCode != 429 {
				return nil, lastErr
			}
			continue
		}

		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("ollama embed: unmarshal response: %w", err)
		}

		lastErr = nil
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return resp.Embeddings, nil
}

// Ollama /api/embed types

type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}
