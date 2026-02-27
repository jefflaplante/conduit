package plugins

import (
	"fmt"
	"sync"

	"conduit/internal/tools/types"
)

// pluginEntry holds a registered plugin along with its runtime state.
type pluginEntry struct {
	plugin  Plugin
	enabled bool
}

// PluginRegistry manages the lifecycle of plugins: registration,
// initialization, enabling/disabling, and shutdown.
// All methods are safe for concurrent use.
type PluginRegistry struct {
	mu      sync.RWMutex
	plugins map[string]*pluginEntry
	ctx     PluginContext
}

// NewPluginRegistry creates a new plugin registry with the given context.
// The context is passed to each plugin during registration.
func NewPluginRegistry(ctx PluginContext) *PluginRegistry {
	return &PluginRegistry{
		plugins: make(map[string]*pluginEntry),
		ctx:     ctx,
	}
}

// Register adds a plugin to the registry, validates it, and calls
// Initialize on the plugin. Returns an error if the plugin is invalid,
// already registered, or fails initialization.
func (r *PluginRegistry) Register(plugin Plugin) error {
	if plugin == nil {
		return fmt.Errorf("plugin must not be nil")
	}

	meta := plugin.Metadata()

	// Validate metadata
	if errs := meta.Validate(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return fmt.Errorf("invalid plugin metadata: %s", msgs[0])
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.plugins[meta.Name]; exists {
		return fmt.Errorf("plugin %q is already registered", meta.Name)
	}

	// Check dependencies
	for _, dep := range meta.Dependencies {
		if _, ok := r.plugins[dep]; !ok {
			return fmt.Errorf("plugin %q requires dependency %q which is not registered", meta.Name, dep)
		}
	}

	// Initialize the plugin
	if err := plugin.Initialize(r.ctx); err != nil {
		return fmt.Errorf("failed to initialize plugin %q: %w", meta.Name, err)
	}

	r.plugins[meta.Name] = &pluginEntry{
		plugin:  plugin,
		enabled: true,
	}

	return nil
}

// Unregister shuts down and removes a plugin from the registry.
// Returns an error if the plugin is not found or shutdown fails.
func (r *PluginRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %q is not registered", name)
	}

	// Check if other plugins depend on this one
	for pName, pe := range r.plugins {
		if pName == name {
			continue
		}
		for _, dep := range pe.plugin.Metadata().Dependencies {
			if dep == name {
				return fmt.Errorf("cannot unregister plugin %q: plugin %q depends on it", name, pName)
			}
		}
	}

	if err := entry.plugin.Shutdown(); err != nil {
		return fmt.Errorf("failed to shutdown plugin %q: %w", name, err)
	}

	delete(r.plugins, name)
	return nil
}

// Get retrieves a registered plugin by name.
// Returns the plugin and true if found, or nil and false otherwise.
func (r *PluginRegistry) Get(name string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.plugins[name]
	if !exists {
		return nil, false
	}
	return entry.plugin, true
}

// ListAll returns metadata for all registered plugins.
func (r *PluginRegistry) ListAll() []PluginMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]PluginMetadata, 0, len(r.plugins))
	for _, entry := range r.plugins {
		result = append(result, entry.plugin.Metadata())
	}
	return result
}

// ListByCapability returns metadata for plugins that provide the given capability.
func (r *PluginRegistry) ListByCapability(cap string) []PluginMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []PluginMetadata
	for _, entry := range r.plugins {
		meta := entry.plugin.Metadata()
		for _, c := range meta.Capabilities {
			if c == cap {
				result = append(result, meta)
				break
			}
		}
	}
	return result
}

// Enable activates a plugin so its tools are available.
// Returns an error if the plugin is not registered.
func (r *PluginRegistry) Enable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	entry.enabled = true
	return nil
}

// Disable deactivates a plugin without removing it from the registry.
// Disabled plugins remain registered but their tools are excluded
// from GetAllTools. Returns an error if the plugin is not registered.
func (r *PluginRegistry) Disable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, exists := r.plugins[name]
	if !exists {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	entry.enabled = false
	return nil
}

// IsEnabled returns whether a plugin is currently enabled.
// Returns false if the plugin is not registered.
func (r *PluginRegistry) IsEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.plugins[name]
	if !exists {
		return false
	}
	return entry.enabled
}

// GetAllTools returns all tools from all enabled plugins.
func (r *PluginRegistry) GetAllTools() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var allTools []types.Tool
	for _, entry := range r.plugins {
		if entry.enabled {
			allTools = append(allTools, entry.plugin.Tools()...)
		}
	}
	return allTools
}

// ShutdownAll shuts down all registered plugins and clears the registry.
// Errors from individual plugin shutdowns are collected and returned
// as a combined error.
func (r *PluginRegistry) ShutdownAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for name, entry := range r.plugins {
		if err := entry.plugin.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("shutdown plugin %q: %w", name, err))
		}
	}

	r.plugins = make(map[string]*pluginEntry)

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}
	return nil
}

// Count returns the number of registered plugins.
func (r *PluginRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.plugins)
}
