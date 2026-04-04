package fts

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors a workspace directory for .md file changes and triggers
// incremental FTS re-indexing via the Indexer.
type Watcher struct {
	indexer      *Indexer
	workspaceDir string
	fsw          *fsnotify.Watcher
}

// NewWatcher creates a Watcher that monitors workspaceDir for markdown file changes.
// It recursively adds all existing subdirectories to the watch list.
func NewWatcher(indexer *Indexer, workspaceDir string) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		indexer:      indexer,
		workspaceDir: workspaceDir,
		fsw:          fsw,
	}

	// Watch the root and all subdirectories
	if err := w.addRecursive(workspaceDir); err != nil {
		fsw.Close()
		return nil, err
	}

	return w, nil
}

// Run processes filesystem events until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	defer w.fsw.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(ctx, event)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			log.Printf("FTS watcher error: %v", err)
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, event fsnotify.Event) {
	// If a directory was created, watch it too
	if event.Has(fsnotify.Create) {
		if isDir(event.Name) {
			w.fsw.Add(event.Name)
			return
		}
	}

	// Only process .md files
	if !isMarkdown(event.Name) {
		return
	}

	relPath, err := filepath.Rel(w.workspaceDir, event.Name)
	if err != nil {
		return
	}

	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		if err := w.indexer.IndexFile(ctx, relPath); err != nil {
			log.Printf("FTS watcher: index %s: %v", relPath, err)
		}
	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		if err := w.indexer.RemoveFile(ctx, relPath); err != nil {
			log.Printf("FTS watcher: remove %s: %v", relPath, err)
		}
	}
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.fsw.Add(path)
		}
		return nil
	})
}

func isMarkdown(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
