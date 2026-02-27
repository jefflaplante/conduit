package plugins

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"conduit/internal/tools/types"
)

// semverRegex validates semantic versioning strings (e.g., "1.0.0", "2.1.3-beta").
var semverRegex = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([a-zA-Z0-9.]+))?$`)

// Plugin defines the standard contract for a gateway plugin.
// Implementations provide metadata, tools, and lifecycle hooks.
type Plugin interface {
	// Metadata returns the plugin's descriptive information.
	Metadata() PluginMetadata

	// Initialize sets up the plugin with the provided context.
	// Called once when the plugin is registered.
	Initialize(ctx PluginContext) error

	// Tools returns the set of tools this plugin provides.
	Tools() []types.Tool

	// Shutdown performs cleanup when the plugin is unregistered or the gateway stops.
	Shutdown() error
}

// PluginMetadata contains descriptive information about a plugin.
type PluginMetadata struct {
	// Name is the unique identifier for the plugin.
	Name string `json:"name"`

	// Version is the semantic version string (e.g., "1.2.3").
	Version string `json:"version"`

	// Author is the plugin author or organization.
	Author string `json:"author"`

	// Description is a human-readable summary of the plugin's purpose.
	Description string `json:"description"`

	// Tags are searchable labels for categorization.
	Tags []string `json:"tags,omitempty"`

	// Capabilities lists the functional capabilities this plugin provides.
	Capabilities []string `json:"capabilities,omitempty"`

	// Dependencies lists other plugins this plugin depends on (by name).
	Dependencies []string `json:"dependencies,omitempty"`

	// MinGatewayVersion is the minimum gateway version required.
	MinGatewayVersion string `json:"min_gateway_version,omitempty"`
}

// Validate checks that the metadata has all required fields and valid formats.
func (m PluginMetadata) Validate() []error {
	var errs []error

	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, fmt.Errorf("plugin name is required"))
	}

	if strings.TrimSpace(m.Version) == "" {
		errs = append(errs, fmt.Errorf("plugin version is required"))
	} else if !semverRegex.MatchString(m.Version) {
		errs = append(errs, fmt.Errorf("plugin version %q is not valid semver", m.Version))
	}

	if strings.TrimSpace(m.Author) == "" {
		errs = append(errs, fmt.Errorf("plugin author is required"))
	}

	if strings.TrimSpace(m.Description) == "" {
		errs = append(errs, fmt.Errorf("plugin description is required"))
	}

	if m.MinGatewayVersion != "" && !semverRegex.MatchString(m.MinGatewayVersion) {
		errs = append(errs, fmt.Errorf("min_gateway_version %q is not valid semver", m.MinGatewayVersion))
	}

	return errs
}

// PluginContext provides services and configuration available to plugins
// during initialization. This is the gateway's interface to the plugin.
type PluginContext struct {
	// Config provides read-only access to plugin-specific configuration.
	Config map[string]interface{}

	// Logger is a logger instance for the plugin to use.
	Logger *log.Logger

	// DataDir is a directory the plugin may use for persistent data.
	DataDir string
}
