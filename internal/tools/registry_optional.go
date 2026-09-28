package tools

import (
	"log"

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

// ListAvailableOptionalTools returns names of optional tools that were compiled in.
// This reflects what factories are registered, not what tools are enabled.
func ListAvailableOptionalTools() []string {
	names := make([]string, 0, len(optionalFactories))
	for name := range optionalFactories {
		names = append(names, name)
	}
	return names
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
		{"SSH", cfg.RemoteSSH.Enabled},
		// MQTT is checked via service, not config
		// UniFi has no config enable flag
	}

	for _, check := range checks {
		if check.enabled && !r.HasTool(check.name) {
			log.Printf("Warning: %s is enabled in config but not compiled (missing build tag)", check.name)
		}
	}
}
