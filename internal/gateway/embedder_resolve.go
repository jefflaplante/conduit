package gateway

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jefflaplante/vecgo/embedder"

	"conduit/internal/config"
	"conduit/internal/vecgo/embedding"
)

// Embedding provider defaults for auto-detection.
const (
	defaultOllamaEmbedHost  = "http://localhost:11434"
	defaultOllamaEmbedModel = "nomic-embed-text"
)

// shouldAutoEnableVecgo returns true when vecgo should auto-enable despite
// vector.enabled not being set in config. This fires when Ollama is reachable
// at localhost or OLLAMA_HOST is set, or OPENAI_API_KEY is present.
func shouldAutoEnableVecgo(cfg config.VectorConfig, logger *slog.Logger) bool {
	// If user explicitly configured a provider, auto-enable.
	if cfg.EmbedProvider != "" && cfg.EmbedProvider != "auto" {
		return true
	}

	// Check OLLAMA_HOST env.
	if os.Getenv("OLLAMA_HOST") != "" {
		return true
	}

	// Check OPENAI_API_KEY env.
	if os.Getenv("OPENAI_API_KEY") != "" {
		return true
	}

	// Probe default Ollama at localhost.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(defaultOllamaEmbedHost + "/api/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		logger.Info("auto-detected Ollama at localhost, enabling vector search")
		return true
	}
	return false
}

// resolveEmbedder determines the embedding provider based on config and environment.
// Returns the embedder and a provider name for logging. A nil embedder means vecgo should be disabled.
func resolveEmbedder(cfg config.VectorConfig, logger *slog.Logger) (embedder.Embedder, string) {
	switch cfg.EmbedProvider {
	case "openai":
		apiKey := ""
		model := ""
		if cfg.OpenAI != nil {
			apiKey = cfg.OpenAI.APIKey
			model = cfg.OpenAI.Model
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
		if apiKey == "" {
			logger.Error("embed_provider=openai but no API key configured (set openai.api_key or OPENAI_API_KEY)")
			return nil, "openai-no-key"
		}
		return embedding.NewOpenAIEmbedder(apiKey, model, cfg.EmbedDims), "openai"

	case "ollama":
		host, model := ollamaConfigValues(cfg)
		logger.Info("using Ollama embeddings", "host", host, "model", model)
		return embedding.NewOllamaEmbedder(host, model, cfg.EmbedDims), "ollama"

	case "tfidf":
		logger.Warn("embed_provider=tfidf is deprecated — TF-IDF produces poor semantic search results; vecgo disabled")
		return nil, "tfidf-deprecated"

	default: // "" or "auto"
		// Auto-detect: OLLAMA_HOST env, then localhost probe, then OPENAI_API_KEY.
		host, model := ollamaConfigValues(cfg)
		if os.Getenv("OLLAMA_HOST") != "" {
			logger.Info("auto-detected Ollama from OLLAMA_HOST", "host", host, "model", model)
			return embedding.NewOllamaEmbedder(host, model, cfg.EmbedDims), "ollama"
		}
		// Probe default localhost Ollama.
		client := &http.Client{Timeout: 2 * time.Second}
		if resp, err := client.Get(host + "/api/version"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				logger.Info("auto-detected Ollama at localhost", "host", host, "model", model)
				return embedding.NewOllamaEmbedder(host, model, cfg.EmbedDims), "ollama"
			}
		}
		if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
			openaiModel := ""
			if cfg.OpenAI != nil {
				openaiModel = cfg.OpenAI.Model
			}
			logger.Info("auto-detected OpenAI from OPENAI_API_KEY")
			return embedding.NewOpenAIEmbedder(apiKey, openaiModel, cfg.EmbedDims), "openai"
		}
		return nil, "none"
	}
}

// ollamaConfigValues returns resolved host and model for Ollama from config + env.
func ollamaConfigValues(cfg config.VectorConfig) (host, model string) {
	host = defaultOllamaEmbedHost
	model = defaultOllamaEmbedModel
	if cfg.Ollama != nil {
		if cfg.Ollama.Host != "" {
			host = cfg.Ollama.Host
		}
		if cfg.Ollama.Model != "" {
			model = cfg.Ollama.Model
		}
	}
	if envHost := os.Getenv("OLLAMA_HOST"); envHost != "" {
		host = envHost
	}
	return host, model
}
