package oauthflow

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"conduit/internal/datadir"
)

// StorageFile is the filename for token storage.
const StorageFile = "auth.json"

// legacyDirName is the old directory that was used before consolidation.
const legacyDirName = ".conduit"

// DataDirConfig holds the optional config value for the data directory.
// Set this before calling any storage functions so that the config-level
// data_dir override is respected.
var DataDirConfig string

// StoredToken represents a single provider's OAuth tokens on disk.
type StoredToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresAt    int64  `json:"expires_at"`
	Scope        string `json:"scope,omitempty"`
	ObtainedAt   int64  `json:"obtained_at"`
	ClientID     string `json:"client_id,omitempty"`
}

// IsExpired returns true if the access token has expired.
func (t *StoredToken) IsExpired() bool {
	return time.Now().Unix() >= t.ExpiresAt
}

// ExpiresIn returns the duration until the token expires.
func (t *StoredToken) ExpiresIn() time.Duration {
	return time.Until(time.Unix(t.ExpiresAt, 0))
}

// tokenStore represents the on-disk JSON format.
type tokenStore struct {
	Version   int                     `json:"version"`
	Providers map[string]*StoredToken `json:"providers"`
}

// storagePath returns the full path to auth.json.
func storagePath() (string, error) {
	return datadir.FilePath(DataDirConfig, StorageFile)
}

// migrateFromLegacy copies auth.json from the old ~/.conduit/ directory
// to the current data directory if the old file exists but the new one doesn't.
func migrateFromLegacy(newPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	oldPath := filepath.Join(home, legacyDirName, StorageFile)

	// Only migrate if old exists and new does not.
	if _, err := os.Stat(oldPath); err != nil {
		return // old file doesn't exist, nothing to migrate
	}
	if _, err := os.Stat(newPath); err == nil {
		return // new file already exists, no migration needed
	}

	data, err := os.ReadFile(oldPath)
	if err != nil {
		return
	}

	dir := filepath.Dir(newPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	if err := os.WriteFile(newPath, data, 0600); err != nil {
		return
	}

	log.Printf("[OAuth] Migrated %s → %s (you can remove %s)",
		oldPath, newPath, filepath.Join(home, legacyDirName))
}

// loadStore reads the token store from disk. Returns an empty store if the file
// does not exist.
func loadStore() (*tokenStore, error) {
	path, err := storagePath()
	if err != nil {
		return nil, err
	}

	// Attempt migration from the legacy directory.
	migrateFromLegacy(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &tokenStore{Version: 1, Providers: make(map[string]*StoredToken)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	var store tokenStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if store.Providers == nil {
		store.Providers = make(map[string]*StoredToken)
	}
	return &store, nil
}

// saveStore writes the token store to disk atomically (temp file + rename).
// Creates the storage directory with 0700 and the file with 0600.
func saveStore(store *tokenStore) error {
	path, err := storagePath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token store: %w", err)
	}
	data = append(data, '\n')

	// Atomic write: write to temp file, then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// LoadProviderToken loads the stored token for a provider. Returns nil, nil if
// no token is stored for the given provider.
func LoadProviderToken(provider string) (*StoredToken, error) {
	store, err := loadStore()
	if err != nil {
		return nil, err
	}
	token, ok := store.Providers[provider]
	if !ok {
		return nil, nil
	}
	return token, nil
}

// SaveProviderToken saves a token for a provider, merging into the existing store.
func SaveProviderToken(provider string, token *StoredToken) error {
	store, err := loadStore()
	if err != nil {
		return err
	}
	store.Providers[provider] = token
	return saveStore(store)
}

// DeleteProviderToken removes a provider's token from storage.
func DeleteProviderToken(provider string) error {
	store, err := loadStore()
	if err != nil {
		return err
	}
	delete(store.Providers, provider)
	return saveStore(store)
}

// StoragePath returns the path to auth.json (exported for display purposes).
func StoragePath() (string, error) {
	return storagePath()
}
