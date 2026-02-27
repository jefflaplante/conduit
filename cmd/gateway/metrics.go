package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"conduit/internal/monitoring"

	"github.com/spf13/cobra"
)

var (
	metricsPort int
)

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Start the metrics dashboard HTTP server",
	Long: `Start a lightweight HTTP server that exposes gateway usage metrics,
routing statistics, and system health data.

Endpoints:
  GET /metrics        — Prometheus-compatible text format
  GET /metrics/json   — Full JSON metrics payload
  GET /metrics/health — System health summary
  GET /metrics/usage  — Usage breakdown by model
  GET /metrics/costs  — Cost breakdown and savings
  GET /metrics/routing — Routing statistics and clusters`,
	RunE: runMetricsServer,
}

func init() {
	metricsCmd.Flags().IntVar(&metricsPort, "metrics-port", 18790, "Port for the metrics HTTP server")
	rootCmd.AddCommand(metricsCmd)
}

func runMetricsServer(cmd *cobra.Command, args []string) error {
	collector := monitoring.NewDashboardCollector()

	// In standalone mode the dashboard serves whatever data sources are
	// available. When integrated into the main gateway process, the caller
	// would call the Set* methods to wire in live subsystems. Here we start
	// with system-level metrics only.

	addr := fmt.Sprintf(":%d", metricsPort)

	srv := &http.Server{
		Addr:         addr,
		Handler:      collector.Handler(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[Metrics] Received signal: %v, shutting down", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[Metrics] Shutdown error: %v", err)
		}
	}()

	log.Printf("[Metrics] Starting metrics dashboard on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("metrics server failed: %w", err)
	}

	_ = ctx // keep linter happy about ctx usage
	log.Println("[Metrics] Server stopped gracefully")
	return nil
}
