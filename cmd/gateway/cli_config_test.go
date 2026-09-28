package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conduit-31jg.87: briefing/chain/tui must not write a default config.json
// when --config points at a missing file; they fail with a clear error.
func TestCLICommands_MissingConfigFailsWithoutWritingFile(t *testing.T) {
	prev := cfgFile
	t.Cleanup(func() { cfgFile = prev })

	cases := map[string]func() error{
		"chain list":        func() error { return runChainList(false) },
		"chain delete":      func() error { return runChainDelete("x") },
		"chain validate":    func() error { return runChainValidate("x") },
		"briefing generate": func() error { return runBriefingGenerate("s", false, 10) },
		"tui config": func() error {
			_, err := loadGatewayConfigForTUI(cfgFile)
			return err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			cfgFile = filepath.Join(t.TempDir(), "config.json")
			err := run()
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Fatalf("err = %v, want a config-not-found error", err)
			}
			if _, statErr := os.Stat(cfgFile); statErr == nil {
				t.Fatal("command wrote a default config file")
			}
		})
	}
}

func TestLoadExistingConfig(t *testing.T) {
	if _, err := loadExistingConfig(""); err == nil {
		t.Error("empty path: want error")
	}
	path := writeTestConfig(t, 18999)
	cfg, err := loadExistingConfig(path)
	if err != nil {
		t.Fatalf("existing config: %v", err)
	}
	if cfg.Port != 18999 {
		t.Errorf("port = %d, want 18999", cfg.Port)
	}
}
