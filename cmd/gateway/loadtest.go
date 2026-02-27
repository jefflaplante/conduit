package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"conduit/internal/ai"
	"conduit/internal/testing/loadtest"

	"github.com/spf13/cobra"
)

var (
	loadtestConcurrency int
	loadtestDuration    string
	loadtestRPS         int
	loadtestMix         string
	loadtestRampUp      string
)

var loadtestCmd = &cobra.Command{
	Use:   "loadtest",
	Short: "Run a load test against the AI provider using a mock backend",
	Long: `Run a load test that simulates concurrent users sending requests to the
AI provider layer. Uses a mock provider so no real API calls are made.

The test measures latency percentiles (P50, P95, P99), throughput (req/s),
error rates, and provides optimization recommendations.

Examples:
  # Quick 10-second test with 5 concurrent users
  conduit loadtest --concurrency 5 --duration 10s

  # High-load test with rate limiting and custom mix
  conduit loadtest --concurrency 50 --duration 60s --rps 200 --mix simple:50,tool_use:30,multi_turn:20

  # Test with ramp-up period
  conduit loadtest --concurrency 20 --duration 30s --ramp-up 10s`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLoadTest()
	},
}

func init() {
	loadtestCmd.Flags().IntVar(&loadtestConcurrency, "concurrency", 10, "Number of parallel virtual users")
	loadtestCmd.Flags().StringVar(&loadtestDuration, "duration", "30s", "Test duration (e.g., 10s, 1m, 5m)")
	loadtestCmd.Flags().IntVar(&loadtestRPS, "rps", 0, "Target requests per second (0 = unlimited)")
	loadtestCmd.Flags().StringVar(&loadtestMix, "mix", "", "Request distribution (e.g., simple:50,tool_use:30,multi_turn:20)")
	loadtestCmd.Flags().StringVar(&loadtestRampUp, "ramp-up", "0s", "Ramp-up period to gradually add virtual users")

	rootCmd.AddCommand(loadtestCmd)
}

func runLoadTest() error {
	// Parse duration
	duration, err := time.ParseDuration(loadtestDuration)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", loadtestDuration, err)
	}

	// Parse ramp-up
	rampUp, err := time.ParseDuration(loadtestRampUp)
	if err != nil {
		return fmt.Errorf("invalid ramp-up %q: %w", loadtestRampUp, err)
	}

	// Parse request mix
	requestMix, err := parseMix(loadtestMix)
	if err != nil {
		return fmt.Errorf("invalid mix %q: %w", loadtestMix, err)
	}

	config := loadtest.LoadTestConfig{
		Concurrency:       loadtestConcurrency,
		Duration:          duration,
		RequestsPerSecond: loadtestRPS,
		RampUpPeriod:      rampUp,
		RequestMix:        requestMix,
	}

	// Create mock provider with realistic simulated latency
	mockProvider := ai.NewMockProvider("loadtest-mock")
	mockProvider.SetResponses([]ai.MockResponse{
		{
			Content: "Mock load test response.",
			Usage: ai.Usage{
				PromptTokens:     50,
				CompletionTokens: 25,
				TotalTokens:      75,
			},
		},
	})

	// Create and configure the load generator
	lg := loadtest.NewLoadGenerator(mockProvider)
	lg.RegisterGenerator(&loadtest.SimpleChatGenerator{})
	lg.RegisterGenerator(&loadtest.ToolUseGenerator{})
	lg.RegisterGenerator(&loadtest.MultiTurnGenerator{})

	if err := lg.Configure(config); err != nil {
		return err
	}

	// Handle interrupt for graceful stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\nStopping load test...\n")
			lg.Stop()
			cancel()
		case <-ctx.Done():
		}
	}()

	// Print configuration
	fmt.Printf("=== Load Test Configuration ===\n")
	fmt.Printf("Concurrency:  %d virtual users\n", config.Concurrency)
	fmt.Printf("Duration:     %v\n", config.Duration)
	if config.RequestsPerSecond > 0 {
		fmt.Printf("Target RPS:   %d\n", config.RequestsPerSecond)
	} else {
		fmt.Printf("Target RPS:   unlimited\n")
	}
	if config.RampUpPeriod > 0 {
		fmt.Printf("Ramp-up:      %v\n", config.RampUpPeriod)
	}
	if len(config.RequestMix) > 0 {
		fmt.Printf("Request mix:  %v\n", formatMix(config.RequestMix))
	} else {
		fmt.Printf("Request mix:  equal distribution\n")
	}
	fmt.Printf("Provider:     mock (no real API calls)\n")
	fmt.Printf("\nRunning...\n\n")

	// Execute load test
	result := lg.Run(ctx)

	// Print results
	fmt.Print(result.FormatSummary())

	// Analyze and print optimizations
	optimizations := loadtest.AnalyzeResults(result)
	if len(optimizations) > 0 {
		fmt.Printf("\n=== Optimization Recommendations ===\n\n")
		for i, opt := range optimizations {
			fmt.Printf("%d. [%s] %s\n", i+1, opt.Impact, opt.Description)
			fmt.Printf("   Action: %s\n\n", opt.Action)
		}
	} else {
		fmt.Printf("\nNo optimization recommendations - results look healthy.\n")
	}

	return nil
}

// parseMix parses a mix string like "simple:50,tool_use:30,multi_turn:20"
func parseMix(mix string) (map[string]int, error) {
	if mix == "" {
		return nil, nil
	}

	result := make(map[string]int)
	parts := strings.Split(mix, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("invalid mix entry %q, expected name:percentage", part)
		}
		name := strings.TrimSpace(kv[0])
		pct, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid percentage in %q: %w", part, err)
		}
		result[name] = pct
	}

	return result, nil
}

// formatMix renders a mix map for display.
func formatMix(mix map[string]int) string {
	parts := make([]string, 0, len(mix))
	for name, pct := range mix {
		parts = append(parts, fmt.Sprintf("%s=%d%%", name, pct))
	}
	return strings.Join(parts, ", ")
}
