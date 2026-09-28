package tools

import (
	"log"
	"sort"

	"conduit/internal/tools/types"
)

// optionalFactories holds factories for optional tools registered via init().
// Tools register here using build tags; the registry instantiates them at startup.
var optionalFactories = make(map[string]types.OptionalToolFactory)

// RegisterOptional registers an optional tool factory.
// Called from init() functions in optional tool packages with build tags.
// The factory will be invoked during SetServices() to instantiate the tool.
func RegisterOptional(name string, factory types.OptionalToolFactory) {
	optionalFactories[name] = factory
}

// registerOptionalTools instantiates optional tools from registered factories.
// Factories are registered via RegisterOptional() from init() functions with build tags.
func (r *Registry) registerOptionalTools() {
	for name, factory := range optionalFactories {
		// Factories run without r.mu held: they may call back into the
		// registry (e.g. HasTool / GetRegistryAsExecutor).
		tool, err := factory(r.services, r.services.ConfigMgr)
		if err != nil {
			log.Printf("Failed to create optional tool %s: %v", name, err)
			continue
		}
		if tool == nil {
			// Tool is compiled in but disabled via config - this is expected
			log.Printf("Optional tool %s: compiled but disabled via config", name)
			continue
		}
		r.mu.Lock()
		r.tools[tool.Name()] = tool
		r.mu.Unlock()
		log.Printf("Registered optional tool: %s", tool.Name())
	}
}

// warnMismatchedOptionalTools logs warnings for tools enabled in config but not compiled.
func (r *Registry) warnMismatchedOptionalTools() {
	if r.services.ConfigMgr == nil {
		return
	}
	cfg := r.services.ConfigMgr

	// Check each optional tool config against registered tools
	checks := []struct {
		name    string
		enabled bool
	}{
		{"Datadog", cfg.Datadog.Enabled},
		{"DatadogMonitor", cfg.Datadog.Enabled},
		{"Kubernetes", cfg.Kubernetes.Enabled},
		{"PagerDuty", cfg.PagerDuty.Enabled},
		{"Ssh", cfg.RemoteSSH.Enabled}, // SSHTool.Name(); "SSH" never matched (conduit-enf0)
		// MQTT is checked via service, not config
		// UniFi has no config enable flag
	}

	for _, check := range checks {
		if check.enabled && !r.HasTool(check.name) {
			log.Printf("Warning: %s is enabled in config but not compiled (missing build tag)", check.name)
		}
	}
}

// CloseTools releases resources held by registered tools that implement
// Close() or Close() error (e.g. the SSH tool's connection pool, persistent
// sessions and tunnels). The gateway calls it once during shutdown, after
// in-flight approved actions have finished (conduit-enf0). Tools are closed
// outside r.mu; errors are logged, not returned.
func (r *Registry) CloseTools() {
	r.mu.RLock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]types.Tool, 0, len(names))
	for _, name := range names {
		list = append(list, r.tools[name])
	}
	r.mu.RUnlock()

	for _, tool := range list {
		switch c := tool.(type) {
		case interface{ Close() error }:
			if err := c.Close(); err != nil {
				log.Printf("Failed to close tool %s: %v", tool.Name(), err)
			}
		case interface{ Close() }:
			c.Close()
		}
	}
}
