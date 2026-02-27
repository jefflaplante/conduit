package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"conduit/internal/maintenance"

	"github.com/spf13/cobra"
)

var maintenanceCmd = &cobra.Command{
	Use:   "maintenance",
	Short: "Database maintenance operations",
	Long:  `Manage database maintenance tasks including cleanup, optimization, and scheduling.`,
}

var maintenanceRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run maintenance tasks immediately",
	Long:  `Execute all configured maintenance tasks immediately, bypassing the scheduler.`,
	RunE:  runMaintenanceTasks,
}

var maintenanceRunTaskCmd = &cobra.Command{
	Use:   "run-task [task-name]",
	Short: "Run a specific maintenance task",
	Long:  `Execute a specific maintenance task by name.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runSpecificMaintenanceTask,
}

var maintenanceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show maintenance task status",
	Long:  `Display the current status of all maintenance tasks including last run times and results.`,
	RunE:  showMaintenanceStatus,
}

var maintenanceConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show maintenance configuration",
	Long:  `Display the current maintenance configuration settings.`,
	RunE:  showMaintenanceConfig,
}

// Command flags
var (
	maintenanceJSONOutput bool
	maintenanceVerbose    bool
	maintenanceForce      bool
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

	maintenanceRunCmd.Flags().BoolVar(&maintenanceForce, "force", false, "Force run even outside maintenance window")
	maintenanceRunTaskCmd.Flags().BoolVar(&maintenanceForce, "force", false, "Force run even outside maintenance window")

	// Add to root command
	rootCmd.AddCommand(maintenanceCmd)
}

// runMaintenanceTasks executes all maintenance tasks
func runMaintenanceTasks(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := loadMaintenanceConfig()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}

	// Initialize database
	db, err := initDatabase()
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Create logger
	logger := log.New(os.Stdout, "[Maintenance] ", log.LstdFlags)
	if !maintenanceVerbose {
		logger.SetOutput(os.Stderr) // Send logs to stderr for clean JSON output
	}

	// Create scheduler
	scheduler := maintenance.NewScheduler(db, config, logger)

	// Register tasks
	err = registerMaintenanceTasks(scheduler, db, config, logger)
	if err != nil {
		return fmt.Errorf("failed to register maintenance tasks: %w", err)
	}

	// Run tasks
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	err = scheduler.RunNow(ctx)
	if err != nil {
		return fmt.Errorf("failed to run maintenance tasks: %w", err)
	}

	// Get and display results
	status := scheduler.GetStatus()
	return displayMaintenanceResults(status)
}

// runSpecificMaintenanceTask executes a single maintenance task
func runSpecificMaintenanceTask(cmd *cobra.Command, args []string) error {
	taskName := args[0]

	// Load configuration
	config, err := loadMaintenanceConfig()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}

	// Initialize database
	db, err := initDatabase()
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// Create logger
	logger := log.New(os.Stdout, "[Maintenance] ", log.LstdFlags)
	if !maintenanceVerbose {
		logger.SetOutput(os.Stderr)
	}

	// Create scheduler
	scheduler := maintenance.NewScheduler(db, config, logger)

	// Register tasks
	err = registerMaintenanceTasks(scheduler, db, config, logger)
	if err != nil {
		return fmt.Errorf("failed to register maintenance tasks: %w", err)
	}

	// Run specific task
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	err = scheduler.RunTask(ctx, taskName)
	if err != nil {
		return fmt.Errorf("failed to run maintenance task %s: %w", taskName, err)
	}

	// Get and display results
	status := scheduler.GetStatus()
	if taskStatus, exists := status[taskName]; exists {
		return displayTaskResult(taskName, taskStatus)
	}

	return fmt.Errorf("task %s not found", taskName)
}

// showMaintenanceStatus displays the current status of all maintenance tasks
func showMaintenanceStatus(cmd *cobra.Command, args []string) error {
	// This would typically connect to a running scheduler or read status from a file
	// For now, we'll show the configuration and indicate if maintenance is enabled

	config, err := loadMaintenanceConfig()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}

	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"enabled":  config.Enabled,
			"schedule": config.Schedule,
			"status":   "configuration_only",
			"message":  "Status from running scheduler not available in this implementation",
		})
	}

	fmt.Printf("Maintenance Configuration:\n")
	fmt.Printf("  Enabled: %t\n", config.Enabled)
	fmt.Printf("  Schedule: %s\n", config.Schedule)
	fmt.Printf("  Session Retention: %d days\n", config.Sessions.RetentionDays)
	fmt.Printf("  Database Vacuum Enabled: %t\n", config.Database.VacuumEnabled)
	fmt.Printf("  Vacuum Threshold: %d MB\n", config.Database.VacuumThreshold)

	fmt.Printf("\nNote: Real-time status requires a running scheduler instance.\n")
	fmt.Printf("Run 'conduit maintenance run' to execute tasks immediately.\n")

	return nil
}

// showMaintenanceConfig displays the current maintenance configuration
func showMaintenanceConfig(cmd *cobra.Command, args []string) error {
	config, err := loadMaintenanceConfig()
	if err != nil {
		return fmt.Errorf("failed to load maintenance configuration: %w", err)
	}

	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(config)
	}

	fmt.Println("Maintenance Configuration:")
	fmt.Printf("  Enabled: %t\n", config.Enabled)
	fmt.Printf("  Schedule: %s\n", config.Schedule)

	fmt.Println("\nSession Configuration:")
	fmt.Printf("  Retention Days: %d\n", config.Sessions.RetentionDays)
	fmt.Printf("  Cleanup Enabled: %t\n", config.Sessions.CleanupEnabled)
	fmt.Printf("  Summarize Old Sessions: %t\n", config.Sessions.SummarizeOld)
	fmt.Printf("  Summary Retention Days: %d\n", config.Sessions.SummaryRetentionDays)

	fmt.Println("\nDatabase Configuration:")
	fmt.Printf("  Vacuum Enabled: %t\n", config.Database.VacuumEnabled)
	fmt.Printf("  Vacuum Threshold: %d MB\n", config.Database.VacuumThreshold)
	fmt.Printf("  Backup Before Vacuum: %t\n", config.Database.BackupBeforeVacuum)
	fmt.Printf("  Optimize Indexes: %t\n", config.Database.OptimizeIndexes)

	fmt.Println("\nMaintenance Window:")
	fmt.Printf("  Start Hour: %d\n", config.Window.StartHour)
	fmt.Printf("  End Hour: %d\n", config.Window.EndHour)
	fmt.Printf("  Time Zone: %s\n", config.Window.TimeZone)

	return nil
}

// loadMaintenanceConfig loads the maintenance configuration
func loadMaintenanceConfig() (maintenance.Config, error) {
	// For now, return default configuration
	// In a real implementation, this would load from a config file
	return maintenance.DefaultConfig(), nil
}

// registerMaintenanceTasks registers all maintenance tasks with the scheduler
func registerMaintenanceTasks(scheduler *maintenance.Scheduler, db *sql.DB, config maintenance.Config, logger *log.Logger) error {
	// Register session cleanup task
	sessionTask := maintenance.NewSessionCleanupTask(db, config.Sessions, logger)
	if err := scheduler.RegisterTask(sessionTask); err != nil {
		return fmt.Errorf("failed to register session cleanup task: %w", err)
	}

	// Register database maintenance task
	dbTask := maintenance.NewDatabaseMaintenanceTask(db, "", config.Database, logger)
	if err := scheduler.RegisterTask(dbTask); err != nil {
		return fmt.Errorf("failed to register database maintenance task: %w", err)
	}

	return nil
}

// displayMaintenanceResults shows the results of maintenance task execution
func displayMaintenanceResults(status map[string]maintenance.TaskStatus) error {
	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(status)
	}

	fmt.Println("Maintenance Task Results:")
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATUS\tDURATION\tRECORDS\tMESSAGE")
	fmt.Fprintln(w, "----\t------\t--------\t-------\t-------")

	for name, taskStatus := range status {
		result := taskStatus.LastResult

		statusStr := "FAILED"
		if result.Success {
			statusStr = "SUCCESS"
		}

		duration := result.Duration.Round(time.Millisecond)
		records := fmt.Sprintf("%d", result.RecordsProcessed)
		if result.RecordsProcessed == 0 {
			records = "-"
		}

		message := result.Message
		if len(message) > 50 {
			message = message[:47] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%v\t%s\t%s\n", name, statusStr, duration, records, message)
	}

	w.Flush()

	// Show any errors
	for name, taskStatus := range status {
		if !taskStatus.LastResult.Success && taskStatus.LastResult.Error != nil {
			fmt.Printf("\nError in %s: %v\n", name, taskStatus.LastResult.Error)
		}
	}

	return nil
}

// displayTaskResult shows the result of a single task execution
func displayTaskResult(taskName string, taskStatus maintenance.TaskStatus) error {
	if maintenanceJSONOutput {
		return json.NewEncoder(os.Stdout).Encode(map[string]interface{}{
			"task":   taskName,
			"status": taskStatus,
		})
	}

	result := taskStatus.LastResult

	fmt.Printf("Task: %s\n", taskName)
	fmt.Printf("Status: %s\n", map[bool]string{true: "SUCCESS", false: "FAILED"}[result.Success])
	fmt.Printf("Duration: %v\n", result.Duration.Round(time.Millisecond))
	fmt.Printf("Message: %s\n", result.Message)

	if result.RecordsProcessed > 0 {
		fmt.Printf("Records Processed: %d\n", result.RecordsProcessed)
	}

	if result.SpaceReclaimed > 0 {
		fmt.Printf("Space Reclaimed: %.1f MB\n", float64(result.SpaceReclaimed)/(1024*1024))
	}

	if result.Error != nil {
		fmt.Printf("Error: %v\n", result.Error)
	}

	return nil
}

// initDatabase initializes the database connection
// This is a placeholder - in the real implementation this would use the existing database setup
func initDatabase() (*sql.DB, error) {
	// This would use the actual database configuration from the gateway
	// For now, return an error to indicate this needs integration
	return nil, fmt.Errorf("database initialization not implemented - needs integration with gateway database setup")
}
