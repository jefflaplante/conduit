package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"conduit/internal/config"
)

// loadExistingConfig loads the config file for a CLI subcommand that needs
// one (briefing, chain, tui). config.Load writes a default config.json when
// the file is missing — right for first-run `server`, wrong for a CLI
// command, which would silently create a file and run against defaults
// (e.g. a fresh gateway.db) instead of the real config. A missing file is an
// error instead (conduit-31jg.87; `status` got the same fix in conduit-1qcg).
func loadExistingConfig(path string) (*config.Config, error) {
	if path == "" {
		return nil, errors.New("no config file given (use --config)")
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("config file %s not found (use --config to point at the gateway's config; this command does not create one)", path)
		}
		return nil, err
	}
	return config.Load(path)
}
