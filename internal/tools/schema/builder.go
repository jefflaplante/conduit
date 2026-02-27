package schema

import (
	"context"
	"fmt"
	"log"
)

// Builder enhances tool schemas with discovery data.
type Builder struct {
	providers map[string]DiscoveryProvider
}

// NewBuilder creates a schema builder with the given discovery providers.
func NewBuilder(providers map[string]DiscoveryProvider) *Builder {
	return &Builder{providers: providers}
}

// EnhanceSchema takes a static schema and enriches it with discovery data.
func (b *Builder) EnhanceSchema(ctx context.Context, staticSchema map[string]interface{}, hints map[string]SchemaHints) map[string]interface{} {
	enhanced := deepCopyMap(staticSchema)

	properties, ok := enhanced["properties"].(map[string]interface{})
	if !ok {
		return enhanced
	}

	for paramName, paramSchema := range properties {
		paramMap, ok := paramSchema.(map[string]interface{})
		if !ok {
			continue
		}

		// Apply hints if provided
		if hints != nil {
			if hint, exists := hints[paramName]; exists {
				b.applyHints(ctx, paramMap, hint)
			}
		}

		properties[paramName] = paramMap
	}

	enhanced["properties"] = properties
	return enhanced
}

func (b *Builder) applyHints(ctx context.Context, paramMap map[string]interface{}, hint SchemaHints) {
	// Add examples
	if len(hint.Examples) > 0 {
		paramMap["examples"] = hint.Examples
	}

	// Add validation hints
	if len(hint.ValidationHints) > 0 {
		paramMap["validation_hints"] = hint.ValidationHints
	}

	// Add dynamic discovery data
	if hint.DiscoveryType != "" && b.providers != nil {
		if provider, exists := b.providers[hint.DiscoveryType]; exists {
			discovery, err := provider.GetDiscoveryData(ctx, hint.DiscoveryType)
			if err != nil {
				log.Printf("[SchemaBuilder] Discovery error for %s: %v", hint.DiscoveryType, err)
				return
			}
			if discovery != nil {
				// Add as extended field (x_ prefix for JSON Schema extensions)
				paramMap["x_discovery"] = discovery

				// Generate human-readable description from discovery data
				if len(discovery.Values) > 0 {
					availableStr := "Available: "
					for i, v := range discovery.Values {
						if i > 0 {
							if i < len(discovery.Values)-1 {
								availableStr += ", "
							} else {
								availableStr += ", and "
							}
						}
						if v.Status != "" {
							availableStr += fmt.Sprintf("%s (%s)", v.Value, v.Status)
						} else {
							availableStr += v.Value
						}
						// Limit to first few values to avoid overwhelming description
						if i >= 2 {
							remaining := len(discovery.Values) - 3
							if remaining > 0 {
								availableStr += fmt.Sprintf(" (and %d more)", remaining)
							}
							break
						}
					}

					// Update description to include discovery info
					if existingDesc, ok := paramMap["description"].(string); ok {
						paramMap["description"] = existingDesc + ". " + availableStr
					} else {
						paramMap["description"] = availableStr
					}
				}

				// Optionally populate enum from discovery
				if hint.EnumFromDiscovery && len(discovery.Values) > 0 {
					enumValues := make([]string, 0, len(discovery.Values))
					for _, v := range discovery.Values {
						enumValues = append(enumValues, v.Value)
					}
					paramMap["enum"] = enumValues
				}
			}
		}
	}
}

// deepCopyMap creates a deep copy of a map.
func deepCopyMap(original map[string]interface{}) map[string]interface{} {
	if original == nil {
		return nil
	}

	cpy := make(map[string]interface{}, len(original))
	for k, v := range original {
		switch val := v.(type) {
		case map[string]interface{}:
			cpy[k] = deepCopyMap(val)
		case []interface{}:
			newSlice := make([]interface{}, len(val))
			for i, item := range val {
				if m, ok := item.(map[string]interface{}); ok {
					newSlice[i] = deepCopyMap(m)
				} else {
					newSlice[i] = item
				}
			}
			cpy[k] = newSlice
		case []string:
			newSlice := make([]string, len(val))
			copy(newSlice, val)
			cpy[k] = newSlice
		default:
			cpy[k] = v
		}
	}
	return cpy
}
