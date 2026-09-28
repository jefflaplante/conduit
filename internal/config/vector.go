package config

import (
	"path/filepath"
	"strings"
)

// VectorConfig holds configuration for the optional vector/semantic search service.
type VectorConfig struct {
	Enabled       bool               `json:"enabled"`
	Path          string             `json:"path,omitempty"`           // Path to vector DB file (derived from gateway DB if empty)
	ChunkSize     int                `json:"chunk_size,omitempty"`     // Max tokens per chunk (default 500)
	EmbedDims     int                `json:"embed_dims,omitempty"`     // Embedding dimensions (0 = use embedder default: 768 Ollama, 1536 OpenAI)
	EmbedProvider string             `json:"embed_provider,omitempty"` // "" or "auto" (default), "ollama", "openai"
	EmbedTimeout  int                `json:"embed_timeout,omitempty"`  // Per-file embedding timeout in seconds (default 300)
	EmbedPacing   int                `json:"embed_pacing,omitempty"`   // Delay between embedding calls in seconds (default 2)
	FTSWeight     float64            `json:"fts_weight,omitempty"`     // RRF weight for FTS5 keyword results (default 1.0)
	VectorWeight  float64            `json:"vector_weight,omitempty"`  // RRF weight for vector semantic results (default 1.5)
	OpenAI        *OpenAIEmbedConfig `json:"openai,omitempty"`
	Ollama        *OllamaEmbedConfig `json:"ollama,omitempty"`
}

// GetFTSWeight returns the configured FTS5 RRF weight, defaulting to 1.0.
func (v *VectorConfig) GetFTSWeight() float64 {
	if v.FTSWeight == 0 {
		return 1.0
	}
	return v.FTSWeight
}

// GetVectorWeight returns the configured vector RRF weight, defaulting to 1.5.
func (v *VectorConfig) GetVectorWeight() float64 {
	if v.VectorWeight == 0 {
		return 1.5
	}
	return v.VectorWeight
}

// OpenAIEmbedConfig holds configuration for OpenAI embedding provider.
type OpenAIEmbedConfig struct {
	APIKey string `json:"api_key,omitempty" cfg:"env"` // Supports ${ENV_VAR} expansion
	Model  string `json:"model,omitempty"`             // Default: "text-embedding-3-small"
}

// OllamaEmbedConfig holds configuration for the Ollama embedding provider.
type OllamaEmbedConfig struct {
	Host  string `json:"host,omitempty"`  // Default: OLLAMA_HOST env or http://localhost:11434
	Model string `json:"model,omitempty"` // Default: nomic-embed-text
}

// DeriveVectorDBPath returns a vector DB path derived from the gateway DB path.
// For example, "gateway.db" becomes "gateway.vector.db".
func DeriveVectorDBPath(gatewayDBPath string) string {
	ext := filepath.Ext(gatewayDBPath)
	base := strings.TrimSuffix(gatewayDBPath, ext)
	if ext == "" {
		ext = ".db"
	}
	return base + ".vector" + ext
}
