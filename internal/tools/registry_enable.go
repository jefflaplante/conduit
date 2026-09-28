package tools

import (
	"strings"

	"conduit/internal/sandbox"
)

// normalizeToolName converts a tool name to a canonical form for case-insensitive matching.
// Handles both PascalCase (SessionsSpawn) and snake_case (sessions_spawn) inputs.
func normalizeToolName(name string) string {
	// Remove underscores and convert to lowercase
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

// isToolEnabled checks if a tool is enabled (case-insensitive, underscore-insensitive).
func (r *Registry) isToolEnabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isToolEnabledLocked(name)
}

// isToolEnabledLocked is isToolEnabled for callers already holding r.mu.
func (r *Registry) isToolEnabledLocked(name string) bool {
	return r.enabledTools[normalizeToolName(name)]
}

// getEnabledToolNames returns the names of all enabled tools.
func (r *Registry) getEnabledToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.tools {
		if r.isToolEnabledLocked(name) {
			names = append(names, name)
		}
	}
	return names
}

// isPathAllowed checks if a file path is allowed within the sandbox.
// conduit-31jg.6: delegates to the shared symlink-aware sandbox resolver.
func (r *Registry) isPathAllowed(path string) bool {
	_, ok := r.sandboxResolve(path)
	return ok
}

// sandboxResolve returns the canonical (symlink-resolved) path when path is
// inside the sandbox. Callers should do their I/O on the returned path so the
// checked path and the opened path are the same.
// conduit-31jg.6
func (r *Registry) sandboxResolve(path string) (string, bool) {
	real, err := sandbox.FromConfig(r.sandboxCfg).Resolve(path)
	if err != nil {
		return "", false
	}
	return real, true
}
