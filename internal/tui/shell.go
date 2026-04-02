package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Default configuration for shell commands
const (
	// DefaultCommandTimeout is the default timeout for shell commands
	DefaultCommandTimeout = 5 * time.Minute
	// MaxOutputLines is the maximum number of lines to show before truncation
	MaxOutputLines = 100
	// JobCleanupAge is how long to keep completed jobs before cleanup
	JobCleanupAge = 10 * time.Minute
)

// JobStatus represents the status of a background job
type JobStatus int

const (
	JobRunning JobStatus = iota
	JobCompleted
	JobFailed
	JobCancelled
)

func (s JobStatus) String() string {
	switch s {
	case JobRunning:
		return "running"
	case JobCompleted:
		return "completed"
	case JobFailed:
		return "failed"
	case JobCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// BackgroundJob represents a job running in the background
type BackgroundJob struct {
	ID         int
	Command    string
	Status     JobStatus
	StartTime  time.Time
	EndTime    time.Time
	Output     strings.Builder
	Error      error
	cancel     context.CancelFunc
	mu         sync.Mutex
	outputDone chan struct{} // signals when output collection is complete
}

// JobManager manages background jobs for a session
type JobManager struct {
	jobs     map[int]*BackgroundJob
	nextID   int32 // atomic counter
	mu       sync.RWMutex
	jobsDone chan int // channel to signal when a job completes
}

// NewJobManager creates a new job manager
func NewJobManager() *JobManager {
	return &JobManager{
		jobs:     make(map[int]*BackgroundJob),
		jobsDone: make(chan int, 10),
	}
}

// AddJob adds a new job and returns its ID
func (jm *JobManager) AddJob(cmd string, cancel context.CancelFunc) *BackgroundJob {
	id := int(atomic.AddInt32(&jm.nextID, 1))
	job := &BackgroundJob{
		ID:         id,
		Command:    cmd,
		Status:     JobRunning,
		StartTime:  time.Now(),
		cancel:     cancel,
		outputDone: make(chan struct{}),
	}
	jm.mu.Lock()
	jm.jobs[id] = job
	jm.mu.Unlock()
	return job
}

// GetJob returns a job by ID
func (jm *JobManager) GetJob(id int) *BackgroundJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.jobs[id]
}

// ListJobs returns all jobs (for display)
func (jm *JobManager) ListJobs() []*BackgroundJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	jobs := make([]*BackgroundJob, 0, len(jm.jobs))
	for _, job := range jm.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// CancelJob cancels a running job
func (jm *JobManager) CancelJob(id int) error {
	jm.mu.RLock()
	job := jm.jobs[id]
	jm.mu.RUnlock()

	if job == nil {
		return fmt.Errorf("no such job: %d", id)
	}
	if job.Status != JobRunning {
		return fmt.Errorf("job %d is not running", id)
	}
	if job.cancel != nil {
		job.cancel()
	}
	return nil
}

// MarkComplete marks a job as complete
func (jm *JobManager) MarkComplete(id int, status JobStatus, err error) {
	jm.mu.Lock()
	if job, ok := jm.jobs[id]; ok {
		job.mu.Lock()
		job.Status = status
		job.EndTime = time.Now()
		job.Error = err
		job.mu.Unlock()
	}
	jm.mu.Unlock()
	// Signal completion
	select {
	case jm.jobsDone <- id:
	default:
	}
}

// CleanupOldJobs removes completed jobs older than JobCleanupAge
func (jm *JobManager) CleanupOldJobs() {
	cutoff := time.Now().Add(-JobCleanupAge)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for id, job := range jm.jobs {
		if job.Status != JobRunning && job.EndTime.Before(cutoff) {
			delete(jm.jobs, id)
		}
	}
}

// ShellState tracks the working directory for shell escape commands
type ShellState struct {
	// CurrentDir is the current working directory for shell commands
	CurrentDir string
	// PrevDir is the previous directory (for cd -)
	PrevDir string
	// EnvVars holds custom environment variables set via export
	EnvVars map[string]string
	// Jobs manages background jobs for this shell session
	Jobs *JobManager
	// RunningCmd tracks a foreground command that can be cancelled
	RunningCmd *exec.Cmd
	// RunningCancel cancels the running foreground command
	RunningCancel context.CancelFunc
}

// inheritEnvironment returns a map of inherited environment variables
func inheritEnvironment() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.Index(e, "="); idx != -1 {
			env[e[:idx]] = e[idx+1:]
		}
	}
	return env
}

// NewShellState creates a new ShellState with the default directory
func NewShellState() ShellState {
	// Default to user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fall back to current working directory
		homeDir, _ = os.Getwd()
	}
	return ShellState{
		CurrentDir: homeDir,
		PrevDir:    homeDir,
		EnvVars:    inheritEnvironment(),
		Jobs:       NewJobManager(),
	}
}

// NewShellStateWithDir creates a ShellState with a specific starting directory
func NewShellStateWithDir(dir string) ShellState {
	if dir == "" {
		return NewShellState()
	}
	// Resolve to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return NewShellState()
	}
	return ShellState{
		CurrentDir: absDir,
		PrevDir:    absDir,
		EnvVars:    inheritEnvironment(),
		Jobs:       NewJobManager(),
	}
}

// IsBackgroundCommand checks if the command should run in the background (ends with &)
func IsBackgroundCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)
	if strings.HasSuffix(cmdLine, "&") {
		// Remove the trailing & and any extra whitespace
		cmd := strings.TrimSpace(strings.TrimSuffix(cmdLine, "&"))
		return true, cmd
	}
	return false, cmdLine
}

// IsJobsCommand checks if the command is the 'jobs' builtin
func IsJobsCommand(cmdLine string) bool {
	return strings.TrimSpace(cmdLine) == "jobs"
}

// IsKillCommand checks if the command is a 'kill %N' command
// Returns (isKill, jobID) where jobID is 0 if parsing failed
func IsKillCommand(cmdLine string) (bool, int) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Must start with "kill " followed by something
	if !strings.HasPrefix(cmdLine, "kill ") {
		return false, 0
	}

	// Get the rest after "kill "
	rest := strings.TrimSpace(cmdLine[5:])

	// Must have % job reference
	if !strings.HasPrefix(rest, "%") {
		return false, 0
	}

	// Extract the number
	idStr := strings.TrimPrefix(rest, "%")
	idStr = strings.TrimSpace(idStr)

	// Must be a positive integer
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		return true, 0 // It's a kill %X command but malformed
	}

	return true, id
}

// FormatJobsList returns a formatted list of jobs for display
func (s *ShellState) FormatJobsList() string {
	if s.Jobs == nil {
		return "No jobs"
	}
	jobs := s.Jobs.ListJobs()
	if len(jobs) == 0 {
		return "No jobs"
	}
	var sb strings.Builder
	for _, job := range jobs {
		job.mu.Lock()
		status := job.Status.String()
		duration := ""
		if job.Status == JobRunning {
			duration = fmt.Sprintf(" (running for %s)", time.Since(job.StartTime).Round(time.Second))
		} else {
			duration = fmt.Sprintf(" (took %s)", job.EndTime.Sub(job.StartTime).Round(time.Second))
		}
		job.mu.Unlock()
		sb.WriteString(fmt.Sprintf("[%d] %s%s  %s\n", job.ID, status, duration, truncateCommand(job.Command, 50)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// truncateCommand truncates a command string for display
func truncateCommand(cmd string, maxLen int) string {
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen-3] + "..."
}

// TruncateOutput truncates output if it exceeds maxLines
// Returns the truncated output and the number of remaining lines
func TruncateOutput(output string, maxLines int) (string, int) {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output, 0
	}
	truncated := strings.Join(lines[:maxLines], "\n")
	remaining := len(lines) - maxLines
	return truncated + fmt.Sprintf("\n[truncated, %d more lines]", remaining), remaining
}

// CancelRunningCommand cancels any foreground command that may be running
func (s *ShellState) CancelRunningCommand() bool {
	if s.RunningCancel != nil {
		s.RunningCancel()
		s.RunningCancel = nil
		s.RunningCmd = nil
		return true
	}
	return false
}

// HandleCdCommand processes a cd command and updates the directory state.
// Returns (newState, errorMessage). If errorMessage is non-empty, the cd failed.
func (s ShellState) HandleCdCommand(args string) (ShellState, string) {
	target := strings.TrimSpace(args)

	var newDir string

	switch {
	case target == "" || target == "~":
		// cd with no args or cd ~ goes to home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return s, fmt.Sprintf("cd: cannot find home directory: %v", err)
		}
		newDir = homeDir

	case target == "-":
		// cd - goes to previous directory
		if s.PrevDir == "" {
			return s, "cd: OLDPWD not set"
		}
		newDir = s.PrevDir

	case strings.HasPrefix(target, "~/"):
		// Expand ~ at the start of the path
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return s, fmt.Sprintf("cd: cannot find home directory: %v", err)
		}
		newDir = filepath.Join(homeDir, target[2:])

	case filepath.IsAbs(target):
		// Absolute path
		newDir = target

	default:
		// Relative path - resolve against current directory
		newDir = filepath.Join(s.CurrentDir, target)
	}

	// Clean the path to resolve . and ..
	newDir = filepath.Clean(newDir)

	// Verify the directory exists and is accessible
	info, err := os.Stat(newDir)
	if err != nil {
		if os.IsNotExist(err) {
			return s, fmt.Sprintf("cd: no such file or directory: %s", target)
		}
		return s, fmt.Sprintf("cd: %s: %v", target, err)
	}

	if !info.IsDir() {
		return s, fmt.Sprintf("cd: not a directory: %s", target)
	}

	// Success - update state (preserve EnvVars and Jobs)
	newState := ShellState{
		CurrentDir:    newDir,
		PrevDir:       s.CurrentDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
	}

	return newState, ""
}

// IsCdCommand checks if the command line is a cd command.
// Returns (isCd, args) where args is the target directory if it's a cd command.
func IsCdCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Check for bare "cd"
	if cmdLine == "cd" {
		return true, ""
	}

	// Check for "cd " followed by arguments
	if strings.HasPrefix(cmdLine, "cd ") {
		return true, strings.TrimSpace(cmdLine[3:])
	}

	return false, ""
}

// FormatPrompt returns a shell prompt string showing the current directory.
// If abbreviateHome is true, replaces home directory with ~.
func (s ShellState) FormatPrompt(abbreviateHome bool) string {
	dir := s.CurrentDir

	if abbreviateHome {
		if homeDir, err := os.UserHomeDir(); err == nil {
			if dir == homeDir {
				dir = "~"
			} else if strings.HasPrefix(dir, homeDir+string(os.PathSeparator)) {
				dir = "~" + dir[len(homeDir):]
			}
		}
	}

	return dir + " $ "
}

// ShellResultWithDirMsg delivers the result of a shell escape command with directory info
type ShellResultWithDirMsg struct {
	SessionKey string
	Output     string
	Err        error
	NewDir     string // Updated directory (for pwd command output, etc.)
}

// ShellStreamMsg delivers streaming output from a running command
type ShellStreamMsg struct {
	SessionKey string
	Line       string
	IsStderr   bool
}

// ShellCommandCancelledMsg signals that a command was cancelled
type ShellCommandCancelledMsg struct {
	SessionKey string
}

// BackgroundJobStartedMsg signals that a background job was started
type BackgroundJobStartedMsg struct {
	SessionKey string
	JobID      int
	Command    string
}

// BackgroundJobCompletedMsg signals that a background job completed
type BackgroundJobCompletedMsg struct {
	SessionKey string
	JobID      int
	Status     JobStatus
	Output     string
	Error      error
}

// executeShellCmdWithDir returns a tea.Cmd that executes a shell command in the specified directory
// with streaming output support and configurable timeout.
func executeShellCmdWithDir(sessionKey, cmdLine, workDir string) tea.Cmd {
	return executeShellCmdWithTimeout(sessionKey, cmdLine, workDir, DefaultCommandTimeout)
}

// executeShellCmdWithTimeout returns a tea.Cmd that executes a shell command with a specific timeout
func executeShellCmdWithTimeout(sessionKey, cmdLine, workDir string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
		cmd.Dir = workDir

		// Create pipes for stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}

		if err := cmd.Start(); err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}

		// Collect output from both streams
		var output strings.Builder
		var wg sync.WaitGroup
		wg.Add(2)

		// Reader for stdout
		go func() {
			defer wg.Done()
			reader := bufio.NewReader(stdout)
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					output.WriteString(line)
				}
				if err != nil {
					if err != io.EOF {
						output.WriteString(fmt.Sprintf("\n[stdout read error: %v]", err))
					}
					break
				}
			}
		}()

		// Reader for stderr
		go func() {
			defer wg.Done()
			reader := bufio.NewReader(stderr)
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					output.WriteString(line)
				}
				if err != nil {
					if err != io.EOF {
						output.WriteString(fmt.Sprintf("\n[stderr read error: %v]", err))
					}
					break
				}
			}
		}()

		// Wait for readers to finish
		wg.Wait()

		// Wait for command to complete
		err = cmd.Wait()

		// Check if we timed out
		if ctx.Err() == context.DeadlineExceeded {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     output.String() + fmt.Sprintf("\n[command timed out after %s]", timeout),
				Err:        ctx.Err(),
			}
		}

		// Truncate output if needed
		finalOutput := output.String()
		truncated, remaining := TruncateOutput(finalOutput, MaxOutputLines)
		if remaining > 0 {
			finalOutput = truncated
		}

		return ShellResultMsg{
			SessionKey: sessionKey,
			Output:     finalOutput,
			Err:        err,
		}
	}
}

// executeBackgroundCmd runs a command in the background and tracks it in the job manager
func executeBackgroundCmd(sessionKey, cmdLine, workDir string, jobs *JobManager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		job := jobs.AddJob(cmdLine, cancel)

		// Start a goroutine to run the command
		go func() {
			cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
			cmd.Dir = workDir

			// Create pipes for output
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				job.mu.Lock()
				job.Output.WriteString(fmt.Sprintf("Error creating stdout pipe: %v\n", err))
				job.mu.Unlock()
				jobs.MarkComplete(job.ID, JobFailed, err)
				return
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				job.mu.Lock()
				job.Output.WriteString(fmt.Sprintf("Error creating stderr pipe: %v\n", err))
				job.mu.Unlock()
				jobs.MarkComplete(job.ID, JobFailed, err)
				return
			}

			if err := cmd.Start(); err != nil {
				job.mu.Lock()
				job.Output.WriteString(fmt.Sprintf("Error starting command: %v\n", err))
				job.mu.Unlock()
				jobs.MarkComplete(job.ID, JobFailed, err)
				return
			}

			// Collect output
			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				reader := bufio.NewReader(stdout)
				for {
					line, err := reader.ReadString('\n')
					if line != "" {
						job.mu.Lock()
						job.Output.WriteString(line)
						job.mu.Unlock()
					}
					if err != nil {
						break
					}
				}
			}()

			go func() {
				defer wg.Done()
				reader := bufio.NewReader(stderr)
				for {
					line, err := reader.ReadString('\n')
					if line != "" {
						job.mu.Lock()
						job.Output.WriteString(line)
						job.mu.Unlock()
					}
					if err != nil {
						break
					}
				}
			}()

			wg.Wait()
			close(job.outputDone)

			err = cmd.Wait()
			if ctx.Err() == context.Canceled {
				jobs.MarkComplete(job.ID, JobCancelled, nil)
			} else if err != nil {
				jobs.MarkComplete(job.ID, JobFailed, err)
			} else {
				jobs.MarkComplete(job.ID, JobCompleted, nil)
			}
		}()

		return BackgroundJobStartedMsg{
			SessionKey: sessionKey,
			JobID:      job.ID,
			Command:    cmdLine,
		}
	}
}

// watchBackgroundJobs returns a tea.Cmd that waits for background job completion notifications
func watchBackgroundJobs(sessionKey string, jobs *JobManager) tea.Cmd {
	return func() tea.Msg {
		select {
		case jobID := <-jobs.jobsDone:
			job := jobs.GetJob(jobID)
			if job == nil {
				return nil
			}
			job.mu.Lock()
			output := job.Output.String()
			status := job.Status
			err := job.Error
			job.mu.Unlock()

			// Truncate output if needed
			truncated, _ := TruncateOutput(output, MaxOutputLines)

			return BackgroundJobCompletedMsg{
				SessionKey: sessionKey,
				JobID:      jobID,
				Status:     status,
				Output:     truncated,
				Error:      err,
			}
		}
	}
}

// ExpandVariables expands $VAR and ${VAR} references in a string
func (s ShellState) ExpandVariables(input string) string {
	if s.EnvVars == nil {
		return input
	}

	result := input

	// First handle ${VAR} style (must do this first to avoid partial matches)
	for name, value := range s.EnvVars {
		result = strings.ReplaceAll(result, "${"+name+"}", value)
	}

	// Handle $VAR style by scanning left to right
	// Build the result character by character to avoid replacement order issues
	var sb strings.Builder
	i := 0
	for i < len(result) {
		if result[i] == '$' && i+1 < len(result) {
			// Try to match a variable name
			j := i + 1
			// Variable names start with letter or underscore
			if (result[j] >= 'a' && result[j] <= 'z') ||
				(result[j] >= 'A' && result[j] <= 'Z') ||
				result[j] == '_' {
				// Consume alphanumeric and underscore
				for j < len(result) &&
					((result[j] >= 'a' && result[j] <= 'z') ||
						(result[j] >= 'A' && result[j] <= 'Z') ||
						(result[j] >= '0' && result[j] <= '9') ||
						result[j] == '_') {
					j++
				}
				varName := result[i+1 : j]
				if value, ok := s.EnvVars[varName]; ok {
					sb.WriteString(value)
					i = j
					continue
				}
			}
		}
		sb.WriteByte(result[i])
		i++
	}

	return sb.String()
}

// IsExportCommand checks if the command is an export command
// Returns (isExport, varName, value, showOnly)
// If showOnly is true, this is "export" or "export VAR" to show values
func IsExportCommand(cmdLine string) (bool, string, string, bool) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Check for bare "export" to show all variables
	if cmdLine == "export" {
		return true, "", "", true
	}

	// Must start with "export "
	if !strings.HasPrefix(cmdLine, "export ") {
		return false, "", "", false
	}

	rest := strings.TrimSpace(cmdLine[7:])
	if rest == "" {
		return true, "", "", true
	}

	// Check for VAR=value format
	if idx := strings.Index(rest, "="); idx != -1 {
		varName := strings.TrimSpace(rest[:idx])
		value := rest[idx+1:]
		// Remove surrounding quotes if present
		value = strings.Trim(value, `"'`)
		return true, varName, value, false
	}

	// Just "export VAR" to show a specific variable
	return true, rest, "", true
}

// HandleExportCommand processes an export command
// Returns (newState, output) where output is for showing variables
func (s ShellState) HandleExportCommand(varName, value string, showOnly bool) (ShellState, string) {
	newState := ShellState{
		CurrentDir:    s.CurrentDir,
		PrevDir:       s.PrevDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
	}

	if newState.EnvVars == nil {
		newState.EnvVars = make(map[string]string)
	}

	if showOnly {
		if varName == "" {
			// Show all variables
			if len(newState.EnvVars) == 0 {
				return newState, "(no exported variables)"
			}
			var lines []string
			for k, v := range newState.EnvVars {
				lines = append(lines, fmt.Sprintf("%s=%s", k, v))
			}
			return newState, strings.Join(lines, "\n")
		}
		// Show specific variable
		if val, ok := newState.EnvVars[varName]; ok {
			return newState, fmt.Sprintf("%s=%s", varName, val)
		}
		return newState, fmt.Sprintf("%s: not set", varName)
	}

	// Set the variable
	newState.EnvVars[varName] = value
	return newState, ""
}

// IsUnsetCommand checks if the command is an unset command
// Returns (isUnset, varName)
func IsUnsetCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)

	if !strings.HasPrefix(cmdLine, "unset ") {
		return false, ""
	}

	varName := strings.TrimSpace(cmdLine[6:])
	if varName == "" {
		return false, ""
	}

	return true, varName
}

// HandleUnsetCommand processes an unset command
// Returns (newState, output)
func (s ShellState) HandleUnsetCommand(varName string) (ShellState, string) {
	newState := ShellState{
		CurrentDir:    s.CurrentDir,
		PrevDir:       s.PrevDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
	}

	if newState.EnvVars == nil {
		return newState, ""
	}

	if _, ok := newState.EnvVars[varName]; !ok {
		return newState, fmt.Sprintf("%s: not set", varName)
	}

	// Make a copy to avoid mutating shared map
	newEnv := make(map[string]string)
	for k, v := range newState.EnvVars {
		if k != varName {
			newEnv[k] = v
		}
	}
	newState.EnvVars = newEnv
	return newState, ""
}

// executeShellCmdWithEnv returns a tea.Cmd that executes a shell command with custom environment
func executeShellCmdWithEnv(sessionKey, cmdLine, workDir string, envVars map[string]string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
		cmd.Dir = workDir

		// Set environment variables
		cmd.Env = os.Environ()
		for k, v := range envVars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}

		// Create pipes for stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}

		if err := cmd.Start(); err != nil {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     "",
				Err:        err,
			}
		}

		// Collect output from both streams
		var output strings.Builder
		var wg sync.WaitGroup
		wg.Add(2)

		// Reader for stdout
		go func() {
			defer wg.Done()
			reader := bufio.NewReader(stdout)
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					output.WriteString(line)
				}
				if err != nil {
					if err != io.EOF {
						output.WriteString(fmt.Sprintf("\n[stdout read error: %v]", err))
					}
					break
				}
			}
		}()

		// Reader for stderr
		go func() {
			defer wg.Done()
			reader := bufio.NewReader(stderr)
			for {
				line, err := reader.ReadString('\n')
				if line != "" {
					output.WriteString(line)
				}
				if err != nil {
					if err != io.EOF {
						output.WriteString(fmt.Sprintf("\n[stderr read error: %v]", err))
					}
					break
				}
			}
		}()

		// Wait for readers to finish
		wg.Wait()

		// Wait for command to complete
		err = cmd.Wait()

		// Check if we timed out
		if ctx.Err() == context.DeadlineExceeded {
			return ShellResultMsg{
				SessionKey: sessionKey,
				Output:     output.String() + fmt.Sprintf("\n[command timed out after %s]", DefaultCommandTimeout),
				Err:        ctx.Err(),
			}
		}

		// Truncate output if needed
		finalOutput := output.String()
		truncated, remaining := TruncateOutput(finalOutput, MaxOutputLines)
		if remaining > 0 {
			finalOutput = truncated
		}

		return ShellResultMsg{
			SessionKey: sessionKey,
			Output:     finalOutput,
			Err:        err,
		}
	}
}
