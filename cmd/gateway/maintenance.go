package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/database"
	"conduit/internal/maintenance"
	"conduit/internal/searchdb"

	"github.com/spf13/cobra"
)

var maintenanceCmd = &cobra.Command{
	Use:   "maintenance",
	Short: "Database maintenance operations",
	Long: `Run database maintenance tasks (session cleanup, search index repair,
database optimization) on demand.

Session cleanup deletes only automated sessions (key prefixes cron_,
heartbeat_, subagent_, test_ by default; see maintenance.prunable_prefixes)
whose last activity is older than the retention window, together with their
messages. Telegram and TUI sessions, and any other prefix, are never deleted.

The database is database.path from --config, or --database. It is safe to run
while the gateway is running: deletes go in short batched transactions.

There is no built-in schedule: run 'conduit maintenance run' from a system
timer (cron, systemd) to run maintenance periodically.`,
}

var maintenanceRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run maintenance tasks immediately",
	Long: `Execute all maintenance tasks immediately: session_cleanup, fts_rebuild,
then database_maintenance. Before deleting anything a VACUUM INTO backup (0600) is
written next to the database unless --no-backup is given. Use --dry-run to see
exactly what would be deleted.`,
	Example: `  conduit maintenance run --dry-run
  conduit maintenance run
  conduit maintenance run --retention-days 14 --database /var/lib/conduit/gateway.db`,
	RunE: runMaintenanceTasks,
}

var maintenanceRunTaskCmd = &cobra.Command{
	Use:   "run-task [task-name]",
	Short: "Run a specific maintenance task",
	Long: `Execute a specific maintenance task by name: session_cleanup, fts_rebuild or
database_maintenance.

fts_rebuild repairs gateway.db's messages_fts search index: it deletes index
rows whose message is gone, has changed or is indexed twice, and indexes
messages that have no row. With --dry-run it only reports the counts.`,
	Example: `  conduit maintenance run-task fts_rebuild --dry-run
  conduit maintenance run-task fts_rebuild`,
	Args: cobra.ExactArgs(1),
	RunE: runSpecificMaintenanceTask,
}

var maintenanceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show database size, sessions per prefix and what a run would prune",
	Long: `Show the database path and size, session and message counts per session-key
prefix, what 'maintenance run' would delete now, and whether the messages_fts
search index matches the messages table. Read-only. No run history is kept.`,
	RunE: showMaintenanceStatus,
}

var maintenanceConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show maintenance configuration",
	Long:  `Display the effective maintenance configuration (config file 'maintenance' section over built-in defaults).`,
	RunE:  showMaintenanceConfig,
}

// Command flags
var (
	maintenanceJSONOutput    bool
	maintenanceVerbose       bool
	maintenanceForce         bool
	maintenanceDryRun        bool
	maintenanceNoBackup      bool
	maintenanceRetentionDays int
)

func init() {
	// Add maintenance subcommands
	maintenanceCmd.AddCommand(maintenanceRunCmd)
	maintenanceCmd.AddCommand(maintenanceRunTaskCmd)
	maintenanceCmd.AddCommand(maintenanceStatusCmd)
	maintenanceCmd.AddCommand(maintenanceConfigCmd)

	// Add flags
	maintenanceCmd.PersistentFlags().BoolVar(&maintenanceJSONOutput, "json", false, "Output results in JSON format")
	maintenanceCmd.PersistentFlags().BoolVar(&maintenanceVerbose, "verbose", false, "Verbose output")

	for _, c := range []*cobra.Command{maintenanceRunCmd, maintenanceRunTaskCmd} {
		// --force is a no-op kept for existing scripts: manual runs always
		// execute (conduit-3kgo; there is no maintenance window any more).
		c.Flags().BoolVar(&maintenanceForce, "force", false, "No effect (manual runs always execute)")
		_ = c.Flags().MarkDeprecated("force", "manual runs always execute; the flag has no effect")

		c.Flags().BoolVar(&maintenanceDryRun, "dry-run", false, "Report what would be deleted/optimised; change nothing")
		c.Flags().BoolVar(&maintenanceNoBackup, "no-backup", false, "Skip the VACUUM INTO backup before deleting or vacuuming")
	}
	for _, c := range []*cobra.Command{maintenanceRunCmd, maintenanceRunTaskCmd, maintenanceStatusCmd} {
		c.Flags().IntVar(&maintenanceRetentionDays, "retention-days", 0, "Override maintenance.retention_days (>= 1)")
	}

	// Add to root command
	rootCmd.AddCommand(maintenanceCmd)
}

// maintenanceEnv is what every maintenance subcommand resolves first.
type maintenanceEnv struct {
	cfg    *config.Config // nil when running on --database with no config file
	dbPath string
	mcfg   maintenance.Config
	source string // where the maintenance settings came from
}

// loadMaintenanceEnv resolves the gateway database exactly like the server
// and the token/pairing CLIs (database.path from --config via
// auth.ResolveDatabasePath; an explicit global --database wins) and the
// effective maintenance settings.
func loadMaintenanceEnv() (*maintenanceEnv, error) {
	env := &maintenanceEnv{}
	dbOverride := rootCmd.PersistentFlags().Changed("database") && dbPath != ""
	cfg, err := loadExistingConfig(cfgFile)
	if err != nil {
		// With an explicit --database, a missing config file just means
		// built-in defaults; any other config error is fatal.
		if _, statErr := os.Stat(cfgFile); !dbOverride || !errors.Is(statErr, fs.ErrNotExist) {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "Warning: %v; using built-in maintenance defaults\n", err)
		cfg = nil
	}
	env.cfg = cfg
	switch {
	case dbOverride:
		env.dbPath = dbPath
		if cfg != nil {
			cfg.Database.Path = dbPath // derived paths (search.db) follow, as in the server
		}
	default:
		env.dbPath = auth.ResolveDatabasePath(cfg)
	}
	if env.dbPath == "" {
		return nil, fmt.Errorf("gateway config %q has no database.path; pass --database explicitly", cfgFile)
	}

	env.mcfg, env.source, err = loadMaintenanceConfig(cfg)
	if err != nil {
		return nil, err
	}
	return env, nil
}

// loadMaintenanceConfig returns the effective maintenance configuration: the
// built-in defaults, overlaid with the config file's optional "maintenance"
// section (conduit-2cxu), then the command-line flags.
func loadMaintenanceConfig(cfg *config.Config) (maintenance.Config, string, error) {
	m := maintenance.DefaultConfig()
	source := "built-in defaults"
	if cfg != nil {
		mc := cfg.Maintenance
		if mc.RetentionDays != 0 || len(mc.PrunablePrefixes) != 0 || mc.BatchSize != 0 || mc.BackupDir != "" {
			source = "config file " + cfgFile + " (maintenance section) over built-in defaults"
		}
		if mc.RetentionDays > 0 {
			m.Sessions.RetentionDays = mc.RetentionDays
		}
		if mc.BatchSize > 0 {
			m.Sessions.BatchSize = mc.BatchSize
		}
		prefixes, err := config.NormalizePrunablePrefixes(mc.PrunablePrefixes)
		if err != nil {
			return m, "", err
		}
		m.Sessions.PrunablePrefixes = prefixes
		m.Sessions.BackupDir = mc.BackupDir
		m.Database.BackupDir = mc.BackupDir
	}

	if maintenanceRetentionDays != 0 {
		if maintenanceRetentionDays < 1 {
			return m, "", fmt.Errorf("--retention-days must be at least 1 (got %d)", maintenanceRetentionDays)
		}
		m.Sessions.RetentionDays = maintenanceRetentionDays
	}
	m.Sessions.DryRun = maintenanceDryRun
	m.Database.DryRun = maintenanceDryRun
	if maintenanceNoBackup {
		m.Sessions.BackupBeforePrune = false
		m.Database.BackupBeforeVacuum = false
	}
	return m, source, nil
}

// initDatabase opens the gateway database at path with the server's DSN
// (WAL, busy_timeout, foreign keys, IMMEDIATE transactions) and pool limits.
// It does not create a missing database or run migrations: it must be an
// existing gateway.db.
func initDatabase(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database %s: %w (use --config or --database to point at the gateway's database)", path, err)
	}
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('sessions', 'messages')`).Scan(&n); err != nil || n != 2 {
		db.Close()
		if err == nil {
			err = errors.New("no sessions/messages tables")
		}
		return nil, fmt.Errorf("%s is not a gateway database: %w", path, err)
	}
	return db, nil
}

// openSearchIndex opens the existing search.db (the gateway's mirror of
// messages in messages_fts) so session cleanup can drop pruned sessions from
// it. Returns nil when search is disabled or the file does not exist.
func openSearchIndex(env *maintenanceEnv) *sql.DB {
	if env.cfg == nil || !env.cfg.Search.IsEnabled() {
		return nil
	}
	path := searchdb.ResolvePath(env.cfg.Search.Path, env.dbPath)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: search index %s: %v (it is rebuilt at gateway startup)\n", path, err)
		return nil
	}
	db.SetMaxOpenConns(1)
	return db
}

func maintenanceLogger() *log.Logger {
	if maintenanceVerbose && !maintenanceJSONOutput {
		return log.New(os.Stdout, "[Maintenance] ", log.LstdFlags)
	}
	if maintenanceVerbose {
		return log.New(os.Stderr, "[Maintenance] ", log.LstdFlags)
	}
	return log.New(io.Discard, "", 0)
}

// newMaintenanceScheduler opens the database and registers the tasks.
func newMaintenanceScheduler(env *maintenanceEnv) (*maintenance.Scheduler, func(), error) {
	db, err := initDatabase(env.dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	closers := []func(){func() { db.Close() }}
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	logger := maintenanceLogger()
	scheduler := maintenance.NewScheduler(db, env.mcfg, logger)

	sessionTask := maintenance.NewSessionCleanupTask(db, env.dbPath, env.mcfg.Sessions, logger)
	if !env.mcfg.Sessions.DryRun {
		if sdb := openSearchIndex(env); sdb != nil {
			sessionTask.SetSearchDB(sdb)
			closers = append(closers, func() { sdb.Close() })
		}
	}
	if err := scheduler.RegisterTask(sessionTask); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to register session cleanup task: %w", err)
	}
	// fts_rebuild runs after session cleanup and before VACUUM, so pages
	// the repair frees are reclaimed in the same run (conduit-3dad).
	if err := scheduler.RegisterTask(maintenance.NewFTSRebuildTask(db, env.mcfg.Database, logger)); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to register fts rebuild task: %w", err)
	}
	dbTask := maintenance.NewDatabaseMaintenanceTask(db, env.dbPath, env.mcfg.Database, logger)
	if err := scheduler.RegisterTask(dbTask); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to register database maintenance task: %w", err)
	}
	return scheduler, cleanup, nil
}

// runMaintenanceTasks executes all maintenance tasks
func runMaintenanceTasks(cmd *cobra.Command, args []string) error {
	env, err := loadMaintenanceEnv()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}
	scheduler, cleanup, err := newMaintenanceScheduler(env)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := scheduler.RunNow(ctx); err != nil {
		return fmt.Errorf("failed to run maintenance tasks: %w", err)
	}
	return displayMaintenanceResults(os.Stdout, env, scheduler.TaskNames(), scheduler.GetStatus())
}

// runSpecificMaintenanceTask executes a single maintenance task
func runSpecificMaintenanceTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]
	env, err := loadMaintenanceEnv()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}
	scheduler, cleanup, err := newMaintenanceScheduler(env)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := scheduler.RunTask(ctx, taskName); err != nil {
		return fmt.Errorf("failed to run maintenance task %s (tasks: %s): %w",
			taskName, strings.Join(scheduler.TaskNames(), ", "), err)
	}
	return displayMaintenanceResults(os.Stdout, env, []string{taskName}, scheduler.GetStatus())
}

// maintenanceNoScheduleNote is shown by `status`: nothing runs maintenance
// in the background and no run history is kept (conduit-3kgo).
const maintenanceNoScheduleNote = "Maintenance runs only when invoked ('conduit maintenance run' or 'run-task'); " +
	"there is no background schedule and no run history. Use a system timer to run it periodically."

// fileSize returns the size of path, or 0.
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// showMaintenanceStatus shows the database, its sessions per prefix and what
// a run would prune now. Read-only.
func showMaintenanceStatus(cmd *cobra.Command, args []string) error {
	env, err := loadMaintenanceEnv()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}
	db, err := initDatabase(env.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	rep, err := maintenance.PlanPrune(context.Background(), db, maintenance.RetentionPolicy{
		RetentionDays:    env.mcfg.Sessions.RetentionDays,
		PrunablePrefixes: env.mcfg.Sessions.PrunablePrefixes,
	}, time.Now())
	if err != nil {
		return err
	}
	fts, err := maintenance.PlanFTSRepair(context.Background(), db) // nil without messages_fts
	if err != nil {
		return err
	}
	size, wal := fileSize(env.dbPath), fileSize(env.dbPath+"-wal")

	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"database":   env.dbPath,
			"size_bytes": size,
			"wal_bytes":  wal,
			"message":    maintenanceNoScheduleNote,
			"config":     env.mcfg,
			"session_cleanup": map[string]interface{}{
				"cleanup_enabled": env.mcfg.Sessions.CleanupEnabled,
				"plan":            rep,
			},
			"fts_index": fts,
		})
	}

	out := os.Stdout
	fmt.Fprintf(out, "Database: %s\n", env.dbPath)
	fmt.Fprintf(out, "Size: %.1f MB (+ %.1f MB WAL); vacuum threshold %d MB\n",
		float64(size)/(1<<20), float64(wal)/(1<<20), env.mcfg.Database.VacuumThreshold)
	fmt.Fprintf(out, "Rows: %d sessions, %d messages\n\n", rep.TotalSessions, rep.TotalMessages)

	fmt.Fprintln(out, "Sessions per key prefix:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  PREFIX\tSESSIONS\tEMPTY\tMESSAGES\tLAST ACTIVITY (UTC)")
	for _, c := range rep.Inventory {
		fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%s\n", c.Prefix, c.Sessions, c.EmptySessions, c.Messages, fmtTime(c.Newest))
	}
	w.Flush()
	fmt.Fprintln(out)
	if !env.mcfg.Sessions.CleanupEnabled {
		fmt.Fprintln(out, "Session cleanup is disabled.")
	}
	printPruneReport(out, rep)
	switch {
	case fts == nil:
		fmt.Fprintln(out, "\nSearch index (messages_fts): not present")
	case fts.InSync():
		fmt.Fprintf(out, "\nSearch index (messages_fts): in sync (%d rows)\n", fts.IndexRows)
	default:
		fmt.Fprintf(out, "\nSearch index (messages_fts): %s\nRepair with 'conduit maintenance run-task fts_rebuild'.\n", fts.Summary())
	}
	fmt.Fprintf(out, "\nNote: %s\n", maintenanceNoScheduleNote)
	return nil
}

// showMaintenanceConfig displays the current maintenance configuration
func showMaintenanceConfig(cmd *cobra.Command, args []string) error {
	env, err := loadMaintenanceEnv()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}
	mc := env.mcfg

	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(mc)
	}

	fmt.Printf("Maintenance Configuration (%s):\n", env.source)
	fmt.Printf("  Database: %s\n", env.dbPath)

	fmt.Println("\nSession Cleanup:")
	fmt.Printf("  Cleanup Enabled: %t\n", mc.Sessions.CleanupEnabled)
	fmt.Printf("  Retention Days: %d\n", mc.Sessions.RetentionDays)
	fmt.Printf("  Prunable Prefixes: %s\n", strings.Join(mc.Sessions.PrunablePrefixes, ", "))
	fmt.Printf("  Protected (never pruned): %s and any other prefix\n", strings.Join(protectedPrefixes(), ", "))
	fmt.Printf("  Batch Size: %d sessions per transaction\n", mc.Sessions.BatchSize)
	fmt.Printf("  Backup Before Prune: %t\n", mc.Sessions.BackupBeforePrune)
	fmt.Printf("  Backup Dir: %s\n", orDefault(mc.Sessions.BackupDir, "(next to the database)"))

	fmt.Println("\nDatabase Configuration:")
	fmt.Printf("  Vacuum Enabled: %t\n", mc.Database.VacuumEnabled)
	fmt.Printf("  Vacuum Threshold: %d MB\n", mc.Database.VacuumThreshold)
	fmt.Printf("  Backup Before Vacuum: %t\n", mc.Database.BackupBeforeVacuum)
	fmt.Printf("  Optimize Indexes: %t\n", mc.Database.OptimizeIndexes)

	return nil
}

func protectedPrefixes() []string {
	return append([]string(nil), config.ProtectedSessionPrefixes...)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// printPruneReport renders a session-cleanup plan or result.
func printPruneReport(out io.Writer, rep *maintenance.PruneReport) {
	fmt.Fprintf(out, "Session retention: %d days (cutoff %s UTC); prunable prefixes: %s\n",
		rep.RetentionDays, fmtTime(rep.Cutoff), strings.Join(rep.PrunablePrefixes, ", "))

	title := "Deleted:"
	if rep.DryRun {
		title = "Would delete:"
	}
	fmt.Fprintln(out, title)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  PREFIX\tSESSIONS\tEMPTY\tMESSAGES\tOLDEST ACTIVITY\tNEWEST ACTIVITY")
	for _, c := range rep.Prune {
		fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%s\t%s\n", c.Prefix, c.Sessions, c.EmptySessions, c.Messages, fmtTime(c.Oldest), fmtTime(c.Newest))
	}
	fmt.Fprintf(w, "  total\t%d\t\t%d\t\t\n", rep.PruneSessions, rep.PruneMessages)
	w.Flush()

	fmt.Fprintf(out, "Kept although older than the cutoff (not prunable): %d sessions, %d messages\n",
		rep.KeptProtectedSessions, rep.KeptProtectedMessages)
	if len(rep.KeptProtected) > 0 {
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  PREFIX\tOLD SESSIONS\tOLD MESSAGES")
		for _, c := range rep.KeptProtected {
			fmt.Fprintf(w, "  %s\t%d\t%d\n", c.Prefix, c.Sessions, c.Messages)
		}
		w.Flush()
	}
	fmt.Fprintf(out, "Kept automated sessions: %d within retention, %d with unparseable timestamps\n",
		rep.KeptRecent, rep.KeptUnparseable)

	if rep.DryRun {
		return
	}
	if rep.BackupPath != "" {
		fmt.Fprintf(out, "Backup: %s\n", rep.BackupPath)
	}
	fmt.Fprintf(out, "Result: deleted %d sessions and %d messages in %d batches (%v)",
		rep.SessionsDeleted, rep.MessagesDeleted, rep.Batches, rep.ExecuteDuration.Round(time.Millisecond))
	if rep.SkippedChanged > 0 {
		fmt.Fprintf(out, "; %d skipped (active since planning)", rep.SkippedChanged)
	}
	fmt.Fprintln(out)
	if rep.SummariesDeleted+rep.MappingsDeleted+rep.SearchIndexDeleted > 0 {
		fmt.Fprintf(out, "Also removed: %d session summaries, %d Claude Code session mappings, %d search.db index rows\n",
			rep.SummariesDeleted, rep.MappingsDeleted, rep.SearchIndexDeleted)
	}
	for _, w := range rep.SearchIndexWarnings {
		fmt.Fprintf(out, "Warning: search index: %s (the gateway rebuilds it at startup)\n", w)
	}
}

// displayMaintenanceResults shows the results of the tasks in order.
func displayMaintenanceResults(out io.Writer, env *maintenanceEnv, order []string, status map[string]maintenance.TaskStatus) error {
	if maintenanceJSONOutput {
		results := make([]map[string]interface{}, 0, len(order))
		for _, name := range order {
			if st, ok := status[name]; ok {
				results = append(results, map[string]interface{}{"task": name, "status": st})
			}
		}
		return json.NewEncoder(out).Encode(map[string]interface{}{
			"database": env.dbPath,
			"dry_run":  maintenanceDryRun,
			"results":  results,
		})
	}

	fmt.Fprintf(out, "Database: %s\n", env.dbPath)
	if maintenanceDryRun {
		fmt.Fprintln(out, "DRY RUN: nothing is changed.")
	}
	failed := 0
	for _, name := range order {
		st, ok := status[name]
		if !ok {
			continue
		}
		r := st.LastResult
		state := "SUCCESS"
		if !r.Success {
			state = "FAILED"
			failed++
		}
		fmt.Fprintf(out, "\n== %s: %s (%v)\n%s\n", name, state, r.Duration.Round(time.Millisecond), r.Message)
		if r.SpaceReclaimed > 0 {
			fmt.Fprintf(out, "Space reclaimed: %.1f MB\n", float64(r.SpaceReclaimed)/(1<<20))
		}
		if rep, ok := r.Details.(*maintenance.PruneReport); ok {
			printPruneReport(out, rep)
		}
		if r.Error != nil {
			fmt.Fprintf(out, "Error: %v\n", r.Error)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d maintenance task(s) failed", failed)
	}
	return nil
}
