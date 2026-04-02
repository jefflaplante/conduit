package tui

import (
	"os"
	"path/filepath"
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
