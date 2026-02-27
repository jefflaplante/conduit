package plugins

import (
	"strings"
	"sync"
)

// InstallStatus represents the installation state of a plugin.
type InstallStatus string

const (
	// StatusAvailable means the plugin is known but not installed.
	StatusAvailable InstallStatus = "available"

	// StatusInstalled means the plugin is installed and ready to use.
	StatusInstalled InstallStatus = "installed"

	// StatusDisabled means the plugin is installed but disabled.
	StatusDisabled InstallStatus = "disabled"
)

// PluginEntry represents a plugin in the marketplace catalog.
// It combines metadata with installation status and community metrics.
type PluginEntry struct {
	// Metadata is the plugin's descriptive information.
	Metadata PluginMetadata `json:"metadata"`

	// Status is the current installation state.
	Status InstallStatus `json:"status"`

	// Rating is the community rating (0.0 - 5.0).
	Rating float64 `json:"rating"`

	// DownloadCount tracks how many times the plugin has been downloaded.
	DownloadCount int `json:"download_count"`

	// Verified indicates whether the plugin has been verified by the gateway team.
	Verified bool `json:"verified"`
}

// Catalog provides a searchable, in-memory directory of available plugins.
// It serves as a local marketplace for discovering and managing plugins.
// All methods are safe for concurrent use.
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]*PluginEntry
}

// NewCatalog creates a new empty marketplace catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		entries: make(map[string]*PluginEntry),
	}
}

// AddEntry adds or updates a plugin entry in the catalog.
func (c *Catalog) AddEntry(entry PluginEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[entry.Metadata.Name] = &entry
}

// RemoveEntry removes a plugin entry from the catalog.
// Returns true if the entry was found and removed, false otherwise.
func (c *Catalog) RemoveEntry(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[name]; !exists {
		return false
	}
	delete(c.entries, name)
	return true
}

// GetEntry retrieves a plugin entry by name.
// Returns nil if the entry is not found.
func (c *Catalog) GetEntry(name string) *PluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.entries[name]
	if !exists {
		return nil
	}

	// Return a copy to avoid data races on the caller side
	copy := *entry
	return &copy
}

// Search performs a case-insensitive substring search across plugin names,
// descriptions, and tags. Returns all matching entries.
func (c *Catalog) Search(query string) []PluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if query == "" {
		return c.allEntriesLocked()
	}

	q := strings.ToLower(query)
	var results []PluginEntry

	for _, entry := range c.entries {
		if c.matchesQuery(entry, q) {
			results = append(results, *entry)
		}
	}

	return results
}

// ListByTag returns all plugin entries that have the given tag.
// Tag matching is case-insensitive.
func (c *Catalog) ListByTag(tag string) []PluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	t := strings.ToLower(tag)
	var results []PluginEntry

	for _, entry := range c.entries {
		for _, entryTag := range entry.Metadata.Tags {
			if strings.ToLower(entryTag) == t {
				results = append(results, *entry)
				break
			}
		}
	}

	return results
}

// ListByStatus returns all entries matching the given installation status.
func (c *Catalog) ListByStatus(status InstallStatus) []PluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var results []PluginEntry
	for _, entry := range c.entries {
		if entry.Status == status {
			results = append(results, *entry)
		}
	}
	return results
}

// ListAll returns all entries in the catalog.
func (c *Catalog) ListAll() []PluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.allEntriesLocked()
}

// Count returns the number of entries in the catalog.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// UpdateStatus updates the installation status of a catalog entry.
// Returns false if the entry is not found.
func (c *Catalog) UpdateStatus(name string, status InstallStatus) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[name]
	if !exists {
		return false
	}
	entry.Status = status
	return true
}

// IncrementDownloads increments the download count for a plugin.
// Returns false if the entry is not found.
func (c *Catalog) IncrementDownloads(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[name]
	if !exists {
		return false
	}
	entry.DownloadCount++
	return true
}

// SetRating sets the rating for a plugin entry (clamped to 0.0 - 5.0).
// Returns false if the entry is not found.
func (c *Catalog) SetRating(name string, rating float64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[name]
	if !exists {
		return false
	}

	if rating < 0 {
		rating = 0
	}
	if rating > 5 {
		rating = 5
	}
	entry.Rating = rating
	return true
}

// ImportFromManifests populates the catalog from discovered manifests.
// Existing entries with the same name are not overwritten.
func (c *Catalog) ImportFromManifests(manifests []PluginManifest) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	added := 0
	for _, m := range manifests {
		if _, exists := c.entries[m.Name]; exists {
			continue
		}
		c.entries[m.Name] = &PluginEntry{
			Metadata: m.ToMetadata(),
			Status:   StatusAvailable,
		}
		added++
	}
	return added
}

// matchesQuery checks if an entry matches the search query.
// Must be called with at least a read lock held.
func (c *Catalog) matchesQuery(entry *PluginEntry, query string) bool {
	if strings.Contains(strings.ToLower(entry.Metadata.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(entry.Metadata.Description), query) {
		return true
	}
	for _, tag := range entry.Metadata.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	for _, cap := range entry.Metadata.Capabilities {
		if strings.Contains(strings.ToLower(cap), query) {
			return true
		}
	}
	return false
}

// allEntriesLocked returns a copy of all entries. Must be called with a read lock.
func (c *Catalog) allEntriesLocked() []PluginEntry {
	result := make([]PluginEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		result = append(result, *entry)
	}
	return result
}
