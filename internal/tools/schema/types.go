// Package schema provides enhanced tool schema types with discovery and guidance fields.
package schema

import (
	"context"
	"time"
)

// EnhancedParameter extends JSON Schema with discovery and guidance fields.
// These fields help AI agents make better tool calls by providing real-time
// information about available options and clearer guidance about parameter constraints.
type EnhancedParameter struct {
	// Standard JSON Schema fields
	Type        string                 `json:"type"`
	Description string                 `json:"description,omitempty"`
	Enum        []string               `json:"enum,omitempty"`
	Default     interface{}            `json:"default,omitempty"`
	Minimum     *float64               `json:"minimum,omitempty"`
	Maximum     *float64               `json:"maximum,omitempty"`
	Items       map[string]interface{} `json:"items,omitempty"`

	// Enhanced fields
	Examples        []interface{}  `json:"examples,omitempty"`         // Example values
	ValidationHints []string       `json:"validation_hints,omitempty"` // Human-readable constraints
	DynamicValues   *DynamicValues `json:"dynamic_values,omitempty"`   // Runtime-discovered values
}

// DynamicValues contains runtime-discovered parameter options.
type DynamicValues struct {
	Source      string         `json:"source"`       // e.g., "channels", "workspace"
	Values      []DynamicValue `json:"values"`       // Discovered values
	LastUpdated time.Time      `json:"last_updated"` // When discovery ran
}

// DynamicValue represents a single discovered value with metadata.
type DynamicValue struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`       // Human-readable label
	Status      string `json:"status,omitempty"`      // e.g., "online", "offline"
	Description string `json:"description,omitempty"` // Additional context
}

// SchemaHints provides enhancement instructions for a parameter.
type SchemaHints struct {
	Examples          []interface{}
	ValidationHints   []string
	DiscoveryType     string // e.g., "channels", "workspace_paths"
	EnumFromDiscovery bool   // Populate enum from discovery data
}

// DiscoveryProvider provides runtime data for schema enhancement.
type DiscoveryProvider interface {
	// GetDiscoveryData returns dynamic values for a specific discovery type.
	GetDiscoveryData(ctx context.Context, discoveryType string) (*DynamicValues, error)
}

// ChannelStatusGetter abstracts channel status retrieval to avoid import cycles.
type ChannelStatusGetter interface {
	GetStatus() map[string]interface{} // Returns channel statuses
}
