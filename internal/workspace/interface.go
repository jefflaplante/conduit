package workspace

import (
	"context"
)

// ContextProvider defines the interface for workspace context loading
type ContextProvider interface {
	// LoadContext loads workspace context files filtered by security rules
	LoadContext(ctx context.Context, securityCtx SecurityContext) (*ContextBundle, error)

	// GetWorkspaceDir returns the workspace directory path
	GetWorkspaceDir() string

	// InvalidateCache invalidates cached content for a specific file
	InvalidateCache(relativePath string)

	// ClearCache clears all cached content
	ClearCache()

	// GetCacheStats returns cache statistics for monitoring
	GetCacheStats() map[string]interface{}
}

// Manager provides a high-level interface for workspace context management
type Manager struct {
	provider ContextProvider
	config   *Config
}

// NewManager creates a new workspace context manager
func NewManager(config *Config) (*Manager, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	provider := NewWorkspaceContext(config.ContextDir)

	return &Manager{
		provider: provider,
		config:   config,
	}, nil
}

// NewManagerWithDefaults creates a manager with default configuration
func NewManagerWithDefaults(workspaceDir string) (*Manager, error) {
	config := DefaultConfig()
	config.ContextDir = workspaceDir
	return NewManager(config)
}

// LoadContextForSession loads workspace context for a specific session
func (m *Manager) LoadContextForSession(ctx context.Context, sessionType, channelID, userID, sessionID string) (*ContextBundle, error) {
	securityCtx := SecurityContext{
		SessionType: sessionType,
		ChannelID:   channelID,
		UserID:      userID,
		SessionID:   sessionID,
	}

	return m.provider.LoadContext(ctx, securityCtx)
}

// GetProvider returns the underlying context provider
func (m *Manager) GetProvider() ContextProvider {
	return m.provider
}

// GetConfig returns the current configuration
func (m *Manager) GetConfig() *Config {
	return m.config
}

// UpdateConfig updates the manager configuration
func (m *Manager) UpdateConfig(config *Config) error {
	if err := config.Validate(); err != nil {
		return err
	}

	m.config = config

	// If workspace directory changed, need to recreate provider
	if config.ContextDir != m.provider.GetWorkspaceDir() {
		m.provider = NewWorkspaceContext(config.ContextDir)
	}

	return nil
}

// Health returns health status of the workspace context system
func (m *Manager) Health() map[string]interface{} {
	health := map[string]interface{}{
		"workspace_dir": m.provider.GetWorkspaceDir(),
		"config":        m.config,
	}

	// Add cache stats if caching is enabled
	if m.config.Caching.Enabled {
		health["cache_stats"] = m.provider.GetCacheStats()
	}

	return health
}
