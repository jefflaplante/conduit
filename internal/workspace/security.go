package workspace

import (
	"path/filepath"
	"strings"
)

// SecurityManager handles access control for workspace context files
type SecurityManager struct {
	rules []FileAccess
}

// FileAccess defines access rules for specific files or patterns
type FileAccess struct {
	Pattern     string                     `json:"pattern"`
	Condition   func(SecurityContext) bool `json:"-"`
	Description string                     `json:"description"`
	Enabled     bool                       `json:"enabled"`
}

// NewSecurityManager creates a new security manager with default rules
func NewSecurityManager() *SecurityManager {
	sm := &SecurityManager{}
	sm.initializeDefaultRules()
	return sm
}

// initializeDefaultRules sets up the default file access rules
func (sm *SecurityManager) initializeDefaultRules() {
	sm.rules = []FileAccess{
		{
			Pattern: "MEMORY.md",
			Condition: func(sc SecurityContext) bool {
				return sc.SessionType == "main"
			},
			Description: "MEMORY.md only accessible in main sessions",
			Enabled:     true,
		},
		{
			Pattern: "SOUL.md",
			Condition: func(sc SecurityContext) bool {
				// SOUL.md is accessible in all session types
				return true
			},
			Description: "SOUL.md (personality) accessible in all sessions",
			Enabled:     true,
		},
		{
			Pattern: "USER.md",
			Condition: func(sc SecurityContext) bool {
				// USER.md is accessible in all session types
				return true
			},
			Description: "USER.md (user context) accessible in all sessions",
			Enabled:     true,
		},
		{
			Pattern: "AGENTS.md",
			Condition: func(sc SecurityContext) bool {
				// AGENTS.md is accessible in all session types
				return true
			},
			Description: "AGENTS.md (operational instructions) accessible in all sessions",
			Enabled:     true,
		},
		{
			Pattern: "TOOLS.md",
			Condition: func(sc SecurityContext) bool {
				// TOOLS.md is accessible in all session types
				return true
			},
			Description: "TOOLS.md (tool configuration) accessible in all sessions",
			Enabled:     true,
		},
		{
			Pattern: "HEARTBEAT.md",
			Condition: func(sc SecurityContext) bool {
				// HEARTBEAT.md only accessible in main sessions
				return sc.SessionType == "main"
			},
			Description: "HEARTBEAT.md only accessible in main sessions",
			Enabled:     true,
		},
		{
			Pattern: "memory/*.md",
			Condition: func(sc SecurityContext) bool {
				// Daily memory files accessible in all session types for continuity
				return true
			},
			Description: "Daily memory files accessible in all sessions for continuity",
			Enabled:     true,
		},
	}
}

// IsAccessible checks if a file is accessible given the security context
func (sm *SecurityManager) IsAccessible(filePath string, securityCtx SecurityContext) bool {
	// Find matching rule
	for _, rule := range sm.rules {
		if !rule.Enabled {
			continue
		}

		if sm.matchesPattern(filePath, rule.Pattern) {
			return rule.Condition(securityCtx)
		}
	}

	// Default: allow access for files not covered by specific rules
	// This is a conservative approach - you might want to default to deny
	return true
}

// matchesPattern checks if a file path matches a pattern
func (sm *SecurityManager) matchesPattern(filePath, pattern string) bool {
	// Normalize paths
	filePath = filepath.Clean(filePath)
	pattern = filepath.Clean(pattern)

	// Exact match
	if filePath == pattern {
		return true
	}

	// Wildcard pattern matching
	if strings.Contains(pattern, "*") {
		matched, err := filepath.Match(pattern, filePath)
		if err != nil {
			return false
		}
		return matched
	}

	// Directory pattern (e.g., "memory/" matches files in memory directory)
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(filePath, pattern)
	}

	return false
}

// AddRule adds a custom access rule
func (sm *SecurityManager) AddRule(rule FileAccess) {
	sm.rules = append(sm.rules, rule)
}

// RemoveRule removes a rule by pattern
func (sm *SecurityManager) RemoveRule(pattern string) {
	for i, rule := range sm.rules {
		if rule.Pattern == pattern {
			sm.rules = append(sm.rules[:i], sm.rules[i+1:]...)
			break
		}
	}
}

// EnableRule enables/disables a rule by pattern
func (sm *SecurityManager) EnableRule(pattern string, enabled bool) {
	for i, rule := range sm.rules {
		if rule.Pattern == pattern {
			sm.rules[i].Enabled = enabled
			break
		}
	}
}

// GetRules returns all current access rules
func (sm *SecurityManager) GetRules() []FileAccess {
	rules := make([]FileAccess, len(sm.rules))
	copy(rules, sm.rules)
	return rules
}

// ValidateSecurityContext validates that the security context has required fields
func (sm *SecurityManager) ValidateSecurityContext(sc SecurityContext) error {
	if sc.SessionType == "" {
		return ErrMissingSessionType
	}

	// Validate session type is one of the expected values
	validSessionTypes := map[string]bool{
		"main":     true,
		"shared":   true,
		"isolated": true,
	}

	if !validSessionTypes[sc.SessionType] {
		return ErrInvalidSessionType
	}

	return nil
}

// DetermineSessionType determines session type from context clues
func (sm *SecurityManager) DetermineSessionType(channelID, userID string) string {
	// This is a placeholder implementation
	// Real implementation would need channel/platform-specific logic

	if channelID == "" || channelID == userID {
		// Direct message or no channel context
		return "main"
	}

	// Group/shared channel
	return "shared"
}
