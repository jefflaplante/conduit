package vecgo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Indexer watches workspace memory files and indexes them into the vector
// database. It tracks file content hashes so that only new or changed files
// are re-embedded and re-indexed.
type Indexer struct {
	svc          *Service
	workspaceDir string
	hashes       map[string]string // relPath -> SHA-256 hex
	mu           sync.Mutex

	// Polling configuration
	pollInterval time.Duration
	stopCh       chan struct{}
	stopped      chan struct{}
}

// IndexerConfig configures the memory indexing pipeline.
type IndexerConfig struct {
	// WorkspaceDir is the root directory to scan for .md files.
	WorkspaceDir string

	// PollInterval controls how often the indexer re-scans for changes.
	// Zero disables periodic scanning (manual IndexNow only).
	PollInterval time.Duration
}

// NewIndexer creates a new memory indexing pipeline.
func NewIndexer(svc *Service, cfg IndexerConfig) *Indexer {
	return &Indexer{
		svc:          svc,
		workspaceDir: cfg.WorkspaceDir,
		hashes:       make(map[string]string),
		pollInterval: cfg.PollInterval,
		stopCh:       make(chan struct{}),
		stopped:      make(chan struct{}),
	}
}

// IndexNow performs a full scan and index of all memory files. Only files
// whose content has changed since the last scan are re-indexed. Stale entries
// (files that have been deleted) are removed from the vector index.
func (idx *Indexer) IndexNow(ctx context.Context) (*IndexResult, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.workspaceDir == "" {
		return &IndexResult{}, nil
	}

	result := &IndexResult{StartTime: time.Now()}

	// Collect all .md files
	var mdFiles []string
	err := filepath.WalkDir(idx.workspaceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk workspace directory: %w", err)
	}

	result.FilesScanned = len(mdFiles)

	// Build a set of current relative paths for stale detection
	currentPaths := make(map[string]bool, len(mdFiles))

	for _, fullPath := range mdFiles {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		relPath, relErr := filepath.Rel(idx.workspaceDir, fullPath)
		if relErr != nil {
			log.Printf("vecgo indexer: cannot relativize %s: %v", fullPath, relErr)
			continue
		}
		currentPaths[relPath] = true

		changed, indexErr := idx.indexFileIfChanged(ctx, fullPath, relPath)
		if indexErr != nil {
			log.Printf("vecgo indexer: error indexing %s: %v", relPath, indexErr)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath, indexErr))
			continue
		}
		if changed {
			result.FilesIndexed++
		} else {
			result.FilesSkipped++
		}
	}

	// Remove stale entries (files that no longer exist on disk)
	for path := range idx.hashes {
		if !currentPaths[path] {
			if removeErr := idx.svc.Remove(ctx, path); removeErr != nil {
				log.Printf("vecgo indexer: failed to remove stale entry %s: %v", path, removeErr)
			} else {
				result.FilesRemoved++
			}
			delete(idx.hashes, path)
		}
	}

	// Save index state
	if result.FilesIndexed > 0 || result.FilesRemoved > 0 {
		if saveErr := idx.svc.Save(ctx); saveErr != nil {
			log.Printf("vecgo indexer: save failed: %v", saveErr)
			result.Errors = append(result.Errors, fmt.Sprintf("save: %v", saveErr))
		}
	}

	result.Duration = time.Since(result.StartTime)
	return result, nil
}

// IndexFile indexes a single file by its relative path. This is useful for
// on-demand indexing when a file is known to have changed.
func (idx *Indexer) IndexFile(ctx context.Context, relativePath string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	fullPath := filepath.Join(idx.workspaceDir, relativePath)
	changed, err := idx.indexFileIfChanged(ctx, fullPath, relativePath)
	if err != nil {
		return err
	}
	if changed {
		return idx.svc.Save(ctx)
	}
	return nil
}

// RemoveFile removes a file from the vector index.
func (idx *Indexer) RemoveFile(ctx context.Context, relativePath string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if err := idx.svc.Remove(ctx, relativePath); err != nil {
		return err
	}
	delete(idx.hashes, relativePath)
	return idx.svc.Save(ctx)
}

// Start begins periodic polling for file changes. It runs IndexNow at each
// interval. The initial scan happens synchronously before Start returns.
// Call Stop() to terminate the background polling.
func (idx *Indexer) Start(ctx context.Context) error {
	// Initial scan
	result, err := idx.IndexNow(ctx)
	if err != nil {
		return fmt.Errorf("initial indexing failed: %w", err)
	}
	if result.FilesIndexed > 0 {
		log.Printf("vecgo indexer: initial scan indexed %d files (%d skipped, %d removed) in %v",
			result.FilesIndexed, result.FilesSkipped, result.FilesRemoved, result.Duration)
	}

	// Start background polling if interval is configured
	if idx.pollInterval > 0 {
		go idx.pollLoop(ctx)
	}

	return nil
}

// Stop terminates the background polling goroutine.
func (idx *Indexer) Stop() {
	close(idx.stopCh)
	if idx.pollInterval > 0 {
		<-idx.stopped
	}
}

// Status returns the current state of the indexer.
func (idx *Indexer) Status() IndexerStatus {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	return IndexerStatus{
		WorkspaceDir: idx.workspaceDir,
		TrackedFiles: len(idx.hashes),
		PollInterval: idx.pollInterval,
	}
}

// pollLoop runs periodic scans until Stop is called.
func (idx *Indexer) pollLoop(ctx context.Context) {
	defer close(idx.stopped)

	ticker := time.NewTicker(idx.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-idx.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			result, err := idx.IndexNow(scanCtx)
			cancel()

			if err != nil {
				log.Printf("vecgo indexer: periodic scan failed: %v", err)
				continue
			}
			if result.FilesIndexed > 0 || result.FilesRemoved > 0 {
				log.Printf("vecgo indexer: periodic scan: %d indexed, %d removed, %d skipped in %v",
					result.FilesIndexed, result.FilesRemoved, result.FilesSkipped, result.Duration)
			}
		}
	}
}

// indexFileIfChanged reads a file, computes its hash, and indexes it if the
// content has changed since the last scan. Returns true if the file was
// (re-)indexed, false if it was skipped as unchanged.
func (idx *Indexer) indexFileIfChanged(ctx context.Context, fullPath, relPath string) (bool, error) {
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", relPath, err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Skip unchanged files
	if existingHash, ok := idx.hashes[relPath]; ok && existingHash == hash {
		return false, nil
	}

	// Build metadata
	name := filepath.Base(relPath)
	meta := map[string]string{
		"source": "workspace",
		"path":   relPath,
		"title":  strings.TrimSuffix(name, filepath.Ext(name)),
	}

	// Check if this is a memory file specifically
	if relPath == "MEMORY.md" || strings.HasPrefix(relPath, "memory/") || strings.HasPrefix(relPath, "memory"+string(filepath.Separator)) {
		meta["type"] = "memory"
	}

	// Index the document (the VecGo pipeline handles chunking and embedding)
	if err := idx.svc.Index(ctx, relPath, string(data), meta); err != nil {
		return false, fmt.Errorf("index %s: %w", relPath, err)
	}

	idx.hashes[relPath] = hash
	return true, nil
}

// IndexResult contains the results of an indexing run.
type IndexResult struct {
	StartTime    time.Time     `json:"start_time"`
	Duration     time.Duration `json:"duration"`
	FilesScanned int           `json:"files_scanned"`
	FilesIndexed int           `json:"files_indexed"`
	FilesSkipped int           `json:"files_skipped"`
	FilesRemoved int           `json:"files_removed"`
	Errors       []string      `json:"errors,omitempty"`
}

// IndexerStatus contains the current state of the indexer.
type IndexerStatus struct {
	WorkspaceDir string        `json:"workspace_dir"`
	TrackedFiles int           `json:"tracked_files"`
	PollInterval time.Duration `json:"poll_interval"`
}
