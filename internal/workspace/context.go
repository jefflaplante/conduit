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

// DefaultDailyLookbackDays is the default number of recent daily memory
// files loaded into the prompt (today + yesterday).
const DefaultDailyLookbackDays = 2

// DefaultStateFile is the session state file rendered into the prompt's
// Session State section, relative to the workspace dir (bd-3ia).
const DefaultStateFile = "memory/jules-state.json"

// DefaultCoreFiles is the core context file list used when no
// workspace.files.core is configured.
var DefaultCoreFiles = []string{
	"SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md",
	"IDENTITY.md", "MEMORY.md", "HEARTBEAT.md", "BOOTSTRAP.md",
}

// Options configures which workspace files are discovered and injected.
type Options struct {
	// DailyLookbackDays: see NewWorkspaceContextWithLookback.
	DailyLookbackDays int
	// CoreFiles lists core context files; empty = DefaultCoreFiles.
	CoreFiles []string
	// StateFile is the session state JSON (empty = DefaultStateFile).
	StateFile string
	// StateInject discovers the state file and allows it for every
	// session type.
	StateInject bool
	// MemoryMDInject injects MEMORY.md into Project Context.
	MemoryMDInject bool
}

// DefaultOptions returns the options matching the config defaults.
func DefaultOptions() Options {
	return Options{
		DailyLookbackDays: DefaultDailyLookbackDays,
		StateFile:         DefaultStateFile,
		StateInject:       true,
		MemoryMDInject:    true,
	}
}

// WorkspaceContext manages loading and caching of workspace context files
type WorkspaceContext struct {
	workspaceDir      string
	dailyLookbackDays int
	coreFiles         []string
	stateFile         string // "" = state injection off
	memoryMDInject    bool
	files             map[string]*ContextFile
	security          *SecurityManager
	cache             *FileCache
	mu                sync.RWMutex
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

// NewWorkspaceContext creates a new workspace context manager with the
// default daily-memory lookback (today + yesterday).
func NewWorkspaceContext(workspaceDir string) *WorkspaceContext {
	return NewWorkspaceContextWithLookback(workspaceDir, DefaultDailyLookbackDays)
}

// NewWorkspaceContextWithLookback creates a workspace context manager with an
// explicit daily-memory lookback. n = number of recent daily memory files to
// include, counting today as day 1. n = 0 disables daily memory injection
// entirely (startup routines read memory files explicitly when needed).
// Negative values fall back to the default.
func NewWorkspaceContextWithLookback(workspaceDir string, dailyLookbackDays int) *WorkspaceContext {
	opts := DefaultOptions()
	opts.DailyLookbackDays = dailyLookbackDays
	return NewWorkspaceContextWithOptions(workspaceDir, opts)
}

// NewWorkspaceContextWithOptions creates a workspace context manager from
// explicit options (the gateway passes the workspace.files config).
func NewWorkspaceContextWithOptions(workspaceDir string, opts Options) *WorkspaceContext {
	if opts.DailyLookbackDays < 0 {
		opts.DailyLookbackDays = DefaultDailyLookbackDays
	}
	coreFiles := opts.CoreFiles
	if len(coreFiles) == 0 {
		coreFiles = DefaultCoreFiles
	}
	wc := &WorkspaceContext{
		workspaceDir:      workspaceDir,
		files:             make(map[string]*ContextFile),
		security:          NewSecurityManager(),
		cache:             NewFileCache(5*time.Minute, 50*1024*1024), // 5min TTL, 50MB max
		dailyLookbackDays: opts.DailyLookbackDays,
		memoryMDInject:    opts.MemoryMDInject,
	}
	for _, name := range coreFiles {
		if rel, ok := wc.relativeName(name); ok {
			wc.coreFiles = append(wc.coreFiles, rel)
		}
	}
	if opts.StateInject {
		stateFile := opts.StateFile
		if stateFile == "" {
			stateFile = DefaultStateFile
		}
		if rel, ok := wc.relativeName(stateFile); ok {
			wc.stateFile = rel
			wc.security.AllowStateFile(rel)
		}
	}
	return wc
}

// relativeName maps a configured file name to a clean path relative to the
// workspace dir. Absolute paths inside the workspace are accepted (the
// files.core validation allows them); anything resolving outside is not.
func (wc *WorkspaceContext) relativeName(name string) (string, bool) {
	if filepath.IsAbs(name) {
		rel, err := filepath.Rel(wc.workspaceDir, name)
		if err != nil {
			return "", false
		}
		name = rel
	}
	if !filepath.IsLocal(name) {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(name)), true
}

// CoreFiles returns the core context files in configured order.
func (wc *WorkspaceContext) CoreFiles() []string {
	return append([]string(nil), wc.coreFiles...)
}

// StateFile returns the state file path relative to the workspace dir, or
// "" when state injection is off.
func (wc *WorkspaceContext) StateFile() string {
	return wc.stateFile
}

// MemoryMDInject reports whether MEMORY.md is injected into the prompt.
func (wc *WorkspaceContext) MemoryMDInject() bool {
	return wc.memoryMDInject
}

// LoadStateFile returns the state file's content when state injection is on,
// the session may read it and it exists.
func (wc *WorkspaceContext) LoadStateFile(securityCtx SecurityContext) (string, bool) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if wc.stateFile == "" || !wc.security.IsAccessible(wc.stateFile, securityCtx) {
		return "", false
	}
	content, err := wc.loadFile(wc.stateFile)
	if err != nil {
		return "", false
	}
	return content, true
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

	// Core workspace files (workspace.files.core, else DefaultCoreFiles),
	// then the state file when state injection is on (bd-3ia).
	names := wc.coreFiles
	if wc.stateFile != "" && !containsString(names, wc.stateFile) {
		names = append(append([]string(nil), names...), wc.stateFile)
	}
	for _, filename := range names {
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

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// discoverMemoryFiles finds recent daily memory files per the configured
// lookback window (n = days including today; 0 disables daily files).
func (wc *WorkspaceContext) discoverMemoryFiles() ([]ContextFile, error) {
	memoryDir := filepath.Join(wc.workspaceDir, "memory")

	// Check if memory directory exists
	if _, err := os.Stat(memoryDir); os.IsNotExist(err) {
		return []ContextFile{}, nil
	}

	files := []ContextFile{}
	if wc.dailyLookbackDays <= 0 {
		return files, nil
	}

	// Last n days, today included
	now := time.Now()
	for i := 0; i < wc.dailyLookbackDays; i++ {
		date := now.AddDate(0, 0, -i)
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
