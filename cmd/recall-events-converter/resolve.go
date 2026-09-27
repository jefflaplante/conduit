package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The gateway writes recall events to <workspace.context_dir>/memory/
// recall-events.jsonl (config.Config.RecallEventsPath). This tool derives the
// same path instead of hardcoding one deployment's location
// (conduit-31jg.40). Resolution order, first match wins:
//
//  1. $CONDUIT_RECALL_EVENTS
//  2. $CONDUIT_WORKSPACE/memory/recall-events.jsonl
//  3. workspace.context_dir from the gateway config: --config, else
//     $CONDUIT_CONFIG, else $CONDUIT_HOME/config.json, else
//     ~/ocgo/config.json (install.sh's default CONDUIT_HOME layout).

const recallEventsRel = "memory/recall-events.jsonl"

func resolveEventsFile(configFlag string) (string, error) {
	if p := os.Getenv("CONDUIT_RECALL_EVENTS"); p != "" {
		return expandHome(p), nil
	}
	if ws := os.Getenv("CONDUIT_WORKSPACE"); ws != "" {
		return filepath.Join(expandHome(ws), recallEventsRel), nil
	}

	cfgPath := configFlag
	if cfgPath == "" {
		cfgPath = os.Getenv("CONDUIT_CONFIG")
	}
	if cfgPath == "" {
		home := os.Getenv("CONDUIT_HOME")
		if home == "" {
			home = "~/ocgo"
		}
		cfgPath = filepath.Join(expandHome(home), "config.json")
	}
	cfgPath = expandHome(cfgPath)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", fmt.Errorf("cannot derive recall-events path (pass --file, --config or set CONDUIT_WORKSPACE): %w", err)
	}
	var cfg struct {
		Workspace struct {
			ContextDir string `json:"context_dir"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	ws := expandHome(os.ExpandEnv(cfg.Workspace.ContextDir))
	if ws == "" {
		return "", fmt.Errorf("%s has no workspace.context_dir; pass --file", cfgPath)
	}
	if !filepath.IsAbs(ws) {
		// The gateway resolves a relative workspace against its working
		// directory; the config's directory is the closest stand-in here.
		ws = filepath.Join(filepath.Dir(cfgPath), ws)
	}
	return filepath.Join(ws, recallEventsRel), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
