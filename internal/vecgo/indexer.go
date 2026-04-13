package vecgo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Indexer watches workspace memory files and indexes them into the vector
// database. It tracks file content hashes so that only new or changed files
// are re-embedded and re-indexed. Embedding is serialized through a single
// background worker with configurable pacing to avoid overwhelming CPU-based
// embedding models.
//
// Hash tracking is persisted to SQLite so that unchanged files are skipped
// across restarts. On startup, the indexer loads persisted hashes and runs
// a non-blocking background scan that only embeds changed/new files.
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

	// Persistent hash storage (nil = in-memory only)
	hashDB *sql.DB

	// Ignore rules loaded from .vectorignore
	ignore *ignoreRules

	// Guards against EnsureIndexed calls before Start()
	started atomic.Bool
}

// IndexerConfig configures the memory indexing pipeline.
type IndexerConfig struct {
	// WorkspaceDir is the root directory to scan for .md files.
	WorkspaceDir string

	// DBPath is the SQLite database path for persistent hash storage.
	// If empty, hashes are kept in-memory only (lost on restart).
	DBPath string

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

	idx := &Indexer{
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

	// Load .vectorignore rules
	if cfg.WorkspaceDir != "" {
		idx.ignore = loadIgnoreFile(filepath.Join(cfg.WorkspaceDir, ".vectorignore"))
	}

	// Initialize persistent hash storage
	if cfg.DBPath != "" {
		if err := idx.initHashDB(cfg.DBPath); err != nil {
			log.Printf("vecgo indexer: hash persistence unavailable (falling back to in-memory): %v", err)
		}
	}

	return idx
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

		// Skip files matched by .vectorignore
		if idx.ignore.isIgnored(relPath) {
			result.FilesSkipped++
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
			idx.deleteHashQuiet(ctx, path)
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

	if idx.ignore.isIgnored(relativePath) {
		return nil
	}

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
	idx.deleteHashQuiet(ctx, relativePath)
	return idx.svc.Save(ctx)
}

// EnsureIndexed checks if a workspace file is indexed and current.
// If stale or missing, queues it for background embedding (non-blocking).
// Safe to call from any goroutine. No-op before Start() is called.
func (idx *Indexer) EnsureIndexed(ctx context.Context, relativePath string) {
	if !idx.started.Load() {
		return
	}

	if idx.ignore.isIgnored(relativePath) {
		return
	}

	// Quick check under lock: is it already indexed and current?
	idx.mu.Lock()
	fullPath := filepath.Join(idx.workspaceDir, relativePath)
	_, _, changed, err := idx.checkFileChanged(fullPath, relativePath)
	idx.mu.Unlock()

	if err != nil || !changed {
		return
	}

	// File needs indexing — submit asynchronously
	go func() {
		if indexErr := idx.IndexFile(context.Background(), relativePath); indexErr != nil {
			log.Printf("vecgo indexer: on-demand index %s: %v", relativePath, indexErr)
		}
	}()
}

// Start begins the indexing pipeline. It launches the embed worker, loads
// persisted hashes, runs a non-blocking background scan for changed files,
// then starts the poll loop. Call Stop() to terminate background goroutines.
func (idx *Indexer) Start(ctx context.Context) error {
	// Launch the embed worker
	go idx.embedWorker(ctx)

	// Load persisted hashes (fast, no embedding)
	if idx.hashDB != nil {
		if err := idx.loadHashes(ctx); err != nil {
			log.Printf("vecgo indexer: failed to load persisted hashes: %v", err)
		} else {
			log.Printf("vecgo indexer: loaded %d persisted file hashes", len(idx.hashes))
		}
	}

	idx.started.Store(true)

	// Non-blocking background scan for changed/new files
	go idx.staggeredScan(ctx)

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
	if idx.hashDB != nil {
		idx.hashDB.Close()
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

		log.Printf("vecgo indexer: embedding started: %s (%d bytes)", job.relPath, len(job.data))
		embedStart := time.Now()

		// Per-file timeout ensures one slow embedding doesn't starve the rest.
		fileCtx, fileCancel := context.WithTimeout(job.ctx, idx.embedTimeout)
		err := idx.svc.Index(fileCtx, job.relPath, string(job.data), job.meta)
		fileCancel()

		embedDur := time.Since(embedStart)

		if err != nil {
			log.Printf("vecgo indexer: embedding failed: %s after %v: %v", job.relPath, embedDur, err)
			job.resultCh <- indexJobResult{err: fmt.Errorf("index %s: %w", job.relPath, err)}
		} else {
			log.Printf("vecgo indexer: embedding complete: %s in %v", job.relPath, embedDur)
			// Persist vectors to SQLite BEFORE recording the hash.
			// This ensures the vectors table and file_hashes stay in sync —
			// if Save fails, the hash is not recorded and the file will be
			// re-indexed on the next scan.
			if saveErr := idx.svc.Save(job.ctx); saveErr != nil {
				log.Printf("vecgo indexer: vector save failed for %s: %v", job.relPath, saveErr)
				job.resultCh <- indexJobResult{err: fmt.Errorf("save after index %s: %w", job.relPath, saveErr)}
				continue
			}
			idx.hashes[job.relPath] = job.hash
			// Persist hash to SQLite
			if idx.hashDB != nil {
				if persistErr := idx.persistHash(job.ctx, job.relPath, job.hash); persistErr != nil {
					log.Printf("vecgo indexer: failed to persist hash for %s: %v", job.relPath, persistErr)
				} else {
					log.Printf("vecgo indexer: hash persisted: %s", job.relPath)
				}
			}
			job.resultCh <- indexJobResult{}
		}
	}
}

// staggeredScan walks all .md files and queues only changed/new files for
// embedding via the embed worker. This runs as a background goroutine so
// Start() returns immediately. The embed worker's pacing naturally staggers
// the embedding work.
func (idx *Indexer) staggeredScan(ctx context.Context) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.workspaceDir == "" {
		return
	}

	// Collect all .md files
	var mdFiles []string
	err := filepath.WalkDir(idx.workspaceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if err != nil {
		log.Printf("vecgo indexer: staggered scan walk failed: %v", err)
		return
	}

	var indexed, skipped, errors int
	currentPaths := make(map[string]bool, len(mdFiles))

	for _, fullPath := range mdFiles {
		if ctx.Err() != nil {
			log.Printf("vecgo indexer: staggered scan cancelled")
			return
		}

		relPath, relErr := filepath.Rel(idx.workspaceDir, fullPath)
		if relErr != nil {
			continue
		}

		if idx.ignore.isIgnored(relPath) {
			skipped++
			continue
		}

		currentPaths[relPath] = true

		data, hash, changed, checkErr := idx.checkFileChanged(fullPath, relPath)
		if checkErr != nil {
			log.Printf("vecgo indexer: staggered scan error checking %s: %v", relPath, checkErr)
			errors++
			continue
		}
		if !changed {
			skipped++
			continue
		}

		meta := idx.buildMeta(relPath)
		if embedErr := idx.submitAndWait(ctx, relPath, data, hash, meta); embedErr != nil {
			log.Printf("vecgo indexer: staggered scan error indexing %s: %v", relPath, embedErr)
			errors++
			continue
		}
		indexed++
	}

	// Remove stale entries
	var removed int
	for path := range idx.hashes {
		if !currentPaths[path] {
			if removeErr := idx.svc.Remove(ctx, path); removeErr != nil {
				log.Printf("vecgo indexer: failed to remove stale entry %s: %v", path, removeErr)
			} else {
				removed++
			}
			delete(idx.hashes, path)
			idx.deleteHashQuiet(ctx, path)
		}
	}

	// Save if anything changed
	if indexed > 0 || removed > 0 {
		if saveErr := idx.svc.Save(ctx); saveErr != nil {
			log.Printf("vecgo indexer: staggered scan save failed: %v", saveErr)
		}
	}

	log.Printf("vecgo indexer: startup scan complete: %d indexed, %d skipped, %d removed, %d errors",
		indexed, skipped, removed, errors)
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
	case <-idx.stopCh:
		return fmt.Errorf("indexer stopped")
	}

	select {
	case res := <-resultCh:
		return res.err
	case <-ctx.Done():
		return ctx.Err()
	case <-idx.stopCh:
		return fmt.Errorf("indexer stopped")
	}
}

// --- Persistent hash storage ---

// initHashDB opens a SQLite connection for the file_hashes table.
func (idx *Indexer) initHashDB(dbPath string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode%%3Dwal&_pragma=busy_timeout%%3D5000&_pragma=synchronous%%3Dnormal", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open hash db: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS file_hashes (
		path TEXT PRIMARY KEY,
		hash TEXT NOT NULL,
		indexed_at TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		return fmt.Errorf("create file_hashes table: %w", err)
	}

	idx.hashDB = db
	return nil
}

// loadHashes populates the in-memory hash map from SQLite.
func (idx *Indexer) loadHashes(ctx context.Context) error {
	rows, err := idx.hashDB.QueryContext(ctx, "SELECT path, hash FROM file_hashes")
	if err != nil {
		return fmt.Errorf("query file_hashes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			return fmt.Errorf("scan file_hashes row: %w", err)
		}
		idx.hashes[path] = hash
	}
	return rows.Err()
}

// persistHash writes a file hash to SQLite.
func (idx *Indexer) persistHash(ctx context.Context, relPath, hash string) error {
	_, err := idx.hashDB.ExecContext(ctx,
		"INSERT OR REPLACE INTO file_hashes (path, hash, indexed_at) VALUES (?, ?, ?)",
		relPath, hash, time.Now().UTC().Format(time.RFC3339))
	return err
}

// deleteHash removes a file hash from SQLite.
func (idx *Indexer) deleteHash(ctx context.Context, relPath string) error {
	_, err := idx.hashDB.ExecContext(ctx, "DELETE FROM file_hashes WHERE path = ?", relPath)
	return err
}

// deleteHashQuiet removes a persisted hash, logging errors without returning them.
func (idx *Indexer) deleteHashQuiet(ctx context.Context, relPath string) {
	if idx.hashDB == nil {
		return
	}
	if err := idx.deleteHash(ctx, relPath); err != nil {
		log.Printf("vecgo indexer: failed to delete hash for %s: %v", relPath, err)
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
