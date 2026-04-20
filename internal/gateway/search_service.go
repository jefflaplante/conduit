package gateway

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/searchdb"
	"conduit/internal/sessions"
	vecgoservice "conduit/internal/vecgo"
)

// SearchService owns the FTS5 indexer/searcher/watcher, the dedicated
// search.db, its attached indexers (beads, brain, messages), and the optional
// vector/semantic search subsystem. These were previously inlined into the
// Gateway struct; centralising them here lets Gateway stay focused on request
// routing and channel orchestration.
//
// Fields are exported so sibling files in the gateway package (and tests) can
// continue to touch the underlying subsystems directly. Cross-package
// consumers should go through the `types.SearchService` interface value
// (backed by FTSSearcher) that Gateway wires into ToolServices.
//
// SearchService owns background work (fsnotify watcher, periodic re-index,
// vector indexer goroutine, async message syncer), so it provides Start/Stop
// lifecycle methods.
type SearchService struct {
	logger *slog.Logger

	// WorkspaceDir is the filesystem root used for FTS5 indexing (workspace
	// Markdown corpus) and for the vector indexer's file walker. Stored on
	// the service because Start's periodic re-index goroutine needs it and
	// because the vector indexer's configured WorkspaceDir is derived from
	// the same value.
	WorkspaceDir string

	// FTSIndexer writes workspace file content (and, indirectly, message
	// history) into the FTS5 virtual tables.
	FTSIndexer *fts.Indexer

	// FTSSearcher runs MATCH queries against FTS5 for document, message,
	// and beads search. This is the concrete implementer of the
	// `types.SearchService` interface that ToolServices wires into the
	// tool registry.
	FTSSearcher *fts.Searcher

	// FTSWatcher is an fsnotify watcher that incrementally re-indexes .md
	// files on change. Nil if the workspace dir is unreachable at startup.
	FTSWatcher *fts.Watcher

	// SearchDB is the dedicated search database (separate from gateway.db)
	// that holds the FTS5 indices. Nil when search is disabled or failed
	// to open — in that case FTS operations fall back to gateway.db.
	SearchDB *searchdb.SearchDB

	// BeadsIndexer mirrors the beads CLI's issue store into search.db for
	// full-text queries over issue titles/bodies. Nil when SearchDB is nil.
	BeadsIndexer *searchdb.BeadsIndexer

	// BrainIndexer mirrors brain LTM entries into search.db. Wired in after
	// construction via WireBrainIndexer (brain service is built later).
	BrainIndexer *searchdb.BrainIndexer

	// MessageSyncer performs full and incremental sync of messages from
	// gateway.db into search.db's FTS indices. Nil when SearchDB is nil.
	MessageSyncer *searchdb.MessageSyncer

	// AsyncMsgSyncer wraps MessageSyncer in a buffered non-blocking channel
	// so message-add callbacks don't block the hot session write path.
	// Closed during Stop().
	AsyncMsgSyncer *searchdb.AsyncMessageSyncer

	// VectorService is the optional vector/semantic search engine (vecgo +
	// embedding provider). Nil when vector search is disabled or no
	// embedder is available.
	VectorService *vecgoservice.Service

	// VectorIndexer walks the workspace and embeds files into VectorService.
	// Started asynchronously from NewSearchService (after an embedder
	// readiness probe) and stopped during Stop().
	VectorIndexer *vecgoservice.Indexer
}

// NewSearchService constructs the search subsystem. It opens the dedicated
// search database (or falls back to gateway.db), builds the FTS indexer /
// searcher / watcher, sets up the beads and message syncers, wires
// message-add callbacks on the session store, runs an initial workspace
// indexing pass, and — if vector search is enabled — builds the vector
// service and kicks off the indexer goroutine once the embedder probe
// succeeds.
//
// Brain indexing (which needs a brain.Brain value that is built later)
// is attached via WireBrainIndexer once brainService exists.
//
// The returned SearchService is ready to Start; Start attaches the
// fsnotify watcher goroutine and the periodic safety-net re-index loop to
// the supplied context.
func NewSearchService(cfg *config.Config, logger *slog.Logger, sessionStore *sessions.Store) (*SearchService, error) {
	workspaceDir := cfg.Workspace.ContextDir
	if workspaceDir == "" {
		workspaceDir = "./workspace"
	}

	svc := &SearchService{
		logger:       logger,
		WorkspaceDir: workspaceDir,
	}

	// Initialize search database (separate from gateway.db).
	// This consolidates FTS5 indices into search.db for better separation
	// of concerns.
	var ftsIndexer *fts.Indexer
	var ftsSearcher *fts.Searcher

	if cfg.Search.IsEnabled() {
		searchDBPath := cfg.Search.Path // Empty means derive from gateway.db path
		sdb, err := searchdb.NewSearchDB(searchDBPath, cfg.Database.Path, sessionStore.DB())
		if err != nil {
			logger.Warn("failed to initialize search database, falling back to gateway.db", "error", err)
			// Fall back to using gateway.db for FTS (backward compatibility)
			ftsIndexer = fts.NewIndexer(sessionStore.DB(), workspaceDir)
			ftsSearcher = fts.NewSearcher(sessionStore.DB())
		} else {
			svc.SearchDB = sdb

			// Use search.db for FTS operations
			ftsIndexer = fts.NewIndexer(sdb.DB(), workspaceDir)
			ftsSearcher = fts.NewSearcher(sdb.DB())

			// Initialize beads indexer
			beadsDir := cfg.Search.BeadsDir
			if beadsDir == "" {
				beadsDir = ".beads"
			}
			svc.BeadsIndexer = searchdb.NewBeadsIndexer(sdb.DB(), beadsDir)

			// Initialize message syncer and wire callbacks
			svc.MessageSyncer = searchdb.NewMessageSyncer(sdb.DB(), sessionStore.DB())
			svc.AsyncMsgSyncer = searchdb.NewAsyncMessageSyncer(svc.MessageSyncer, 256)
			sessionStore.SetMessageCallbacks(
				svc.AsyncMsgSyncer.MessageAddedCallback(),  // non-blocking
				svc.MessageSyncer.SessionClearedCallback(), // session clear stays synchronous (rare)
			)

			// Run initial sync operations
			indexCtx, indexCancel := context.WithTimeout(context.Background(), 60*time.Second)

			// Sync messages from gateway.db to search.db
			if err := svc.MessageSyncer.FullSync(indexCtx); err != nil {
				logger.Warn("initial message sync failed", "error", err)
			}

			// Index beads
			if err := svc.BeadsIndexer.IndexBeads(indexCtx); err != nil {
				logger.Warn("initial beads indexing failed", "error", err)
			}

			indexCancel()
			logger.Info("search database initialized", "path", sdb.Path())
		}
	} else {
		// Search disabled - use gateway.db (backward compatibility)
		ftsIndexer = fts.NewIndexer(sessionStore.DB(), workspaceDir)
		ftsSearcher = fts.NewSearcher(sessionStore.DB())
		logger.Debug("search database disabled, using gateway.db for FTS")
	}

	svc.FTSIndexer = ftsIndexer
	svc.FTSSearcher = ftsSearcher

	// Run initial workspace indexing
	indexCtx, indexCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := ftsIndexer.IndexWorkspace(indexCtx); err != nil {
		logger.Warn("initial FTS5 workspace indexing failed", "error", err)
	}
	indexCancel()

	// Start fsnotify watcher for incremental FTS indexing
	if workspaceDir != "" {
		w, err := fts.NewWatcher(ftsIndexer, workspaceDir)
		if err != nil {
			logger.Warn("FTS file watcher failed to start, falling back to polling", "error", err)
		} else {
			svc.FTSWatcher = w
		}
	}

	// Initialize vector/semantic search service.
	// Batteries-included: auto-enables when Ollama is available at localhost,
	// even without vector.enabled=true in config. Set vector.enabled=false
	// explicitly only if you need to force-disable it.
	if cfg.Vector.Enabled || shouldAutoEnableVecgo(cfg.Vector, logger) {
		emb, providerName := resolveEmbedder(cfg.Vector, logger)
		if emb == nil {
			if cfg.Vector.Enabled {
				logger.Warn("vector search disabled: no embedding provider available", "provider", providerName)
			}
		} else {
			vectorDBPath := cfg.Vector.Path
			if vectorDBPath == "" {
				vectorDBPath = config.DeriveVectorDBPath(cfg.Database.Path)
			}
			vecCfg := vecgoservice.Config{
				DBPath:    vectorDBPath,
				ChunkSize: cfg.Vector.ChunkSize,
				EmbedDims: emb.Dimensions(),
				Embedder:  emb,
			}
			vectorSvc, vecErr := vecgoservice.NewService(vecCfg)
			if vecErr != nil {
				logger.Warn("failed to initialize vector search, continuing without", "error", vecErr)
			} else {
				svc.VectorService = vectorSvc
				embedTimeout := time.Duration(cfg.Vector.EmbedTimeout) * time.Second
				embedPacing := time.Duration(cfg.Vector.EmbedPacing) * time.Second
				if cfg.Vector.EmbedPacing <= 0 {
					embedPacing = 2 * time.Second
				}
				svc.VectorIndexer = vecgoservice.NewIndexer(vectorSvc, vecgoservice.IndexerConfig{
					WorkspaceDir: workspaceDir,
					DBPath:       vectorSvc.DBPath(),
					PollInterval: 30 * time.Second,
					EmbedTimeout: embedTimeout,
					EmbedPacing:  embedPacing,
				})
				logger.Info("vector search initialized", "provider", providerName, "dims", emb.Dimensions(), "path", vectorDBPath)

				// Async: probe embedder readiness, then start indexer.
				// This avoids blocking gateway startup on slow Ollama model loading.
				go func() {
					type pinger interface {
						Ping(ctx context.Context) error
					}
					if p, ok := emb.(pinger); ok {
						probeCtx, probeCancel := context.WithTimeout(context.Background(), 30*time.Second)
						if err := p.Ping(probeCtx); err != nil {
							logger.Warn("vector embedder readiness probe failed; indexing deferred to poll cycle",
								"provider", providerName, "error", err)
						} else {
							logger.Info("vector embedder readiness probe passed", "provider", providerName)
						}
						probeCancel()
					}

					if err := svc.VectorIndexer.Start(context.Background()); err != nil {
						logger.Error("vector indexer failed to start", "error", err)
					}
				}()
			}
		}
	}

	return svc, nil
}

// WireBrainIndexer attaches the brain LTM → search.db indexer once the
// brain service is available. A no-op when SearchDB is nil (brain indexer
// requires the dedicated search database). Runs an initial IndexBrain pass
// synchronously; errors are logged, not returned.
func (s *SearchService) WireBrainIndexer(ctx context.Context, brainDB *sql.DB) {
	if s.SearchDB == nil || brainDB == nil {
		return
	}
	s.BrainIndexer = searchdb.NewBrainIndexer(s.SearchDB.DB(), brainDB)
	if err := s.BrainIndexer.IndexBrain(ctx); err != nil {
		s.logger.Warn("initial brain FTS5 index failed", "error", err)
	}
}

// Start launches the FTS file watcher and the periodic safety-net re-index
// goroutine. Both goroutines exit when ctx is cancelled. Safe to call when
// optional components (watcher, indexer) are nil — their goroutines are
// skipped.
func (s *SearchService) Start(ctx context.Context) error {
	// Start fsnotify watcher for real-time FTS indexing of .md file changes
	if s.FTSWatcher != nil {
		go s.FTSWatcher.Run(ctx)
		s.logger.Info("FTS file watcher started")
	}

	// Periodic safety-net re-indexing (every 30 minutes).
	// The fsnotify watcher handles real-time .md changes; this catches
	// anything it might miss (e.g., files changed while watcher was down)
	// plus beads/messages.
	if s.FTSIndexer != nil {
		go func() {
			ticker := time.NewTicker(30 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Full workspace re-index as safety net
					if err := s.FTSIndexer.IndexWorkspace(ctx); err != nil {
						s.logger.Warn("FTS5 periodic re-index failed", "error", err)
					}

					// Re-index beads if available
					if s.BeadsIndexer != nil {
						if err := s.BeadsIndexer.IndexBeads(ctx); err != nil {
							s.logger.Warn("beads periodic re-index failed", "error", err)
						}
					}

					// Re-index brain LTM if available
					if s.BrainIndexer != nil {
						if err := s.BrainIndexer.IndexBrain(ctx); err != nil {
							s.logger.Warn("brain periodic re-index failed", "error", err)
						}
					}

					// Run incremental message sync as safety net
					if s.MessageSyncer != nil {
						if err := s.MessageSyncer.IncrementalSync(ctx); err != nil {
							s.logger.Warn("message incremental sync failed", "error", err)
						}
					}
				}
			}
		}()
	}

	return nil
}

// Stop drains the async message syncer, stops the vector indexer, and
// closes the vector service. Safe to call when any component is nil.
//
// This drains AsyncMsgSyncer first (so no more writes land after search
// DB closes), then stops the vector indexer, then closes the vector
// service. Gateway.Stop calls DrainAsyncSyncer early (before MCP/MQTT
// shutdown) and StopVector late (after MQTT) to preserve the original
// inline shutdown ordering; Stop itself is equivalent to those two calls
// in sequence for callers that don't need the intermediate hooks.
func (s *SearchService) Stop() error {
	s.DrainAsyncSyncer()
	s.StopVector()
	return nil
}

// DrainAsyncSyncer closes the async message syncer, flushing any pending
// message-add writes into search.db. Must be called before the search DB
// is closed (or before any path that could race on its backing handle).
// Safe to call when AsyncMsgSyncer is nil.
func (s *SearchService) DrainAsyncSyncer() {
	if s.AsyncMsgSyncer != nil {
		s.AsyncMsgSyncer.Close()
	}
}

// StopVector stops the vector indexer goroutine and closes the underlying
// vector service (database handle + embedder). Safe to call when either
// component is nil.
func (s *SearchService) StopVector() {
	if s.VectorIndexer != nil {
		s.VectorIndexer.Stop()
	}

	if s.VectorService != nil {
		if err := s.VectorService.Close(); err != nil {
			s.logger.Error("error closing vector service", "error", err)
		}
	}
}
