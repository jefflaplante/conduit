package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PluginManifest is the on-disk JSON format for a plugin package.
// It describes the plugin's identity, requirements, and capabilities
// so the gateway can discover and validate it before loading.
type PluginManifest struct {
	// Name is the unique plugin identifier.
	Name string `json:"name"`

	// Version is the semantic version of the plugin.
	Version string `json:"version"`

	// Description is a human-readable summary.
	Description string `json:"description"`

	// Author is the plugin author or organization.
	Author string `json:"author"`

	// Entrypoint is the relative path to the plugin's Go package or binary.
	Entrypoint string `json:"entrypoint"`

	// Dependencies lists other plugin names this plugin requires.
	Dependencies []string `json:"dependencies,omitempty"`

	// Capabilities lists the functional capabilities the plugin provides.
	Capabilities []string `json:"capabilities,omitempty"`

	// Tags are searchable labels for categorization.
	Tags []string `json:"tags,omitempty"`

	// MinGatewayVersion is the minimum gateway version required.
	MinGatewayVersion string `json:"min_gateway_version,omitempty"`

	// ConfigSchema describes the plugin's configuration options.
	// Keys are parameter names; values describe the expected type and defaults.
	ConfigSchema map[string]ConfigParam `json:"config_schema,omitempty"`

	// Dir is the filesystem directory where the manifest was found.
	// This is populated by DiscoverPlugins and not serialized.
	Dir string `json:"-"`
}

// ConfigParam describes a single configuration parameter in a plugin manifest.
type ConfigParam struct {
	Type        string      `json:"type"`
	Description string      `json:"description"`
	Default     interface{} `json:"default,omitempty"`
	Required    bool        `json:"required,omitempty"`
}

// ManifestFileName is the expected filename for plugin manifests.
const ManifestFileName = "plugin.json"

// LoadManifest reads and parses a plugin manifest from the given file path.
func LoadManifest(path string) (*PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}

	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}

	manifest.Dir = filepath.Dir(path)
	return &manifest, nil
}

// ValidateManifest checks that a manifest is complete and has valid values.
// Returns a slice of validation errors (empty if valid).
func ValidateManifest(m *PluginManifest) []error {
	var errs []error

	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, fmt.Errorf("manifest name is required"))
	}

	if strings.TrimSpace(m.Version) == "" {
		errs = append(errs, fmt.Errorf("manifest version is required"))
	} else if !semverRegex.MatchString(m.Version) {
		errs = append(errs, fmt.Errorf("manifest version %q is not valid semver", m.Version))
	}

	if strings.TrimSpace(m.Description) == "" {
		errs = append(errs, fmt.Errorf("manifest description is required"))
	}

	if strings.TrimSpace(m.Author) == "" {
		errs = append(errs, fmt.Errorf("manifest author is required"))
	}

	if strings.TrimSpace(m.Entrypoint) == "" {
		errs = append(errs, fmt.Errorf("manifest entrypoint is required"))
	}

	if m.MinGatewayVersion != "" && !semverRegex.MatchString(m.MinGatewayVersion) {
		errs = append(errs, fmt.Errorf("min_gateway_version %q is not valid semver", m.MinGatewayVersion))
	}

	// Validate config schema types
	validTypes := map[string]bool{
		"string": true, "int": true, "float": true, "bool": true,
		"[]string": true, "map": true,
	}
	for paramName, param := range m.ConfigSchema {
		if !validTypes[param.Type] {
			errs = append(errs, fmt.Errorf("config parameter %q has invalid type %q", paramName, param.Type))
		}
	}

	return errs
}

// ToMetadata converts a manifest into PluginMetadata.
func (m *PluginManifest) ToMetadata() PluginMetadata {
	return PluginMetadata{
		Name:              m.Name,
		Version:           m.Version,
		Author:            m.Author,
		Description:       m.Description,
		Tags:              m.Tags,
		Capabilities:      m.Capabilities,
		Dependencies:      m.Dependencies,
		MinGatewayVersion: m.MinGatewayVersion,
	}
}

// DiscoverPlugins scans the given directories for plugin manifests.
// Each directory is expected to contain subdirectories, each with a
// plugin.json manifest file. Invalid manifests are skipped with a
// log message.
func DiscoverPlugins(dirs []string) []PluginManifest {
	var manifests []PluginManifest
	seen := make(map[string]bool)

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			manifestPath := filepath.Join(dir, entry.Name(), ManifestFileName)
			if _, err := os.Stat(manifestPath); err != nil {
				continue
			}

			manifest, err := LoadManifest(manifestPath)
			if err != nil {
				continue
			}

			if errs := ValidateManifest(manifest); len(errs) > 0 {
				continue
			}

			// Deduplicate by name — first occurrence wins
			if seen[manifest.Name] {
				continue
			}
			seen[manifest.Name] = true

			manifests = append(manifests, *manifest)
		}
	}

	return manifests
}
