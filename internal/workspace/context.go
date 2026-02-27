package workspace

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorkspaceContext manages loading and caching of workspace context files
type WorkspaceContext struct {
	workspaceDir string
	files        map[string]*ContextFile
	security     *SecurityManager
	cache        *FileCache
	mu           sync.RWMutex
}

// ContextFile represents a loaded workspace context file
type ContextFile struct {
	Path         string    `json:"path"`
	Content      string    `json:"content"`
	LastModified time.Time `json:"last_modified"`
	Size         int64     `json:"size"`
}

// ContextBundle contains all loaded context files for a session
type ContextBundle struct {
	Files     map[string]string      `json:"files"`
	Metadata  map[string]interface{} `json:"metadata"`
	Timestamp time.Time              `json:"timestamp"`
	SessionID string                 `json:"session_id"`
}

// SecurityContext provides session information for access control
type SecurityContext struct {
	SessionType string `json:"session_type"` // "main", "shared", "isolated"
	ChannelID   string `json:"channel_id"`   // Source channel
	UserID      string `json:"user_id"`      // User identifier
	SessionID   string `json:"session_id"`   // Session identifier
}

// NewWorkspaceContext creates a new workspace context manager
func NewWorkspaceContext(workspaceDir string) *WorkspaceContext {
	return &WorkspaceContext{
		workspaceDir: workspaceDir,
		files:        make(map[string]*ContextFile),
		security:     NewSecurityManager(),
		cache:        NewFileCache(5*time.Minute, 50*1024*1024), // 5min TTL, 50MB max
	}
}

// LoadContext loads workspace context files filtered by security rules
func (wc *WorkspaceContext) LoadContext(ctx context.Context, securityCtx SecurityContext) (*ContextBundle, error) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	log.Printf("[Workspace] LoadContext called for session type: %s, channel: %s", securityCtx.SessionType, securityCtx.ChannelID)

	bundle := &ContextBundle{
		Files:     make(map[string]string),
		Metadata:  make(map[string]interface{}),
		Timestamp: time.Now(),
		SessionID: securityCtx.SessionID,
	}

	// Discover available context files
	files, err := wc.discoverFiles()
	if err != nil {
		return nil, fmt.Errorf("failed to discover context files: %w", err)
	}

	log.Printf("[Workspace] Discovered %d files", len(files))

	// Load permitted files
	for _, file := range files {
		if !wc.security.IsAccessible(file.Path, securityCtx) {
			log.Printf("[Workspace] Skipping %s (security filter)", file.Path)
			continue
		}

		content, err := wc.loadFile(file.Path)
		if err != nil {
			log.Printf("[Workspace] Error loading %s: %v", file.Path, err)
			continue
		}

		// Use relative path as key
		key := file.Path
		if strings.HasPrefix(key, wc.workspaceDir+"/") {
			key = strings.TrimPrefix(key, wc.workspaceDir+"/")
		}

		bundle.Files[key] = content
		log.Printf("[Workspace] Loaded %s (%d bytes)", key, len(content))
	}

	// Add metadata
	bundle.Metadata["workspace_dir"] = wc.workspaceDir
	bundle.Metadata["file_count"] = len(bundle.Files)
	bundle.Metadata["session_type"] = securityCtx.SessionType

	return bundle, nil
}

// discoverFiles finds all available workspace context files
func (wc *WorkspaceContext) discoverFiles() ([]ContextFile, error) {
	files := []ContextFile{}

	// Core workspace files
	coreFiles := []string{
		"SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md",
		"IDENTITY.md", "MEMORY.md", "HEARTBEAT.md",
	}

	for _, filename := range coreFiles {
		path := filepath.Join(wc.workspaceDir, filename)
		if file, err := wc.createContextFile(path, filename); err == nil {
			files = append(files, *file)
		}
	}

	// Memory files (recent daily logs)
	memoryFiles, err := wc.discoverMemoryFiles()
	if err != nil {
		// Log error but continue
	} else {
		files = append(files, memoryFiles...)
	}

	return files, nil
}

// discoverMemoryFiles finds recent daily memory files
func (wc *WorkspaceContext) discoverMemoryFiles() ([]ContextFile, error) {
	memoryDir := filepath.Join(wc.workspaceDir, "memory")

	// Check if memory directory exists
	if _, err := os.Stat(memoryDir); os.IsNotExist(err) {
		return []ContextFile{}, nil
	}

	files := []ContextFile{}

	// Get recent memory files (today + yesterday)
	now := time.Now()
	targets := []time.Time{now, now.AddDate(0, 0, -1)}

	for _, date := range targets {
		filename := fmt.Sprintf("%s.md", date.Format("2006-01-02"))
		path := filepath.Join(memoryDir, filename)
		relativePath := filepath.Join("memory", filename)

		if file, err := wc.createContextFile(path, relativePath); err == nil {
			files = append(files, *file)
		}
	}

	return files, nil
}

// createContextFile creates a ContextFile from a filesystem path
func (wc *WorkspaceContext) createContextFile(fullPath, relativePath string) (*ContextFile, error) {
	stat, err := os.Stat(fullPath)
	if err != nil {
		return nil, err
	}

	return &ContextFile{
		Path:         relativePath,
		LastModified: stat.ModTime(),
		Size:         stat.Size(),
	}, nil
}

// loadFile loads content from a file with caching
func (wc *WorkspaceContext) loadFile(relativePath string) (string, error) {
	fullPath := filepath.Join(wc.workspaceDir, relativePath)

	// Check cache first
	if content, ok := wc.cache.Get(relativePath); ok {
		return content, nil
	}

	// Load from filesystem
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", relativePath, err)
	}

	contentStr := string(content)

	// Cache the content
	wc.cache.Set(relativePath, contentStr)

	return contentStr, nil
}

// GetWorkspaceDir returns the workspace directory path
func (wc *WorkspaceContext) GetWorkspaceDir() string {
	return wc.workspaceDir
}

// InvalidateCache invalidates cached content for a specific file
func (wc *WorkspaceContext) InvalidateCache(relativePath string) {
	wc.cache.Delete(relativePath)
}

// ClearCache clears all cached content
func (wc *WorkspaceContext) ClearCache() {
	wc.cache.Clear()
}

// GetCacheStats returns cache statistics for monitoring
func (wc *WorkspaceContext) GetCacheStats() map[string]interface{} {
	return wc.cache.Stats()
}
