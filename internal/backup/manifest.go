package backup

import (
	"encoding/json"
	"fmt"
	"time"

	"conduit/internal/version"
)

const ManifestVersion = "1.0"

// NewManifest builds a manifest from the given options and discovered info.
func NewManifest(components BackupComponents, paths OriginalPaths, dbInfo DatabaseInfo) *BackupManifest {
	return &BackupManifest{
		Version:        ManifestVersion,
		Timestamp:      time.Now().UTC(),
		GatewayVersion: version.Full(),
		Components:     components,
		OriginalPaths:  paths,
		DatabaseInfo:   dbInfo,
	}
}

// ValidateManifest checks that a manifest is usable for restore.
func ValidateManifest(m *BackupManifest) error {
	if m.Version == "" {
		return fmt.Errorf("manifest missing version")
	}
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported manifest version %q (expected %q)", m.Version, ManifestVersion)
	}
	if m.Timestamp.IsZero() {
		return fmt.Errorf("manifest missing timestamp")
	}
	if m.Components == 0 {
		return fmt.Errorf("manifest has no components")
	}
	return nil
}

// MarshalManifest serializes a manifest to JSON.
func MarshalManifest(m *BackupManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// UnmarshalManifest deserializes a manifest from JSON.
func UnmarshalManifest(data []byte) (*BackupManifest, error) {
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	return &m, nil
}
