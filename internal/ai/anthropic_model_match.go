package ai

import (
	"regexp"
	"strings"
)

// anthropicSnapshotSuffix matches a dated snapshot suffix like -20250514.
var anthropicSnapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// normalizeAnthropicModel reduces a model ID to its alias form: lowercased,
// provider prefix ("anthropic/") removed, and a trailing "-latest" or dated
// snapshot suffix stripped. conduit-31jg.16.
func normalizeAnthropicModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	m = strings.TrimSuffix(m, "-latest")
	return anthropicSnapshotSuffix.ReplaceAllString(m, "")
}

// anthropicModelsMatch reports whether the model the API says it served is
// the model that was requested, treating aliases and their dated snapshots
// (any year) as the same model. A family prefix is NOT a match:
// "claude-sonnet-4" vs "claude-sonnet-4-6" are different models. Used for a
// log-only parity check. conduit-31jg.16.
func anthropicModelsMatch(requested, served string) bool {
	return normalizeAnthropicModel(requested) == normalizeAnthropicModel(served)
}
