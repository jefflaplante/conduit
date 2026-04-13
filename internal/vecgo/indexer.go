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
// are re-embedded and re-indexed. Embedding is serialized through a single
// background worker with configurable pacing to avoid overwhelming CPU-based
// embedding models.
type Indexer struct {
	svc          *Service
	workspaceDir string
	hashes       map[string]string // relPath -> SHA-256 hex
	mu           sync.Mutex

	// Polling configuration
	pollInterval time.Duration
	embedTimeout time.Duration
	embedPacing  time.Duration
	stopCh       chan struct{}
	stopped      chan struct{}

	// Embed worker queue
	workCh     chan indexJob
	workerDone chan struct{}
}

// IndexerConfig configures the memory indexing pipeline.
type IndexerConfig struct {
	// WorkspaceDir is the root directory to scan for .md files.
	WorkspaceDir string

	// PollInterval controls how often the indexer re-scans for changes.
	// Zero disables periodic scanning (manual IndexNow only).
	PollInterval time.Duration

	// EmbedTimeout is the per-file embedding timeout.
	// Zero uses the default (300s).
	EmbedTimeout time.Duration

	// EmbedPacing is the delay between consecutive embedding calls.
	// This gives CPU-based embedders breathing room between documents.
	// Zero disables pacing (back-to-back embedding).
	EmbedPacing time.Duration
}

// indexJob is a unit of work for the embed worker.
type indexJob struct {
	ctx      context.Context
	relPath  string
	data     []byte
	hash     string
	meta     map[string]string
	resultCh chan<- indexJobResult
}

// indexJobResult is the outcome of a single embedding job.
type indexJobResult struct {
	err error
}

// Default embedding timeout per file.
const defaultEmbedTimeout = 300 * time.Second

// NewIndexer creates a new memory indexing pipeline.
func NewIndexer(svc *Service, cfg IndexerConfig) *Indexer {
	embedTimeout := cfg.EmbedTimeout
	if embedTimeout <= 0 {
		embedTimeout = defaultEmbedTimeout
	}
	return &Indexer{
		svc:          svc,
		workspaceDir: cfg.WorkspaceDir,
		hashes:       make(map[string]string),
		pollInterval: cfg.PollInterval,
		embedTimeout: embedTimeout,
		embedPacing:  cfg.EmbedPacing,
		stopCh:       make(chan struct{}),
		stopped:      make(chan struct{}),
		workCh:       make(chan indexJob, 64),
		workerDone:   make(chan struct{}),
	}
}

// IndexNow performs a full scan and index of all memory files. Only files
// whose content has changed since the last scan are re-indexed. Stale entries
// (files that have been deleted) are removed from the vector index.
// Embedding calls are dispatched to the background worker with pacing.
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

		// Check if file changed (fast: file read + hash, no embedding)
		data, hash, changed, checkErr := idx.checkFileChanged(fullPath, relPath)
		if checkErr != nil {
			log.Printf("vecgo indexer: error checking %s: %v", relPath, checkErr)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath, checkErr))
			continue
		}
		if !changed {
			result.FilesSkipped++
			continue
		}

		// Build metadata
		meta := idx.buildMeta(relPath)

		// Submit to embed worker and wait for result
		embedErr := idx.submitAndWait(ctx, relPath, data, hash, meta)
		if embedErr != nil {
			log.Printf("vecgo indexer: error indexing %s: %v", relPath, embedErr)
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath, embedErr))
			continue
		}
		result.FilesIndexed++
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

	data, hash, changed, err := idx.checkFileChanged(fullPath, relativePath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	meta := idx.buildMeta(relativePath)

	if err := idx.submitAndWait(ctx, relativePath, data, hash, meta); err != nil {
		return err
	}
	return idx.svc.Save(ctx)
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

// Start begins periodic polling for file changes. It launches the embed
// worker, runs an initial scan, then starts the poll loop.
// Call Stop() to terminate background goroutines.
func (idx *Indexer) Start(ctx context.Context) error {
	// Launch the embed worker before the initial scan
	go idx.embedWorker(ctx)

	// Initial scan — log results but don't fail; polling will retry.
	result, err := idx.IndexNow(ctx)
	if err != nil {
		log.Printf("vecgo indexer: initial scan failed: %v (polling will retry)", err)
	} else {
		log.Printf("vecgo indexer: initial scan: %d scanned, %d indexed, %d skipped, %d removed, %d errors in %v",
			result.FilesScanned, result.FilesIndexed, result.FilesSkipped, result.FilesRemoved, len(result.Errors), result.Duration)
		for _, e := range result.Errors {
			log.Printf("vecgo indexer: scan error: %s", e)
		}
	}

	// Start background polling if interval is configured
	if idx.pollInterval > 0 {
		go idx.pollLoop(ctx)
	}

	return nil
}

// Stop terminates the background polling goroutine and embed worker.
func (idx *Indexer) Stop() {
	close(idx.stopCh)
	close(idx.workCh)
	<-idx.workerDone
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

// embedWorker processes embedding jobs sequentially with pacing.
// It runs until workCh is closed or the context is cancelled.
func (idx *Indexer) embedWorker(ctx context.Context) {
	defer close(idx.workerDone)

	first := true
	for job := range idx.workCh {
		// Pace between embedding calls (skip before the first one)
		if !first && idx.embedPacing > 0 {
			select {
			case <-ctx.Done():
				job.resultCh <- indexJobResult{err: ctx.Err()}
				continue
			case <-idx.stopCh:
				job.resultCh <- indexJobResult{err: fmt.Errorf("indexer stopped")}
				continue
			case <-time.After(idx.embedPacing):
			}
		}
		first = false

		// Per-file timeout ensures one slow embedding doesn't starve the rest.
		fileCtx, fileCancel := context.WithTimeout(job.ctx, idx.embedTimeout)
		err := idx.svc.Index(fileCtx, job.relPath, string(job.data), job.meta)
		fileCancel()

		if err != nil {
			job.resultCh <- indexJobResult{err: fmt.Errorf("index %s: %w", job.relPath, err)}
		} else {
			// Update hash on success (caller holds idx.mu)
			idx.hashes[job.relPath] = job.hash
			job.resultCh <- indexJobResult{}
		}
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
			scanCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			result, err := idx.IndexNow(scanCtx)
			cancel()

			if err != nil {
				log.Printf("vecgo indexer: periodic scan failed: %v", err)
				continue
			}
			if result.FilesIndexed > 0 || result.FilesRemoved > 0 || len(result.Errors) > 0 {
				log.Printf("vecgo indexer: periodic scan: %d indexed, %d removed, %d skipped, %d errors in %v",
					result.FilesIndexed, result.FilesRemoved, result.FilesSkipped, len(result.Errors), result.Duration)
				for _, e := range result.Errors {
					log.Printf("vecgo indexer: scan error: %s", e)
				}
			}
		}
	}
}

// checkFileChanged reads a file and computes its hash to determine if
// the content has changed since the last index. This is fast (no embedding).
func (idx *Indexer) checkFileChanged(fullPath, relPath string) (data []byte, hash string, changed bool, err error) {
	data, err = os.ReadFile(fullPath)
	if err != nil {
		return nil, "", false, fmt.Errorf("read %s: %w", relPath, err)
	}

	hash = fmt.Sprintf("%x", sha256.Sum256(data))

	if existingHash, ok := idx.hashes[relPath]; ok && existingHash == hash {
		return nil, "", false, nil
	}

	return data, hash, true, nil
}

// buildMeta constructs metadata for a file being indexed.
func (idx *Indexer) buildMeta(relPath string) map[string]string {
	name := filepath.Base(relPath)
	meta := map[string]string{
		"source": "workspace",
		"path":   relPath,
		"title":  strings.TrimSuffix(name, filepath.Ext(name)),
	}
	if relPath == "MEMORY.md" || strings.HasPrefix(relPath, "memory/") || strings.HasPrefix(relPath, "memory"+string(filepath.Separator)) {
		meta["type"] = "memory"
	}
	return meta
}

// submitAndWait sends an embedding job to the worker and blocks until
// the result is available. Returns the embedding error, if any.
func (idx *Indexer) submitAndWait(ctx context.Context, relPath string, data []byte, hash string, meta map[string]string) error {
	resultCh := make(chan indexJobResult, 1)
	job := indexJob{
		ctx:      ctx,
		relPath:  relPath,
		data:     data,
		hash:     hash,
		meta:     meta,
		resultCh: resultCh,
	}

	select {
	case idx.workCh <- job:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case res := <-resultCh:
		return res.err
	case <-ctx.Done():
		return ctx.Err()
	}
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
