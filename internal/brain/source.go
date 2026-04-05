package brain

import (
	"fmt"
	"strings"
	"time"
)

// Source prefix constants for provenance tracking.
const (
	SourcePrefixFile     = "file"
	SourcePrefixSkill    = "skill"
	SourcePrefixTool     = "tool"
	SourcePrefixUser     = "user"
	SourcePrefixLLM      = "llm"
	SourcePrefixSubAgent = "sub-agent"
)

// knownPrefixes is the set of recognized source prefixes.
var knownPrefixes = map[string]bool{
	SourcePrefixFile:     true,
	SourcePrefixSkill:    true,
	SourcePrefixTool:     true,
	SourcePrefixUser:     true,
	SourcePrefixLLM:      true,
	SourcePrefixSubAgent: true,
}

// ParseSource splits a source string into its prefix and detail parts.
// "file:MEMORY.md#L3" returns ("file", "MEMORY.md#L3").
// "tool" (no colon) returns ("tool", "").
// "" returns ("", "").
func ParseSource(s string) (prefix, detail string) {
	if s == "" {
		return "", ""
	}
	if idx := strings.Index(s, ":"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, ""
}

// ValidateSource returns nil if the source uses a known prefix or is empty.
// Returns an error for unknown prefixes (callers should log-warn, not hard-fail).
func ValidateSource(s string) error {
	if s == "" {
		return nil
	}
	prefix, _ := ParseSource(s)
	if knownPrefixes[prefix] {
		return nil
	}
	return fmt.Errorf("unknown source prefix %q (known: file, skill, tool, user, llm, sub-agent)", prefix)
}

// StalenessThreshold returns the age-based staleness threshold for a source prefix.
// Returns 0 for source types that should never be considered stale.
func StalenessThreshold(prefix string) time.Duration {
	switch prefix {
	case SourcePrefixFile:
		return 0 // file: sources use hash-based detection, not age
	case SourcePrefixUser:
		return 0 // user-provided facts are authoritative
	case SourcePrefixLLM:
		return 14 * 24 * time.Hour // 14 days
	case SourcePrefixSkill, SourcePrefixSubAgent, SourcePrefixTool:
		return 30 * 24 * time.Hour // 30 days
	default:
		return 30 * 24 * time.Hour // default 30 days
	}
}
