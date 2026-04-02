package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewShellState(t *testing.T) {
	state := NewShellState()

	// Should default to home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	if state.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", homeDir, state.CurrentDir)
	}

	if state.PrevDir != homeDir {
		t.Errorf("Expected PrevDir to be %q, got %q", homeDir, state.PrevDir)
	}

	// Should have Jobs manager
	if state.Jobs == nil {
		t.Error("Expected Jobs manager to be initialized")
	}
}

func TestNewShellStateWithDir(t *testing.T) {
	tmpDir := t.TempDir()

	state := NewShellStateWithDir(tmpDir)

	if state.CurrentDir != tmpDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", tmpDir, state.CurrentDir)
	}

	if state.PrevDir != tmpDir {
		t.Errorf("Expected PrevDir to be %q, got %q", tmpDir, state.PrevDir)
	}
}

func TestNewShellStateWithDir_Empty(t *testing.T) {
	// Empty string should fall back to default (home directory)
	state := NewShellStateWithDir("")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	if state.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be %q (home), got %q", homeDir, state.CurrentDir)
	}
}

func TestIsCdCommand(t *testing.T) {
	tests := []struct {
		input    string
		isCd     bool
		expected string
	}{
		{"cd", true, ""},
		{"cd ", true, ""},
		{"cd /tmp", true, "/tmp"},
		{"cd /path/to/dir", true, "/path/to/dir"},
		{"cd ~", true, "~"},
		{"cd -", true, "-"},
		{"cd ..", true, ".."},
		{"cd  /tmp", true, "/tmp"}, // extra space
		{"ls", false, ""},
		{"echo cd", false, ""},
		{"cdrom", false, ""},
		{"ls cd", false, ""},
		{"", false, ""},
	}

	for _, tt := range tests {
		isCd, args := IsCdCommand(tt.input)
		if isCd != tt.isCd {
			t.Errorf("IsCdCommand(%q): expected isCd=%v, got %v", tt.input, tt.isCd, isCd)
		}
		if isCd && args != tt.expected {
			t.Errorf("IsCdCommand(%q): expected args=%q, got %q", tt.input, tt.expected, args)
		}
	}
}

func TestHandleCdCommand_NoArgs(t *testing.T) {
	tmpDir := t.TempDir()
	state := NewShellStateWithDir(tmpDir)

	newState, errMsg := state.HandleCdCommand("")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	if newState.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be home (%q), got %q", homeDir, newState.CurrentDir)
	}

	if newState.PrevDir != tmpDir {
		t.Errorf("Expected PrevDir to be %q, got %q", tmpDir, newState.PrevDir)
	}
}

func TestHandleCdCommand_Tilde(t *testing.T) {
	tmpDir := t.TempDir()
	state := NewShellStateWithDir(tmpDir)

	newState, errMsg := state.HandleCdCommand("~")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	if newState.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be home (%q), got %q", homeDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_TildeExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	state := NewShellStateWithDir(tmpDir)

	// Create a test subdirectory in home
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	// Test ~/. which should resolve to home
	newState, errMsg := state.HandleCdCommand("~/.")
	if errMsg != "" {
		t.Errorf("Expected no error for ~/., got: %s", errMsg)
	}

	if newState.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be home (%q), got %q", homeDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_Dash(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	// Start in tmpDir with PrevDir set to home
	state := ShellState{
		CurrentDir: tmpDir,
		PrevDir:    homeDir,
	}

	newState, errMsg := state.HandleCdCommand("-")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	if newState.CurrentDir != homeDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", homeDir, newState.CurrentDir)
	}

	if newState.PrevDir != tmpDir {
		t.Errorf("Expected PrevDir to be %q, got %q", tmpDir, newState.PrevDir)
	}
}

func TestHandleCdCommand_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	state := NewShellState()

	newState, errMsg := state.HandleCdCommand(tmpDir)
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	if newState.CurrentDir != tmpDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", tmpDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	state := NewShellStateWithDir(tmpDir)

	newState, errMsg := state.HandleCdCommand("subdir")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	if newState.CurrentDir != subDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", subDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_DotDot(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	state := NewShellStateWithDir(subDir)

	newState, errMsg := state.HandleCdCommand("..")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	if newState.CurrentDir != tmpDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", tmpDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_NonexistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	state := NewShellStateWithDir(tmpDir)

	newState, errMsg := state.HandleCdCommand("nonexistent")
	if errMsg == "" {
		t.Error("Expected error for nonexistent directory, got none")
	}

	// State should remain unchanged
	if newState.CurrentDir != tmpDir {
		t.Errorf("Expected CurrentDir to remain %q, got %q", tmpDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_NotADirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a regular file
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	state := NewShellStateWithDir(tmpDir)

	newState, errMsg := state.HandleCdCommand("file.txt")
	if errMsg == "" {
		t.Error("Expected error for file (not directory), got none")
	}

	// State should remain unchanged
	if newState.CurrentDir != tmpDir {
		t.Errorf("Expected CurrentDir to remain %q, got %q", tmpDir, newState.CurrentDir)
	}
}

func TestHandleCdCommand_ComplexRelativePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a/b/c structure
	aDir := filepath.Join(tmpDir, "a")
	bDir := filepath.Join(aDir, "b")
	cDir := filepath.Join(bDir, "c")
	if err := os.MkdirAll(cDir, 0755); err != nil {
		t.Fatalf("Failed to create directories: %v", err)
	}

	state := NewShellStateWithDir(bDir)

	// cd ../a/b/c should work
	newState, errMsg := state.HandleCdCommand("../b/c")
	if errMsg != "" {
		t.Errorf("Expected no error, got: %s", errMsg)
	}

	if newState.CurrentDir != cDir {
		t.Errorf("Expected CurrentDir to be %q, got %q", cDir, newState.CurrentDir)
	}
}

func TestFormatPrompt(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("Cannot get home directory: %v", err)
	}

	tests := []struct {
		name           string
		currentDir     string
		abbreviateHome bool
		expected       string
	}{
		{
			name:           "home directory abbreviated",
			currentDir:     homeDir,
			abbreviateHome: true,
			expected:       "~ $ ",
		},
		{
			name:           "home directory not abbreviated",
			currentDir:     homeDir,
			abbreviateHome: false,
			expected:       homeDir + " $ ",
		},
		{
			name:           "subdirectory of home abbreviated",
			currentDir:     filepath.Join(homeDir, "subdir"),
			abbreviateHome: true,
			expected:       "~/subdir $ ",
		},
		{
			name:           "non-home directory",
			currentDir:     "/tmp",
			abbreviateHome: true,
			expected:       "/tmp $ ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := ShellState{CurrentDir: tt.currentDir}
			result := state.FormatPrompt(tt.abbreviateHome)
			if result != tt.expected {
				t.Errorf("FormatPrompt(%v) = %q, expected %q", tt.abbreviateHome, result, tt.expected)
			}
		})
	}
}

func TestHandleCdCommand_PreservesPrevDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two subdirectories
	dir1 := filepath.Join(tmpDir, "dir1")
	dir2 := filepath.Join(tmpDir, "dir2")
	if err := os.Mkdir(dir1, 0755); err != nil {
		t.Fatalf("Failed to create dir1: %v", err)
	}
	if err := os.Mkdir(dir2, 0755); err != nil {
		t.Fatalf("Failed to create dir2: %v", err)
	}

	// Start in dir1
	state := NewShellStateWithDir(dir1)

	// cd to dir2
	state, _ = state.HandleCdCommand(dir2)
	if state.CurrentDir != dir2 || state.PrevDir != dir1 {
		t.Errorf("After first cd: CurrentDir=%q (want %q), PrevDir=%q (want %q)",
			state.CurrentDir, dir2, state.PrevDir, dir1)
	}

	// cd - should swap back
	state, _ = state.HandleCdCommand("-")
	if state.CurrentDir != dir1 || state.PrevDir != dir2 {
		t.Errorf("After cd -: CurrentDir=%q (want %q), PrevDir=%q (want %q)",
			state.CurrentDir, dir1, state.PrevDir, dir2)
	}

	// cd - again should swap back to dir2
	state, _ = state.HandleCdCommand("-")
	if state.CurrentDir != dir2 || state.PrevDir != dir1 {
		t.Errorf("After second cd -: CurrentDir=%q (want %q), PrevDir=%q (want %q)",
			state.CurrentDir, dir2, state.PrevDir, dir1)
	}
}

func TestHandleCdCommand_EmptyPrevDir(t *testing.T) {
	// Edge case: if PrevDir is empty, cd - should fail
	state := ShellState{
		CurrentDir: "/tmp",
		PrevDir:    "",
	}

	_, errMsg := state.HandleCdCommand("-")
	if errMsg == "" {
		t.Error("Expected error for cd - with empty PrevDir, got none")
	}
}

// Environment variable tests

func TestNewShellState_InheritsEnvironment(t *testing.T) {
	state := NewShellState()

	if state.EnvVars == nil {
		t.Fatal("Expected EnvVars to be initialized")
	}

	// Should have inherited at least HOME or USER from the environment
	hasInherited := false
	if _, ok := state.EnvVars["HOME"]; ok {
		hasInherited = true
	}
	if _, ok := state.EnvVars["USER"]; ok {
		hasInherited = true
	}
	if _, ok := state.EnvVars["PATH"]; ok {
		hasInherited = true
	}

	if !hasInherited {
		t.Error("Expected EnvVars to inherit at least HOME, USER, or PATH from environment")
	}
}

func TestIsExportCommand(t *testing.T) {
	tests := []struct {
		input    string
		isExport bool
		varName  string
		value    string
		showOnly bool
	}{
		// List all vars
		{"export", true, "", "", true},
		{"  export  ", true, "", "", true},

		// Show specific var
		{"export HOME", true, "HOME", "", true},
		{"export  MY_VAR", true, "MY_VAR", "", true},

		// Set var
		{"export FOO=bar", true, "FOO", "bar", false},
		{"export FOO=bar baz", true, "FOO", "bar baz", false},
		{"export FOO=", true, "FOO", "", false},
		{"export FOO=\"bar baz\"", true, "FOO", "bar baz", false},
		{"export FOO='bar baz'", true, "FOO", "bar baz", false},
		{"export PATH=/usr/bin:/bin", true, "PATH", "/usr/bin:/bin", false},

		// Not export commands
		{"exports", false, "", "", false},
		{"echo export", false, "", "", false},
		{"ls", false, "", "", false},
		{"", false, "", "", false},
	}

	for _, tt := range tests {
		isExport, varName, value, showOnly := IsExportCommand(tt.input)
		if isExport != tt.isExport {
			t.Errorf("IsExportCommand(%q): isExport=%v, want %v", tt.input, isExport, tt.isExport)
		}
		if isExport {
			if varName != tt.varName {
				t.Errorf("IsExportCommand(%q): varName=%q, want %q", tt.input, varName, tt.varName)
			}
			if value != tt.value {
				t.Errorf("IsExportCommand(%q): value=%q, want %q", tt.input, value, tt.value)
			}
			if showOnly != tt.showOnly {
				t.Errorf("IsExportCommand(%q): showOnly=%v, want %v", tt.input, showOnly, tt.showOnly)
			}
		}
	}
}

func TestIsUnsetCommand(t *testing.T) {
	tests := []struct {
		input   string
		isUnset bool
		varName string
	}{
		{"unset FOO", true, "FOO"},
		{"unset MY_VAR", true, "MY_VAR"},
		{"  unset  VAR  ", true, "VAR"},

		// Not unset commands
		{"unset", false, ""},
		{"unset ", false, ""},
		{"unsets FOO", false, ""},
		{"echo unset", false, ""},
		{"", false, ""},
	}

	for _, tt := range tests {
		isUnset, varName := IsUnsetCommand(tt.input)
		if isUnset != tt.isUnset {
			t.Errorf("IsUnsetCommand(%q): isUnset=%v, want %v", tt.input, isUnset, tt.isUnset)
		}
		if isUnset && varName != tt.varName {
			t.Errorf("IsUnsetCommand(%q): varName=%q, want %q", tt.input, varName, tt.varName)
		}
	}
}

func TestHandleExportCommand_SetVar(t *testing.T) {
	state := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    make(map[string]string),
	}

	// Set a new variable
	newState, output := state.HandleExportCommand("FOO", "bar", false)
	if output != "" {
		t.Errorf("Expected no output when setting var, got: %s", output)
	}
	if newState.EnvVars["FOO"] != "bar" {
		t.Errorf("Expected FOO=bar, got FOO=%s", newState.EnvVars["FOO"])
	}
}

func TestHandleExportCommand_ShowVar(t *testing.T) {
	state := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    map[string]string{"FOO": "bar"},
	}

	// Show existing variable
	_, output := state.HandleExportCommand("FOO", "", true)
	if output != "FOO=bar" {
		t.Errorf("Expected 'FOO=bar', got: %s", output)
	}

	// Show non-existing variable
	_, output = state.HandleExportCommand("NOTSET", "", true)
	if output != "NOTSET: not set" {
		t.Errorf("Expected 'NOTSET: not set', got: %s", output)
	}
}

func TestHandleExportCommand_ListAll(t *testing.T) {
	state := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    map[string]string{"A": "1", "B": "2"},
	}

	_, output := state.HandleExportCommand("", "", true)
	if !strings.Contains(output, "A=1") || !strings.Contains(output, "B=2") {
		t.Errorf("Expected output to contain A=1 and B=2, got: %s", output)
	}

	// Empty env vars
	emptyState := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    make(map[string]string),
	}
	_, output = emptyState.HandleExportCommand("", "", true)
	if output != "(no exported variables)" {
		t.Errorf("Expected '(no exported variables)', got: %s", output)
	}
}

func TestHandleUnsetCommand(t *testing.T) {
	state := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    map[string]string{"FOO": "bar", "BAZ": "qux"},
	}

	// Unset existing variable
	newState, output := state.HandleUnsetCommand("FOO")
	if output != "" {
		t.Errorf("Expected no output when unsetting var, got: %s", output)
	}
	if _, ok := newState.EnvVars["FOO"]; ok {
		t.Error("Expected FOO to be removed")
	}
	if newState.EnvVars["BAZ"] != "qux" {
		t.Error("Expected BAZ to remain unchanged")
	}

	// Unset non-existing variable
	_, output = state.HandleUnsetCommand("NOTSET")
	if !strings.Contains(output, "not set") {
		t.Errorf("Expected 'not set' error, got: %s", output)
	}
}

func TestExpandVariables(t *testing.T) {
	state := ShellState{
		CurrentDir: "/home/test",
		PrevDir:    "/tmp",
		EnvVars: map[string]string{
			"FOO":  "bar",
			"NAME": "world",
			"PATH": "/usr/bin",
		},
	}

	tests := []struct {
		input    string
		expected string
	}{
		// No expansion needed
		{"hello world", "hello world"},
		{"", ""},

		// Simple $VAR expansion
		{"echo $FOO", "echo bar"},
		{"hello $NAME", "hello world"},
		{"$FOO$NAME", "barworld"},

		// ${VAR} expansion
		{"echo ${FOO}", "echo bar"},
		{"${FOO}${NAME}", "barworld"},
		{"prefix${FOO}suffix", "prefixbarsuffix"},

		// Partial matches - longer names should not be replaced
		{"$FOOBAR", "$FOOBAR"}, // Not in env, not expanded
	}

	for _, tt := range tests {
		result := state.ExpandVariables(tt.input)
		if result != tt.expected {
			t.Errorf("ExpandVariables(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestHandleCdCommand_PreservesEnvVars(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	state := ShellState{
		CurrentDir: tmpDir,
		PrevDir:    "/prev",
		EnvVars:    map[string]string{"FOO": "bar"},
	}

	newState, errMsg := state.HandleCdCommand("sub")
	if errMsg != "" {
		t.Errorf("Unexpected error: %s", errMsg)
	}

	// EnvVars should be preserved
	if newState.EnvVars["FOO"] != "bar" {
		t.Errorf("Expected EnvVars to be preserved, FOO=%s", newState.EnvVars["FOO"])
	}
}

func TestExportCommand_EmptyValue(t *testing.T) {
	state := ShellState{
		CurrentDir: "/tmp",
		EnvVars:    make(map[string]string),
	}

	// Set empty value
	newState, _ := state.HandleExportCommand("EMPTY", "", false)
	if v, ok := newState.EnvVars["EMPTY"]; !ok {
		t.Error("Expected EMPTY to be set")
	} else if v != "" {
		t.Errorf("Expected empty value, got: %q", v)
	}
}

func TestExportCommand_ValueWithEquals(t *testing.T) {
	// Test "export FOO=bar=baz" - value should be "bar=baz"
	isExport, varName, value, showOnly := IsExportCommand("export FOO=bar=baz")
	if !isExport {
		t.Fatal("Expected isExport=true")
	}
	if varName != "FOO" {
		t.Errorf("Expected varName=FOO, got %q", varName)
	}
	if value != "bar=baz" {
		t.Errorf("Expected value='bar=baz', got %q", value)
	}
	if showOnly {
		t.Error("Expected showOnly=false")
	}
}

// Background job tests

func TestIsBackgroundCommand(t *testing.T) {
	tests := []struct {
		input string
		isBg  bool
		cmd   string
	}{
		{"sleep 10 &", true, "sleep 10"},
		{"ls -la &", true, "ls -la"},
		{"  cmd &  ", true, "cmd"},
		{"cmd", false, "cmd"},
		{"", false, ""},
		{"&", true, ""}, // edge case
	}

	for _, tt := range tests {
		isBg, cmd := IsBackgroundCommand(tt.input)
		if isBg != tt.isBg {
			t.Errorf("IsBackgroundCommand(%q): isBg=%v, want %v", tt.input, isBg, tt.isBg)
		}
		if cmd != tt.cmd {
			t.Errorf("IsBackgroundCommand(%q): cmd=%q, want %q", tt.input, cmd, tt.cmd)
		}
	}
}

func TestIsJobsCommand(t *testing.T) {
	tests := []struct {
		input  string
		isJobs bool
	}{
		{"jobs", true},
		{"  jobs  ", true},
		{"jobs -l", false},
		{"echo jobs", false},
		{"jobslist", false},
		{"", false},
	}

	for _, tt := range tests {
		isJobs := IsJobsCommand(tt.input)
		if isJobs != tt.isJobs {
			t.Errorf("IsJobsCommand(%q) = %v, want %v", tt.input, isJobs, tt.isJobs)
		}
	}
}

func TestIsKillCommand(t *testing.T) {
	tests := []struct {
		input  string
		isKill bool
		jobID  int
	}{
		{"kill %1", true, 1},
		{"kill %42", true, 42},
		{"kill  %3", true, 3},
		{"kill %0", true, 0},   // job 0 is invalid
		{"kill %-1", true, 0},  // negative is invalid
		{"kill %abc", true, 0}, // malformed
		{"kill %", true, 0},    // malformed
		{"kill 123", false, 0}, // not a job reference
		{"kill -9 123", false, 0},
		{"killall", false, 0},
		{"", false, 0},
	}

	for _, tt := range tests {
		isKill, jobID := IsKillCommand(tt.input)
		if isKill != tt.isKill {
			t.Errorf("IsKillCommand(%q): isKill=%v, want %v", tt.input, isKill, tt.isKill)
		}
		if isKill && jobID != tt.jobID {
			t.Errorf("IsKillCommand(%q): jobID=%d, want %d", tt.input, jobID, tt.jobID)
		}
	}
}

func TestJobManager_AddAndList(t *testing.T) {
	jm := NewJobManager()

	// Add jobs
	job1 := jm.AddJob("sleep 10", nil)
	job2 := jm.AddJob("ls -la", nil)

	if job1.ID != 1 {
		t.Errorf("Expected first job ID=1, got %d", job1.ID)
	}
	if job2.ID != 2 {
		t.Errorf("Expected second job ID=2, got %d", job2.ID)
	}

	// List jobs
	jobs := jm.ListJobs()
	if len(jobs) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(jobs))
	}

	// Get specific job
	retrieved := jm.GetJob(1)
	if retrieved == nil {
		t.Fatal("Expected to get job 1")
	}
	if retrieved.Command != "sleep 10" {
		t.Errorf("Expected command='sleep 10', got %q", retrieved.Command)
	}
	if retrieved.Status != JobRunning {
		t.Errorf("Expected status=running, got %s", retrieved.Status)
	}
}

func TestJobManager_CancelJob(t *testing.T) {
	jm := NewJobManager()

	cancelled := false
	cancelFn := func() { cancelled = true }

	job := jm.AddJob("sleep 10", cancelFn)

	// Cancel the job
	err := jm.CancelJob(job.ID)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !cancelled {
		t.Error("Expected cancel function to be called")
	}

	// Try to cancel non-existent job
	err = jm.CancelJob(999)
	if err == nil {
		t.Error("Expected error for non-existent job")
	}
}

func TestJobManager_MarkComplete(t *testing.T) {
	jm := NewJobManager()
	job := jm.AddJob("test", nil)

	jm.MarkComplete(job.ID, JobCompleted, nil)

	retrieved := jm.GetJob(job.ID)
	if retrieved.Status != JobCompleted {
		t.Errorf("Expected status=completed, got %s", retrieved.Status)
	}
	if retrieved.EndTime.IsZero() {
		t.Error("Expected EndTime to be set")
	}
}

func TestJobManager_CancelCompletedJob(t *testing.T) {
	jm := NewJobManager()
	job := jm.AddJob("test", nil)

	// Mark complete
	jm.MarkComplete(job.ID, JobCompleted, nil)

	// Try to cancel - should fail
	err := jm.CancelJob(job.ID)
	if err == nil {
		t.Error("Expected error when cancelling completed job")
	}
}

func TestJobStatus_String(t *testing.T) {
	tests := []struct {
		status JobStatus
		str    string
	}{
		{JobRunning, "running"},
		{JobCompleted, "completed"},
		{JobFailed, "failed"},
		{JobCancelled, "cancelled"},
		{JobStatus(99), "unknown"},
	}

	for _, tt := range tests {
		if tt.status.String() != tt.str {
			t.Errorf("JobStatus(%d).String() = %q, want %q", tt.status, tt.status.String(), tt.str)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLines  int
		remaining int
	}{
		{
			name:      "short output",
			input:     "line1\nline2\nline3",
			maxLines:  10,
			remaining: 0,
		},
		{
			name:      "exact limit",
			input:     "line1\nline2\nline3",
			maxLines:  3,
			remaining: 0,
		},
		{
			name:      "needs truncation",
			input:     "1\n2\n3\n4\n5\n6\n7\n8\n9\n10",
			maxLines:  5,
			remaining: 5,
		},
		{
			name:      "empty",
			input:     "",
			maxLines:  10,
			remaining: 0,
		},
		{
			name:      "single line",
			input:     "just one line",
			maxLines:  10,
			remaining: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, remaining := TruncateOutput(tt.input, tt.maxLines)
			if remaining != tt.remaining {
				t.Errorf("TruncateOutput remaining=%d, want %d", remaining, tt.remaining)
			}
			if remaining > 0 {
				if !strings.Contains(output, "[truncated,") {
					t.Error("Expected truncation message in output")
				}
			}
		})
	}
}

func TestTruncateCommand(t *testing.T) {
	tests := []struct {
		cmd    string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a very long command", 15, "this is a ve..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		got := truncateCommand(tt.cmd, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateCommand(%q, %d) = %q, want %q", tt.cmd, tt.maxLen, got, tt.want)
		}
	}
}

func TestShellState_FormatJobsList(t *testing.T) {
	state := NewShellState()

	// Empty jobs list
	output := state.FormatJobsList()
	if output != "No jobs" {
		t.Errorf("Expected 'No jobs' for empty list, got: %s", output)
	}

	// Add some jobs
	state.Jobs.AddJob("sleep 10", nil)
	state.Jobs.AddJob("ls -la", nil)
	state.Jobs.MarkComplete(2, JobCompleted, nil)

	output = state.FormatJobsList()
	if !strings.Contains(output, "[1]") || !strings.Contains(output, "[2]") {
		t.Errorf("Expected job list to contain job IDs, got: %s", output)
	}
	if !strings.Contains(output, "running") {
		t.Error("Expected job list to show running status")
	}
	if !strings.Contains(output, "completed") {
		t.Error("Expected job list to show completed status")
	}
}

func TestShellState_NilJobs(t *testing.T) {
	// Test with nil Jobs manager
	state := ShellState{
		CurrentDir: "/tmp",
		Jobs:       nil,
	}

	output := state.FormatJobsList()
	if output != "No jobs" {
		t.Errorf("Expected 'No jobs' for nil manager, got: %s", output)
	}
}

func TestCancelRunningCommand(t *testing.T) {
	state := NewShellState()

	// No running command
	if state.CancelRunningCommand() {
		t.Error("Expected false when no command running")
	}

	// Set up running command
	cancelled := false
	state.RunningCancel = func() { cancelled = true }

	if !state.CancelRunningCommand() {
		t.Error("Expected true when command was running")
	}
	if !cancelled {
		t.Error("Expected cancel function to be called")
	}
	if state.RunningCancel != nil {
		t.Error("Expected RunningCancel to be cleared")
	}
}
