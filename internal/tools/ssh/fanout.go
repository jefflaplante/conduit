//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// FanoutExecutor handles parallel command execution across multiple hosts
type FanoutExecutor struct {
	pool        *Pool
	maxParallel int
}

// FanoutResult contains aggregated results from fan-out execution
type FanoutResult struct {
	// Results maps host names to their execution results
	Results map[string]*ExecutionResult

	// Succeeded lists hosts that completed successfully (exit code 0)
	Succeeded []string

	// Failed lists hosts that failed (non-zero exit code or error)
	Failed []string

	// Duration is the total time from start to completion
	Duration time.Duration

	// Summary is a human-readable summary of the execution
	Summary string
}

// NewFanoutExecutor creates a new fan-out executor
func NewFanoutExecutor(pool *Pool, maxParallel int) *FanoutExecutor {
	if maxParallel <= 0 {
		maxParallel = 10 // Default to 10 concurrent executions
	}
	return &FanoutExecutor{
		pool:        pool,
		maxParallel: maxParallel,
	}
}

// Execute runs a command across multiple hosts in parallel with bounded concurrency
func (f *FanoutExecutor) Execute(ctx context.Context, hosts []string, command string, timeout time.Duration) *FanoutResult {
	startTime := time.Now()

	result := &FanoutResult{
		Results:   make(map[string]*ExecutionResult),
		Succeeded: []string{},
		Failed:    []string{},
	}

	if len(hosts) == 0 {
		result.Summary = "No hosts specified"
		result.Duration = time.Since(startTime)
		return result
	}

	// Create semaphore for bounded parallelism
	sem := make(chan struct{}, f.maxParallel)

	// WaitGroup to wait for all executions to complete
	var wg sync.WaitGroup

	// Mutex to protect result maps
	var mu sync.Mutex

	// Execute on each host
	for _, host := range hosts {
		wg.Add(1)

		go func(hostName string) {
			defer wg.Done()

			// Acquire semaphore slot
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }() // Release slot when done
			case <-ctx.Done():
				// Context cancelled while waiting for slot
				mu.Lock()
				result.Results[hostName] = &ExecutionResult{
					Host:     hostName,
					Command:  command,
					Error:    "context cancelled before execution",
					ExitCode: -1,
				}
				result.Failed = append(result.Failed, hostName)
				mu.Unlock()
				return
			}

			// Check context before execution
			if ctx.Err() != nil {
				mu.Lock()
				result.Results[hostName] = &ExecutionResult{
					Host:     hostName,
					Command:  command,
					Error:    ctx.Err().Error(),
					ExitCode: -1,
				}
				result.Failed = append(result.Failed, hostName)
				mu.Unlock()
				return
			}

			// Execute command with timeout
			execResult := f.executeOnHost(ctx, hostName, command, timeout)

			// Store result
			mu.Lock()
			result.Results[hostName] = execResult
			if execResult.ExitCode == 0 && execResult.Error == "" {
				result.Succeeded = append(result.Succeeded, hostName)
			} else {
				result.Failed = append(result.Failed, hostName)
			}
			mu.Unlock()
		}(host)
	}

	// Wait for all executions to complete
	wg.Wait()

	// Calculate total duration
	result.Duration = time.Since(startTime)

	// Sort host lists for consistent output
	sort.Strings(result.Succeeded)
	sort.Strings(result.Failed)

	// Generate summary
	result.Summary = f.generateSummary(result, len(hosts))

	return result
}

// executeOnHost executes a command on a single host
func (f *FanoutExecutor) executeOnHost(ctx context.Context, hostName string, command string, timeout time.Duration) *ExecutionResult {
	execStart := time.Now()

	// Get connection from pool
	client, err := f.pool.Get(hostName)
	if err != nil {
		return &ExecutionResult{
			Host:     hostName,
			Command:  command,
			Error:    fmt.Sprintf("failed to get connection: %v", err),
			ExitCode: -1,
			Duration: time.Since(execStart),
		}
	}
	defer f.pool.Put(hostName, client)

	// Create timeout context if timeout is specified
	var execCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	} else {
		execCtx = ctx
	}

	// Execute command
	execResult, err := client.ExecWithTimeout(command, timeout)
	duration := time.Since(execStart)

	if err != nil {
		// Check if it was a timeout
		timedOut := false
		if execCtx.Err() == context.DeadlineExceeded {
			timedOut = true
		}

		return &ExecutionResult{
			Host:     hostName,
			Command:  command,
			Error:    err.Error(),
			ExitCode: -1,
			Duration: duration,
			TimedOut: timedOut,
		}
	}

	// Map ExecResult to ExecutionResult
	return &ExecutionResult{
		Host:     hostName,
		Command:  command,
		ExitCode: execResult.ExitCode,
		Stdout:   execResult.Stdout,
		Stderr:   execResult.Stderr,
		Duration: duration,
		Error:    "",
		TimedOut: false,
	}
}

// generateSummary creates a human-readable summary of the fan-out execution
func (f *FanoutExecutor) generateSummary(result *FanoutResult, totalHosts int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Executed on %d host(s) in %v\n", totalHosts, result.Duration.Round(time.Millisecond)))
	sb.WriteString(fmt.Sprintf("Succeeded: %d, Failed: %d\n", len(result.Succeeded), len(result.Failed)))

	if len(result.Succeeded) > 0 {
		sb.WriteString(fmt.Sprintf("\nSuccessful hosts: %s\n", strings.Join(result.Succeeded, ", ")))
	}

	if len(result.Failed) > 0 {
		sb.WriteString(fmt.Sprintf("\nFailed hosts: %s\n", strings.Join(result.Failed, ", ")))

		// Include error details for failed hosts
		sb.WriteString("\nFailure details:\n")
		for _, host := range result.Failed {
			if execResult, ok := result.Results[host]; ok {
				if execResult.TimedOut {
					sb.WriteString(fmt.Sprintf("  %s: timeout after %v\n", host, execResult.Duration.Round(time.Millisecond)))
				} else if execResult.Error != "" {
					sb.WriteString(fmt.Sprintf("  %s: %s\n", host, execResult.Error))
				} else {
					sb.WriteString(fmt.Sprintf("  %s: exit code %d\n", host, execResult.ExitCode))
				}
			}
		}
	}

	return sb.String()
}

// FormatResults formats the fan-out results for display
func (f *FanoutExecutor) FormatResults(result *FanoutResult, includeOutput bool) string {
	var sb strings.Builder

	sb.WriteString(result.Summary)

	if includeOutput && len(result.Results) > 0 {
		sb.WriteString("\n--- Detailed Results ---\n")

		// Sort hosts for consistent output
		hosts := make([]string, 0, len(result.Results))
		for host := range result.Results {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)

		for _, host := range hosts {
			execResult := result.Results[host]
			sb.WriteString(fmt.Sprintf("\n[%s] (exit: %d, duration: %v)\n",
				host, execResult.ExitCode, execResult.Duration.Round(time.Millisecond)))

			if execResult.Error != "" {
				sb.WriteString(fmt.Sprintf("Error: %s\n", execResult.Error))
			}

			if execResult.Stdout != "" {
				sb.WriteString("Stdout:\n")
				sb.WriteString(execResult.Stdout)
				if !strings.HasSuffix(execResult.Stdout, "\n") {
					sb.WriteString("\n")
				}
			}

			if execResult.Stderr != "" {
				sb.WriteString("Stderr:\n")
				sb.WriteString(execResult.Stderr)
				if !strings.HasSuffix(execResult.Stderr, "\n") {
					sb.WriteString("\n")
				}
			}
		}
	}

	return sb.String()
}
